// SPDX-License-Identifier: GPL-3.0-only

package webprov

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(server *httptest.Server) *Client {
	return New(5 * time.Second).WithHTTP(server.Client()).WithBase(server.URL)
}

const sessionJWT = "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ1c2VyX2ZpeHR1cmUiLCJleHAiOjQwNzA5MDg4MDB9.sig"

func TestCredentialRoundTrip(t *testing.T) {
	cred := Credential{
		UserAgent:  DefaultUserAgent,
		DistinctID: "device-1",
		Venice:     []Cookie{{Name: "cf_clearance", Value: "clear"}, {Name: "empty", Value: ""}},
		Clerk:      []Cookie{{Name: "__client", Value: "client"}},
	}
	decoded, err := ParseCredential(cred.Encode())
	if err != nil || decoded.cookie("cf_clearance") != "clear" || decoded.clerkCookie("__client") != "client" ||
		decoded.DistinctID != "device-1" || decoded.UserAgent != DefaultUserAgent || len(decoded.Venice) != 1 {
		t.Fatalf("round trip failed: %+v %v", decoded, err)
	}
	if _, err := ParseCredential("not json"); err == nil {
		t.Fatal("invalid credential accepted")
	}
	if _, err := CaptureFromHeader("cf_clearance=abc; __client=def"); err != nil {
		t.Fatal(err)
	}
	pasted, _ := CaptureFromHeader("__cf_bm=bm; cf_clearance=abc")
	if pasted.cookie("cf_clearance") != "abc" || pasted.clerkCookie("__client") != "" {
		t.Fatalf("header parse lost cookies: %+v", pasted)
	}
	if _, err := CaptureFromHeader("__client=only"); err == nil {
		t.Fatal("header without clearance accepted")
	}
}

func TestChatStreamsAndAuthenticates(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/user/client_attestation/mint":
			fmt.Fprint(w, `{"expiresAt":1,"tokens":[{"token":"`+sessionJWT+`"}]}`)
		case "/v1/client":
			if cookie, err := r.Cookie("__client"); err != nil || cookie.Value != "client-cookie" {
				t.Errorf("clerk lost the client cookie: %v", err)
			}
			fmt.Fprint(w, `{"response":{"sessions":[{"id":"sess_1","status":"active"}]}}`)
		case "/v1/client/sessions/sess_1/tokens":
			fmt.Fprintf(w, `{"jwt":%q}`, sessionJWT)
		case "/inference/chat":
			calls.Add(1)
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if payload["modelId"] != "venice-uncensored-1-2" || payload["userId"] != "user_fixture" ||
				r.Header.Get("Authorization") != "Bearer "+sessionJWT ||
				r.Header.Get("x-venice-client-attestation") != sessionJWT ||
				r.Header.Get("x-venice-distinct-id") != "device-1" || cookieValue(r, "cf_clearance") != "clear" {
				t.Errorf("unexpected upstream request: %v %v", payload, r.Header)
			}
			prompt := payload["prompt"].([]any)
			if len(prompt) != 1 || prompt[0].(map[string]any)["role"] != "user" {
				t.Errorf("prompt not forwarded: %v", prompt)
			}
			if calls.Load() == 1 && prompt[0].(map[string]any)["content"] != "hello" {
				t.Errorf("prompt text changed: %v", prompt)
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `{"kind":"meta","servingModelId":"venice-uncensored-1-2"}
{"kind":"content","content":"Hel"}
{"content":"lo","kind":"content"}
{"kind":"meta","completion_id":"done"}
`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	client := testClient(server)
	cred := Credential{DistinctID: "device-1", Venice: []Cookie{{Name: "cf_clearance", Value: "clear"}},
		Clerk: []Cookie{{Name: "__client", Value: "client-cookie"}}}
	var text strings.Builder
	serving := ""
	err := client.Chat(context.Background(), cred, Request{Model: "venice-uncensored-1-2", Messages: []Message{{Role: "user", Content: "hello"}}},
		func(event Event) error {
			if event.Kind == "content" {
				text.WriteString(event.Content)
			}
			if event.ServingModel != "" {
				serving = event.ServingModel
			}
			return nil
		})
	if err != nil || text.String() != "Hello" || serving != "venice-uncensored-1-2" {
		t.Fatalf("text=%q serving=%q err=%v", text.String(), serving, err)
	}
	// A second call must reuse the minted Clerk token and attestation.
	if err := client.Chat(context.Background(), cred, Request{Model: "venice-uncensored-1-2", Messages: []Message{{Role: "user", Content: "again"}}},
		func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func cookieValue(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func TestAnonymousChatSkipsClerk(t *testing.T) {
	var clerkCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/user/client_attestation/mint":
			fmt.Fprint(w, `{"tokens":[{"token":"anon"}]}`)
		case "/inference/chat":
			if r.Header.Get("Authorization") != "" {
				t.Error("anonymous request carried an authorization header")
			}
			fmt.Fprint(w, `{"kind":"content","content":"ok"}`)
		default:
			clerkCalls.Add(1)
		}
	}))
	t.Cleanup(server.Close)
	var text strings.Builder
	err := testClient(server).Chat(context.Background(), Credential{}, Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}},
		func(event Event) error { text.WriteString(event.Content); return nil })
	if err != nil || text.String() != "ok" || clerkCalls.Load() != 0 {
		t.Fatalf("text=%q clerk=%d err=%v", text.String(), clerkCalls.Load(), err)
	}
}

func TestErrorClassification(t *testing.T) {
	for _, test := range []struct {
		status int
		body   string
		want   error
	}{
		{http.StatusTooManyRequests, "slow down", ErrRateLimited},
		{http.StatusUnauthorized, "expired", ErrSignInRequired},
		{http.StatusForbidden, "<html>cloudflare</html>", ErrCloudflare},
		{http.StatusRequestEntityTooLarge, `{"error":"userPromptTooLong"}`, ErrContextTooLong()},
		{http.StatusBadRequest, `{"error":"Model not found"}`, ErrUnknownModel()},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/mint") {
				fmt.Fprint(w, `{"tokens":[{"token":"anon"}]}`)
				return
			}
			w.WriteHeader(test.status)
			fmt.Fprint(w, test.body)
		}))
		err := testClient(server).Chat(context.Background(), Credential{},
			Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, func(Event) error { return nil })
		server.Close()
		if !errors.Is(err, test.want) {
			t.Errorf("status %d: got %v", test.status, err)
		}
	}
}

func TestForbiddenRetriesWithFreshAttestation(t *testing.T) {
	var mints atomic.Int32
	var chats atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/mint") {
			mints.Add(1)
			fmt.Fprintf(w, `{"tokens":[{"token":"token-%d"}]}`, mints.Load())
			return
		}
		if chats.Add(1) == 1 {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "<html>challenge</html>")
			return
		}
		if r.Header.Get("x-venice-client-attestation") == "token-1" {
			t.Error("stale attestation was replayed")
		}
		fmt.Fprint(w, `{"kind":"content","content":"ok"}`)
	}))
	t.Cleanup(server.Close)
	var text strings.Builder
	err := testClient(server).Chat(context.Background(), Credential{},
		Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}},
		func(event Event) error { text.WriteString(event.Content); return nil })
	if err != nil || text.String() != "ok" || mints.Load() != 2 || chats.Load() != 2 {
		t.Fatalf("text=%q mints=%d chats=%d err=%v", text.String(), mints.Load(), chats.Load(), err)
	}
}

func TestForbiddenTwiceReportsClearance(t *testing.T) {
	var chats atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/mint") {
			fmt.Fprint(w, `{"tokens":[{"token":"t"}]}`)
			return
		}
		chats.Add(1)
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "<html>challenge</html>")
	}))
	t.Cleanup(server.Close)
	err := testClient(server).Chat(context.Background(), Credential{},
		Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, func(Event) error { return nil })
	if !errors.Is(err, ErrCloudflare) || chats.Load() != 2 {
		t.Fatalf("err=%v chats=%d", err, chats.Load())
	}
}

func TestChatSerialisesAccount(t *testing.T) {
	var inFlight, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/mint") {
			fmt.Fprint(w, `{"tokens":[{"token":"t"},{"token":"t2"}]}`)
			return
		}
		current := inFlight.Add(1)
		for {
			old := peak.Load()
			if current <= old || peak.CompareAndSwap(old, current) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		inFlight.Add(-1)
		fmt.Fprint(w, `{"kind":"content","content":"ok"}`)
	}))
	t.Cleanup(server.Close)
	client := testClient(server)
	cred := Credential{DistinctID: "same"}
	var group sync.WaitGroup
	for range 4 {
		group.Go(func() {
			client.Chat(context.Background(), cred,
				Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, func(Event) error { return nil })
		})
	}
	group.Wait()
	if peak.Load() != 1 {
		t.Fatalf("peak concurrency was %d", peak.Load())
	}
}

func TestExpiredSignedSessionIsUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/mint"):
			fmt.Fprint(w, `{"tokens":[{"token":"t"}]}`)
		case strings.HasSuffix(r.URL.Path, "/tokens"):
			fmt.Fprint(w, `{"jwt":"eyJhbGciOiJub25lIn0.eyJzdWIiOiJ1c2VyX3giLCJleHAiOjQwNzA5MDg4MDB9.sig"}`)
		case strings.HasSuffix(r.URL.Path, "/v1/client"):
			fmt.Fprint(w, `{"response":{"sessions":[{"id":"s","status":"active"}]}}`)
		default:
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, "expired")
		}
	}))
	t.Cleanup(server.Close)
	cred := Credential{Clerk: []Cookie{{Name: "__client", Value: "c"}}}
	err := testClient(server).Chat(context.Background(), cred,
		Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, func(Event) error { return nil })
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v", err)
	}
}

func TestClerkExpiryAndForget(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	mints := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/mint") {
			fmt.Fprint(w, `{"tokens":[{"token":"anon"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/tokens") {
			fmt.Fprintf(w, `{"jwt":%q}`, sessionJWT)
			return
		}
		fmt.Fprint(w, `{"response":{"sessions":[{"id":"s","status":"active"}]}}`)
		mints++
	}))
	t.Cleanup(server.Close)
	client := testClient(server)
	client.now = func() time.Time { return now }
	cred := Credential{Clerk: []Cookie{{Name: "__client", Value: "c"}}}
	if _, err := client.user(context.Background(), cred); err != nil {
		t.Fatal(err)
	}
	client.user(context.Background(), cred)
	if mints != 1 {
		t.Fatalf("token cache missed: %d", mints)
	}
	client.Forget(cred)
	client.user(context.Background(), cred)
	if mints != 2 {
		t.Fatalf("forget did not evict: %d", mints)
	}
}
