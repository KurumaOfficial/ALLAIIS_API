// SPDX-License-Identifier: GPL-3.0-only

package gateway_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/gateway"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/router"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

// veniceFixture serves the attestation and inference calls a web provider makes.
func veniceFixture(t *testing.T, reply func(w http.ResponseWriter, body map[string]any)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/user/client_attestation/mint":
			fmt.Fprint(w, `{"tokens":[{"token":"attestation"}]}`)
		case "/inference/chat":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			reply(w, body)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func webGateway(t *testing.T, upstream string) (http.Handler, *vault.Store) {
	t.Helper()
	store, _ := setup(t, upstream)
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{
		"venice-web-fixture": {BaseURL: upstream, Transport: config.TransportVeniceWeb},
	}
	cfg.Models = []config.Route{{
		ID: "venice-uncensored-venice-web-fixture", Provider: "venice-web-fixture",
		UpstreamModel: "venice-uncensored-1-2", Privacy: config.ZDR,
	}}
	cred := webprov.Credential{DistinctID: "device", Venice: []webprov.Cookie{{Name: "cf_clearance", Value: "clear"}}}
	if _, err := store.Import(t.Context(), []vault.Entry{{
		Provider: "venice-web-fixture", Endpoint: upstream, Label: "personal", Key: cred.Encode(),
	}}); err != nil {
		t.Fatal(err)
	}
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	web := webprov.New(5 * time.Second).WithHTTP(http.DefaultClient).WithBase(upstream)
	return gateway.NewWithWeb(routes, store.ClientKey(), cfg.MinPrivacy, web), store
}

const webModel = "venice-uncensored-venice-web-fixture"

func TestWebCompletionStreams(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		if body["modelId"] != "venice-uncensored-1-2" || body["reasoning"] != false {
			t.Errorf("unexpected payload %v", body)
		}
		fmt.Fprint(w, `{"kind":"meta","servingModelId":"venice-uncensored-1-2"}
{"kind":"content","content":"Hello"}
{"kind":"content","content":", world."}
`)
	})
	handler, store := webGateway(t, server.URL)
	result := complete(handler, store.ClientKey(), "",
		`{"model":"`+webModel+`","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}],"stream":true}`)
	body := result.Body.String()
	if result.Code != http.StatusOK || result.Header().Get("X-ALLAIIS-Transport-Tier") != "tier_c3" ||
		result.Header().Get("X-ALLAIIS-Emulated-Features") != "usage_estimate" ||
		!strings.Contains(body, `"content":"Hello`) || !strings.Contains(body, `"finish_reason":"stop"`) ||
		!strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("unexpected stream: %d %s", result.Code, body)
	}
}

func TestWebCompletionJSON(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		fmt.Fprint(w, `{"kind":"content","content":"full answer"}`)
	})
	handler, store := webGateway(t, server.URL)
	result := complete(handler, store.ClientKey(), "",
		`{"model":"`+webModel+`","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	var reply struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Estimated bool `json:"estimated"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &reply); err != nil {
		t.Fatalf("%v: %s", err, result.Body)
	}
	if result.Code != http.StatusOK || reply.Choices[0].Message.Content != "full answer" || !reply.Usage.Estimated {
		t.Fatalf("unexpected reply: %d %s", result.Code, result.Body)
	}
}

func TestWebCompletionErrorsAndCooldown(t *testing.T) {
	var limited atomic.Bool
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		if limited.Load() {
			w.Header().Set("x-ratelimit-resets", fmt.Sprint(time.Now().Add(3*time.Minute).UnixMilli()))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "<html>challenge</html>")
	})
	handler, store := webGateway(t, server.URL)
	request := `{"model":"` + webModel + `","messages":[{"role":"user","content":"hi"}]}`
	denied := complete(handler, store.ClientKey(), "", request)
	if denied.Code != http.StatusBadGateway || !strings.Contains(denied.Body.String(), "cloudflare_challenge") {
		t.Fatalf("403 mapped to %d %s", denied.Code, denied.Body.String())
	}
	limited.Store(true)
	rate := complete(handler, store.ClientKey(), "s", request)
	if rate.Code != http.StatusTooManyRequests || rate.Header().Get("Retry-After") == "" {
		t.Fatalf("429 mapped to %d", rate.Code)
	}
	if seconds, _ := strconv.Atoi(rate.Header().Get("Retry-After")); seconds < 120 {
		t.Fatalf("published reset was ignored, Retry-After=%q", rate.Header().Get("Retry-After"))
	}
	blocked := complete(handler, store.ClientKey(), "other", request)
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("cooldown not honored: %d", blocked.Code)
	}
}

func TestHugeRateLimitResetIsCapped(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		w.Header().Set("x-ratelimit-resets", fmt.Sprint(time.Now().Add(17*time.Hour).UnixMilli()))
		w.WriteHeader(http.StatusTooManyRequests)
	})
	handler, store := webGateway(t, server.URL)
	request := `{"model":"` + webModel + `","messages":[{"role":"user","content":"hi"}]}`
	rate := complete(handler, store.ClientKey(), "s", request)
	if rate.Code != http.StatusTooManyRequests {
		t.Fatalf("429 mapped to %d", rate.Code)
	}
	seconds, err := strconv.Atoi(rate.Header().Get("Retry-After"))
	if err != nil || seconds > 600 || seconds < 120 {
		t.Fatalf("cooldown was not capped, Retry-After=%q", rate.Header().Get("Retry-After"))
	}
}

func TestWebProviderRejectsOtherProtocols(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		io.WriteString(w, `{"kind":"content","content":"x"}`)
	})
	store, _ := setup(t, server.URL)
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{"web": {BaseURL: server.URL, Transport: config.TransportVeniceWeb}}
	cfg.Models = []config.Route{{ID: "alias-web", Provider: "web", UpstreamModel: "m", Privacy: config.Consumer}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Models[0].Protocol = config.Responses
	if err := cfg.Validate(); err == nil {
		t.Fatal("responses protocol accepted for a web provider")
	}
	cfg.Models[0].Protocol = config.Chat
	if _, err := store.Import(t.Context(), []vault.Entry{{Provider: "web", Endpoint: server.URL, Label: "l", Key: "{}"}}); err != nil {
		t.Fatal(err)
	}
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	handler := gateway.NewWithWeb(routes, store.ClientKey(), config.Consumer,
		webprov.New(time.Second).WithHTTP(http.DefaultClient).WithBase(server.URL))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"alias-web","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"content":"x"`) {
		t.Fatalf("got %d %s", result.Code, result.Body.String())
	}
}

func TestWebVisionUsesAgentMultipart(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("pngbytes"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/mint"):
			io.WriteString(w, `{"tokens":[{"token":"t"}]}`)
		case r.URL.Path == "/inference/chat/agents/venice":
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				t.Errorf("content type %q", r.Header.Get("Content-Type"))
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "input_image") || !strings.Contains(string(body), "pngbytes") {
				t.Errorf("image missing from body")
			}
			io.WriteString(w, "event: response.output_text.delta\ndata: {\"delta\":\"Red\"}\n\ndata: [DONE]\n\n")
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	handler, store := webGateway(t, server.URL)
	payload := `{"model":"` + webModel + `","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"colour?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + png + `"}}]}]}`
	result := complete(handler, store.ClientKey(), "", payload)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "Red") {
		t.Fatalf("got %d %s", result.Code, result.Body.String())
	}
}

func TestWebEmptyFirstEventKeepsLaterText(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		fmt.Fprint(w, `{"kind":"content","content":""}
{"kind":"content","content":"Hello"}
{"kind":"content","content":" there"}
`)
	})
	handler, store := webGateway(t, server.URL)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"`+webModel+`","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
	var reply struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if got := reply.Choices[0].Message.Content; got != "Hello there" {
		t.Fatalf("content %q", got)
	}
}

func TestWebEmptyReplyRetriesOnce(t *testing.T) {
	calls := 0
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		calls++
		if calls == 1 {
			fmt.Fprint(w, `{"kind":"content","content":"processing"}`)
			return
		}
		fmt.Fprint(w, `{"kind":"content","content":"Recovered"}`)
	})
	handler, store := webGateway(t, server.URL)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"`+webModel+`","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "Recovered") {
		t.Fatalf("retry failed: %d %s", result.Code, result.Body)
	}
}

func TestCapabilitiesShapeTheRequest(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		if body["reasoning"] != true {
			t.Errorf("reasoning not forced: %v", body["reasoning"])
		}
		if body["maxTokens"] != float64(64) {
			t.Errorf("max tokens not clamped: %v", body["maxTokens"])
		}
		if body["temperature"] != float64(0.3) {
			t.Errorf("default temperature missing: %v", body["temperature"])
		}
		fmt.Fprint(w, "{\"kind\":\"content\",\"content\":\"ok\"}\n")
	})
	store, cfg := setup(t, server.URL)
	cfg.Providers = map[string]config.Provider{"web": {BaseURL: server.URL, Transport: config.TransportVeniceWeb}}
	thinking := true
	temperature := 0.3
	cfg.Models = []config.Route{{
		ID: "capable", Provider: "web", UpstreamModel: "m", Privacy: config.Consumer,
		Reasoning: &thinking, MaxResponse: 64, Temperature: &temperature,
	}}
	if _, err := store.Import(t.Context(), []vault.Entry{{
		Provider: "web", Endpoint: server.URL, Label: "one",
		Key: webprov.Credential{DistinctID: "d", Venice: []webprov.Cookie{{Name: "cf_clearance", Value: "c"}}}.Encode(),
	}}); err != nil {
		t.Fatal(err)
	}
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	web := webprov.New(5 * time.Second).WithHTTP(http.DefaultClient).WithBase(server.URL)
	handler := gateway.NewWithWeb(routes, store.ClientKey(), cfg.MinPrivacy, web)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"capable","max_tokens":4096,"messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

func TestContextLimitRejectsBeforeSending(t *testing.T) {
	sent := false
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		sent = true
		fmt.Fprint(w, "{\"kind\":\"content\",\"content\":\"ok\"}\n")
	})
	store, cfg := setup(t, server.URL)
	cfg.Providers = map[string]config.Provider{"web": {BaseURL: server.URL, Transport: config.TransportVeniceWeb}}
	cfg.Models = []config.Route{{
		ID: "small", Provider: "web", UpstreamModel: "m", Privacy: config.Consumer, MaxContext: 10,
	}}
	if _, err := store.Import(t.Context(), []vault.Entry{{
		Provider: "web", Endpoint: server.URL, Label: "one",
		Key: webprov.Credential{DistinctID: "d", Venice: []webprov.Cookie{{Name: "cf_clearance", Value: "c"}}}.Encode(),
	}}); err != nil {
		t.Fatal(err)
	}
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	web := webprov.New(5 * time.Second).WithHTTP(http.DefaultClient).WithBase(server.URL)
	handler := gateway.NewWithWeb(routes, store.ClientKey(), cfg.MinPrivacy, web)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"small","messages":[{"role":"user","content":"`+strings.Repeat("long ", 200)+`"}]}`))
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusBadRequest || !strings.Contains(result.Body.String(), "context_length_exceeded") || sent {
		t.Fatalf("got %d sent=%v %s", result.Code, sent, result.Body)
	}
}

func TestReasoningEffortAndWebSearchReachThePayload(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		if body["reasoningEffort"] != "high" || body["webEnabled"] != true {
			t.Errorf("payload %v", body)
		}
		fmt.Fprint(w, "{\"kind\":\"content\",\"content\":\"ok\"}\n")
	})
	store, cfg := setup(t, server.URL)
	cfg.Providers = map[string]config.Provider{"web": {BaseURL: server.URL, Transport: config.TransportVeniceWeb}}
	cfg.Models = []config.Route{{
		ID: "searcher", Provider: "web", UpstreamModel: "m", Privacy: config.Consumer, WebSearch: true,
	}}
	if _, err := store.Import(t.Context(), []vault.Entry{{
		Provider: "web", Endpoint: server.URL, Label: "one",
		Key: webprov.Credential{DistinctID: "d", Venice: []webprov.Cookie{{Name: "cf_clearance", Value: "c"}}}.Encode(),
	}}); err != nil {
		t.Fatal(err)
	}
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	web := webprov.New(5 * time.Second).WithHTTP(http.DefaultClient).WithBase(server.URL)
	handler := gateway.NewWithWeb(routes, store.ClientKey(), cfg.MinPrivacy, web)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"searcher","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

func TestMessagesThinkingBudgetBecomesEffort(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		if body["reasoningEffort"] != "medium" || body["reasoning"] != true {
			t.Errorf("payload %v", body)
		}
		fmt.Fprint(w, `{}`+"\n")
	})
	store, cfg := setup(t, server.URL)
	cfg.Providers = map[string]config.Provider{"web": {BaseURL: server.URL, Transport: config.TransportVeniceWeb}}
	cfg.Models = []config.Route{{
		ID: "thinker", Provider: "web", UpstreamModel: "m", Privacy: config.Consumer, Protocol: config.Messages,
	}}
	if _, err := store.Import(t.Context(), []vault.Entry{{
		Provider: "web", Endpoint: server.URL, Label: "one",
		Key: webprov.Credential{DistinctID: "d", Venice: []webprov.Cookie{{Name: "cf_clearance", Value: "c"}}}.Encode(),
	}}); err != nil {
		t.Fatal(err)
	}
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	web := webprov.New(5 * time.Second).WithHTTP(http.DefaultClient).WithBase(server.URL)
	handler := gateway.NewWithWeb(routes, store.ClientKey(), cfg.MinPrivacy, web)
	body := `{"model":"thinker","max_tokens":64,"thinking":{"type":"enabled","budget_tokens":4096},` +
		`"messages":[{"role":"user","content":"hi"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	request.Header.Set("x-api-key", store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

func TestToolLimitRejects(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		t.Error("sent despite the tool limit")
		fmt.Fprint(w, `{}`+"\n")
	})
	store, cfg := setup(t, server.URL)
	cfg.Providers = map[string]config.Provider{"web": {BaseURL: server.URL, Transport: config.TransportVeniceWeb}}
	cfg.Models = []config.Route{{
		ID: "narrow", Provider: "web", UpstreamModel: "m", Privacy: config.Consumer, MaxTools: 1,
	}}
	if _, err := store.Import(t.Context(), []vault.Entry{{
		Provider: "web", Endpoint: server.URL, Label: "one",
		Key: webprov.Credential{DistinctID: "d", Venice: []webprov.Cookie{{Name: "cf_clearance", Value: "c"}}}.Encode(),
	}}); err != nil {
		t.Fatal(err)
	}
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	web := webprov.New(5 * time.Second).WithHTTP(http.DefaultClient).WithBase(server.URL)
	handler := gateway.NewWithWeb(routes, store.ClientKey(), cfg.MinPrivacy, web)
	tools := `[{"type":"function","function":{"name":"a","parameters":{"type":"object"}}},` +
		`{"type":"function","function":{"name":"b","parameters":{"type":"object"}}}]`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"narrow","messages":[{"role":"user","content":"hi"}],"tools":`+tools+`}`))
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusBadRequest || !strings.Contains(result.Body.String(), "too_many_tools") {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

func TestStrictOrderMergesConsecutiveTurns(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		messages := body["prompt"].([]any)
		roles := make([]string, 0, len(messages))
		for _, raw := range messages {
			roles = append(roles, raw.(map[string]any)["role"].(string))
		}
		if strings.Join(roles, ",") != "user,assistant,user" {
			t.Fatalf("turns not merged: %v", roles)
		}
		last := messages[len(messages)-1].(map[string]any)["content"].(string)
		if !strings.Contains(last, "second") || !strings.Contains(last, "and more") {
			t.Fatalf("merged content lost: %q", last)
		}
		fmt.Fprint(w, "{\"kind\":\"content\",\"content\":\"ok\"}\n")
	})
	store, cfg := setup(t, server.URL)
	cfg.Providers = map[string]config.Provider{"web": {BaseURL: server.URL, Transport: config.TransportVeniceWeb}}
	cfg.Models = []config.Route{{
		ID: "strict", Provider: "web", UpstreamModel: "m", Privacy: config.Consumer, StrictOrder: true,
	}}
	if _, err := store.Import(t.Context(), []vault.Entry{{
		Provider: "web", Endpoint: server.URL, Label: "one",
		Key: webprov.Credential{DistinctID: "d", Venice: []webprov.Cookie{{Name: "cf_clearance", Value: "c"}}}.Encode(),
	}}); err != nil {
		t.Fatal(err)
	}
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	web := webprov.New(5 * time.Second).WithHTTP(http.DefaultClient).WithBase(server.URL)
	handler := gateway.NewWithWeb(routes, store.ClientKey(), cfg.MinPrivacy, web)
	body := `{"model":"strict","messages":[` +
		`{"role":"user","content":"first"},` +
		`{"role":"assistant","content":"reply"},` +
		`{"role":"user","content":"second"},` +
		`{"role":"user","content":"and more"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

func TestStalledStreamMapsToUpstreamTimeout(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		fmt.Fprint(w, "{\"kind\":\"content\",\"content\":\"partial\"}\n")
		w.(http.Flusher).Flush()
		time.Sleep(1600 * time.Millisecond)
	})
	store, cfg := setup(t, server.URL)
	cfg.Providers = map[string]config.Provider{"web": {BaseURL: server.URL, Transport: config.TransportVeniceWeb}}
	cfg.Models = []config.Route{{
		ID: "slow", Provider: "web", UpstreamModel: "m", Privacy: config.Consumer, InactivityMS: 1000,
	}}
	if _, err := store.Import(t.Context(), []vault.Entry{{
		Provider: "web", Endpoint: server.URL, Label: "one",
		Key: webprov.Credential{DistinctID: "d", Venice: []webprov.Cookie{{Name: "cf_clearance", Value: "c"}}}.Encode(),
	}}); err != nil {
		t.Fatal(err)
	}
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	web := webprov.New(5 * time.Second).WithHTTP(http.DefaultClient).WithBase(server.URL)
	handler := gateway.NewWithWeb(routes, store.ClientKey(), cfg.MinPrivacy, web)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"slow","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusGatewayTimeout || !strings.Contains(result.Body.String(), "upstream_timeout") {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

func TestSamplingAndPenaltiesFollowCapabilities(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		if _, ok := body["temperature"]; ok {
			t.Errorf("sampling sent to a no-sampling model: %v", body["temperature"])
		}
		if body["frequencyPenalty"] != 0.5 || body["presencePenalty"] != 0.25 {
			t.Errorf("penalties %v %v", body["frequencyPenalty"], body["presencePenalty"])
		}
		fmt.Fprint(w, "{\"kind\":\"content\",\"content\":\"ok\"}\n")
	})
	store, cfg := setup(t, server.URL)
	cfg.Providers = map[string]config.Provider{"web": {BaseURL: server.URL, Transport: config.TransportVeniceWeb}}
	cfg.Models = []config.Route{{
		ID: "plain", Provider: "web", UpstreamModel: "m", Privacy: config.Consumer,
		NoSampling: true, Frequency: true, Presence: true,
	}}
	if _, err := store.Import(t.Context(), []vault.Entry{{
		Provider: "web", Endpoint: server.URL, Label: "one",
		Key: webprov.Credential{DistinctID: "d", Venice: []webprov.Cookie{{Name: "cf_clearance", Value: "c"}}}.Encode(),
	}}); err != nil {
		t.Fatal(err)
	}
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	web := webprov.New(5 * time.Second).WithHTTP(http.DefaultClient).WithBase(server.URL)
	handler := gateway.NewWithWeb(routes, store.ClientKey(), cfg.MinPrivacy, web)
	body := `{"model":"plain","temperature":0.9,"frequency_penalty":0.5,"presence_penalty":0.25,` +
		`"messages":[{"role":"user","content":"hi"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

func TestUnknownModelMentionsTheReplacement(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"Model not found"}`)
	})
	store, cfg := setup(t, server.URL)
	cfg.Providers = map[string]config.Provider{"web": {BaseURL: server.URL, Transport: config.TransportVeniceWeb}}
	cfg.Models = []config.Route{{
		ID: "old", Provider: "web", UpstreamModel: "m", Privacy: config.Consumer, Replacement: "new-model",
	}}
	if _, err := store.Import(t.Context(), []vault.Entry{{
		Provider: "web", Endpoint: server.URL, Label: "one",
		Key: webprov.Credential{DistinctID: "d", Venice: []webprov.Cookie{{Name: "cf_clearance", Value: "c"}}}.Encode(),
	}}); err != nil {
		t.Fatal(err)
	}
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	web := webprov.New(5 * time.Second).WithHTTP(http.DefaultClient).WithBase(server.URL)
	handler := gateway.NewWithWeb(routes, store.ClientKey(), cfg.MinPrivacy, web)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"old","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusNotFound || !strings.Contains(result.Body.String(), "new-model") {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

func roundTripGateway(t *testing.T, roundTrip bool, reply func(http.ResponseWriter, map[string]any, int)) (http.Handler, string) {
	t.Helper()
	calls := 0
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		calls++
		reply(w, body, calls)
	})
	store, cfg := setup(t, server.URL)
	cfg.Providers = map[string]config.Provider{"web": {BaseURL: server.URL, Transport: config.TransportVeniceWeb}}
	thinking := true
	cfg.Models = []config.Route{{
		ID: "thinker", Provider: "web", UpstreamModel: "m", Privacy: config.Consumer,
		Reasoning: &thinking, ReasoningRoundTrip: roundTrip,
	}}
	if _, err := store.Import(t.Context(), []vault.Entry{{
		Provider: "web", Endpoint: server.URL, Label: "one",
		Key: webprov.Credential{DistinctID: "d", Venice: []webprov.Cookie{{Name: "cf_clearance", Value: "c"}}}.Encode(),
	}}); err != nil {
		t.Fatal(err)
	}
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	web := webprov.New(5 * time.Second).WithHTTP(http.DefaultClient).WithBase(server.URL)
	return gateway.NewWithWeb(routes, store.ClientKey(), cfg.MinPrivacy, web), store.ClientKey()
}

func TestReasoningIsReplayedWithFollowUps(t *testing.T) {
	var replayed any = "missing"
	handler, key := roundTripGateway(t, true, func(w http.ResponseWriter, body map[string]any, call int) {
		if call == 2 {
			messages := body["prompt"].([]any)
			replayed = messages[1].(map[string]any)["reasoning"]
		}
		if call == 1 {
			fmt.Fprint(w, "{\"kind\":\"reasoning\",\"content\":\"because\"}\n")
			fmt.Fprint(w, "{\"kind\":\"content\",\"content\":\"answer\"}\n")
			return
		}
		fmt.Fprint(w, "{\"kind\":\"content\",\"content\":\"again\"}\n")
	})
	first := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"thinker","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	first.Header.Set("Authorization", "Bearer "+key)
	firstResult := httptest.NewRecorder()
	handler.ServeHTTP(firstResult, first)
	if !strings.Contains(firstResult.Body.String(), `"reasoning_content":"because"`) {
		t.Fatalf("reasoning not emitted: %s", firstResult.Body)
	}
	second := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"thinker","messages":[`+
			`{"role":"user","content":"hi"},{"role":"assistant","content":"answer"},`+
			`{"role":"user","content":"next"}]}`))
	second.Header.Set("Authorization", "Bearer "+key)
	secondResult := httptest.NewRecorder()
	handler.ServeHTTP(secondResult, second)
	if replayed != "because" {
		t.Fatalf("reasoning not replayed: %v (%s)", replayed, secondResult.Body)
	}
}

func TestReasoningIsDroppedWithoutTheCapability(t *testing.T) {
	var replayed any = "missing"
	handler, key := roundTripGateway(t, false, func(w http.ResponseWriter, body map[string]any, call int) {
		if call == 2 {
			messages := body["prompt"].([]any)
			replayed = messages[1].(map[string]any)["reasoning"]
		}
		if call == 1 {
			fmt.Fprint(w, "{\"kind\":\"reasoning\",\"content\":\"because\"}\n")
		}
		fmt.Fprint(w, "{\"kind\":\"content\",\"content\":\"answer\"}\n")
	})
	for call := 0; call < 2; call++ {
		body := `{"model":"thinker","messages":[{"role":"user","content":"hi"}]}`
		if call == 1 {
			body = `{"model":"thinker","messages":[{"role":"user","content":"hi"},` +
				`{"role":"assistant","content":"answer"},{"role":"user","content":"next"}]}`
		}
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+key)
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
	if replayed != nil {
		t.Fatalf("reasoning replayed without the capability: %v", replayed)
	}
}

func TestVideoLimitRejectsBeforeUpload(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		t.Error("sent despite the video limit")
	})
	store, cfg := setup(t, server.URL)
	cfg.Providers = map[string]config.Provider{"web": {BaseURL: server.URL, Transport: config.TransportVeniceWeb}}
	cfg.Models = []config.Route{{
		ID: "video", Provider: "web", UpstreamModel: "m", Privacy: config.Consumer, MaxVideos: 1,
	}}
	if _, err := store.Import(t.Context(), []vault.Entry{{
		Provider: "web", Endpoint: server.URL, Label: "one",
		Key: webprov.Credential{DistinctID: "d", Venice: []webprov.Cookie{{Name: "cf_clearance", Value: "c"}}}.Encode(),
	}}); err != nil {
		t.Fatal(err)
	}
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	web := webprov.New(5 * time.Second).WithHTTP(http.DefaultClient).WithBase(server.URL)
	handler := gateway.NewWithWeb(routes, store.ClientKey(), cfg.MinPrivacy, web)
	clip := `"image_url":{"url":"data:video/mp4;base64,AAAA"}`
	body := `{"model":"video","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"compare"},{"type":"image_url",` + clip + `},{"type":"image_url",` + clip + `}]}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusBadRequest || !strings.Contains(result.Body.String(), "video") {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

func TestNonStreamRepliesCarryReasoning(t *testing.T) {
	makeHandler := func(protocol config.Protocol) (http.Handler, string) {
		server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
			fmt.Fprint(w, "{\"kind\":\"reasoning\",\"content\":\"because\"}\n")
			fmt.Fprint(w, "{\"kind\":\"content\",\"content\":\"answer\"}\n")
		})
		store, cfg := setup(t, server.URL)
		cfg.Providers = map[string]config.Provider{"web": {BaseURL: server.URL, Transport: config.TransportVeniceWeb}}
		thinking := true
		cfg.Models = []config.Route{{
			ID: "thinker", Provider: "web", UpstreamModel: "m", Privacy: config.Consumer,
			Reasoning: &thinking, Protocol: protocol,
		}}
		if _, err := store.Import(t.Context(), []vault.Entry{{
			Provider: "web", Endpoint: server.URL, Label: "one",
			Key: webprov.Credential{DistinctID: "d", Venice: []webprov.Cookie{{Name: "cf_clearance", Value: "c"}}}.Encode(),
		}}); err != nil {
			t.Fatal(err)
		}
		routes, err := router.New(t.Context(), cfg, store)
		if err != nil {
			t.Fatal(err)
		}
		web := webprov.New(5 * time.Second).WithHTTP(http.DefaultClient).WithBase(server.URL)
		return gateway.NewWithWeb(routes, store.ClientKey(), cfg.MinPrivacy, web), store.ClientKey()
	}

	handler, key := makeHandler(config.Chat)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"thinker","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+key)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if !strings.Contains(result.Body.String(), `"reasoning_content":"because"`) {
		t.Fatalf("chat reply lost reasoning: %s", result.Body)
	}

	handler, key = makeHandler(config.Messages)
	request = httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"thinker","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("x-api-key", key)
	result = httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if !strings.Contains(result.Body.String(), `"type":"thinking"`) {
		t.Fatalf("messages reply lost the thinking block: %s", result.Body)
	}
}

func TestMinPFollowsTheCapability(t *testing.T) {
	run := func(supported bool) map[string]any {
		var seen map[string]any
		server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
			seen = body
			fmt.Fprint(w, "{\"kind\":\"content\",\"content\":\"ok\"}\n")
		})
		store, cfg := setup(t, server.URL)
		cfg.Providers = map[string]config.Provider{"web": {BaseURL: server.URL, Transport: config.TransportVeniceWeb}}
		cfg.Models = []config.Route{{
			ID: "sampler", Provider: "web", UpstreamModel: "m", Privacy: config.Consumer, MinP: supported,
		}}
		if _, err := store.Import(t.Context(), []vault.Entry{{
			Provider: "web", Endpoint: server.URL, Label: "one",
			Key: webprov.Credential{DistinctID: "d", Venice: []webprov.Cookie{{Name: "cf_clearance", Value: "c"}}}.Encode(),
		}}); err != nil {
			t.Fatal(err)
		}
		routes, err := router.New(t.Context(), cfg, store)
		if err != nil {
			t.Fatal(err)
		}
		web := webprov.New(5 * time.Second).WithHTTP(http.DefaultClient).WithBase(server.URL)
		handler := gateway.NewWithWeb(routes, store.ClientKey(), cfg.MinPrivacy, web)
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"sampler","min_p":0.2,"messages":[{"role":"user","content":"hi"}]}`))
		request.Header.Set("Authorization", "Bearer "+store.ClientKey())
		handler.ServeHTTP(httptest.NewRecorder(), request)
		return seen
	}
	if body := run(true); body["minP"] != 0.2 {
		t.Fatalf("minP forwarded wrong: %v", body["minP"])
	}
	if body := run(false); body["minP"] != nil {
		t.Fatalf("minP forwarded without support: %v", body["minP"])
	}
}

func TestUnpinnedPoolRetriesAnotherLabel(t *testing.T) {
	var calls atomic.Int32
	var served atomic.Value
	// The label travels as the clearance cookie, so the fixture knows which one
	// was asked and the header can be checked against it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/user/client_attestation/mint":
			fmt.Fprint(w, `{"tokens":[{"token":"attestation"}]}`)
		case "/inference/chat":
			label := ""
			if cookie, err := r.Cookie("cf_clearance"); err == nil {
				label = cookie.Value
			}
			if calls.Add(1) == 1 {
				// The first label is rate limited, a problem of the label.
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, `{"error":"rate limited"}`)
				return
			}
			served.Store(label)
			writeFrames(w, "recovered")
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	store, _ := setup(t, server.URL)
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{
		"venice-web-fixture": {BaseURL: server.URL, Transport: config.TransportVeniceWeb},
	}
	cfg.Models = []config.Route{{
		ID: webModel, Provider: "venice-web-fixture",
		UpstreamModel: "venice-uncensored-1-2", Privacy: config.ZDR,
	}}
	for _, label := range []string{"one", "two"} {
		cred := webprov.Credential{DistinctID: "device", Venice: []webprov.Cookie{{Name: "cf_clearance", Value: label}}}
		if _, err := store.Import(t.Context(), []vault.Entry{{
			Provider: "venice-web-fixture", Endpoint: server.URL, Label: label, Key: cred.Encode(),
		}}); err != nil {
			t.Fatal(err)
		}
	}
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	web := webprov.New(5 * time.Second).WithHTTP(http.DefaultClient).WithBase(server.URL)
	handler := gateway.NewWithWeb(routes, store.ClientKey(), cfg.MinPrivacy, web)

	result := complete(handler, store.ClientKey(), "", `{"model":"`+webModel+`","messages":[{"role":"user","content":"hi"}]}`)
	if result.Code != http.StatusOK || calls.Load() < 2 {
		t.Fatalf("no retry on the other label: %d calls=%d %s", result.Code, calls.Load(), result.Body)
	}
	if !strings.Contains(result.Body.String(), "recovered") {
		t.Fatalf("second label answer missing: %s", result.Body)
	}
	// The header must name the label that actually served the call.
	if got, want := result.Header().Get("X-ALLAIIS-Credential"), served.Load(); got == "" || got != want {
		t.Fatalf("header names %q, the answer came from %q", got, want)
	}
}

// An empty balance is a fact about one model; a free model on the same session
// must keep answering instead of waiting out the parking.
func TestBalanceCooldownNamesOneModel(t *testing.T) {
	var calls atomic.Int32
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		calls.Add(1)
		if body["modelId"] == "paid-1" {
			w.WriteHeader(http.StatusPaymentRequired)
			fmt.Fprint(w, `{"error":"insufficientBalance"}`)
			return
		}
		writeFrames(w, "free answer")
	})
	store, _ := setup(t, server.URL)
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{
		"venice-web-fixture": {BaseURL: server.URL, Transport: config.TransportVeniceWeb},
	}
	label := "one"
	cred := webprov.Credential{DistinctID: "device", Venice: []webprov.Cookie{{Name: "cf_clearance", Value: "one"}}}
	if _, err := store.Import(t.Context(), []vault.Entry{{
		Provider: "venice-web-fixture", Endpoint: server.URL, Label: label, Key: cred.Encode(),
	}}); err != nil {
		t.Fatal(err)
	}
	cfg.Models = []config.Route{
		{ID: "paid", Provider: "venice-web-fixture", UpstreamModel: "paid-1", CredentialLabel: label, Privacy: config.ZDR},
		{ID: "free", Provider: "venice-web-fixture", UpstreamModel: "free-1", CredentialLabel: label, Privacy: config.ZDR},
	}
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	web := webprov.New(5 * time.Second).WithHTTP(http.DefaultClient).WithBase(server.URL)
	handler := gateway.NewWithWeb(routes, store.ClientKey(), cfg.MinPrivacy, web)

	prompt := func(model string) string {
		return `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
	}
	if result := complete(handler, store.ClientKey(), "", prompt("paid")); result.Code != http.StatusPaymentRequired {
		t.Fatalf("paid model answered %d", result.Code)
	}
	before := calls.Load()
	if result := complete(handler, store.ClientKey(), "", prompt("free")); result.Code != http.StatusOK {
		t.Fatalf("free model blocked by the balance mark: %d %s", result.Code, result.Body)
	}
	// The free call proves the session still works; the paid one is parked, so
	// it does not reach the provider again.
	afterFree := calls.Load()
	if result := complete(handler, store.ClientKey(), "", prompt("paid")); result.Code != http.StatusTooManyRequests {
		t.Fatalf("parked model answered %d", result.Code)
	}
	if calls.Load() != afterFree || afterFree != before+1 {
		t.Fatalf("unexpected upstream calls: before=%d free=%d now=%d", before, afterFree, calls.Load())
	}
}
