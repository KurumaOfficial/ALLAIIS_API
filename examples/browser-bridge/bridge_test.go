// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	fixtureAPIKey    = "api-fixture-key"
	fixtureWorkerKey = "local-worker-fixture"
)

func TestBrowserWorkerBoundaryAndStreaming(t *testing.T) {
	server := startTestBridge(t, 3*time.Second)
	rec := call(t, server, "/worker/claim", fixtureWorkerKey, "http://example.test", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unexpected response to cross-origin worker: %d", rec.Code)
	}
	if rec := call(t, server, "/worker/claim", fixtureAPIKey, server.URL, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("client API key accessed the worker: %d", rec.Code)
	}

	result := asyncCompletion(t, server, t.Context(), true)
	claimed := waitForClaim(t, server)
	if claimed.Text != "test message" {
		t.Fatalf("worker got %q", claimed.Text)
	}
	if rec := call(t, server, "/worker/claim", fixtureWorkerKey, server.URL, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("request was claimed twice: %d", rec.Code)
	}
	if rec := call(t, server, "/v1/chat/completions", fixtureAPIKey, "", completionRequest(false)); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("bridge accepted a second in-flight request: %d", rec.Code)
	}
	if rec := call(t, server, "/worker/reply", fixtureWorkerKey, server.URL,
		map[string]string{"id": "different-id", "text": "wrong result"}); rec.Code != http.StatusConflict {
		t.Fatalf("unmatched reply was accepted: %d", rec.Code)
	}
	if rec := call(t, server, "/worker/reply", fixtureWorkerKey, server.URL,
		map[string]string{"id": claimed.ID, "text": "Browser echo: " + claimed.Text}); rec.Code != http.StatusNoContent {
		t.Fatalf("worker reply failed: %d", rec.Code)
	}

	response := <-result
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" ||
		!strings.Contains(response.Body.String(), "Browser echo: test message") ||
		!strings.Contains(response.Body.String(), "data: [DONE]") {
		t.Fatalf("unexpected completion: %d %s", response.Code, response.Body)
	}
	if rec := call(t, server, "/worker/reply", fixtureWorkerKey, server.URL,
		map[string]string{"id": claimed.ID, "text": "replayed"}); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate reply was accepted: %d", rec.Code)
	}
}

func TestCanceledRequestReleasesWorker(t *testing.T) {
	server := startTestBridge(t, 3*time.Second)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := asyncCompletion(t, server, ctx, false)
	old := waitForClaim(t, server)
	cancel()
	<-result

	fresh := asyncCompletion(t, server, t.Context(), false)
	newJob := waitForClaim(t, server)
	if old.ID == newJob.ID {
		t.Fatal("new request reused the old ID")
	}
	if rec := call(t, server, "/worker/reply", fixtureWorkerKey, server.URL,
		map[string]string{"id": old.ID, "text": "late reply"}); rec.Code != http.StatusConflict {
		t.Fatalf("late reply was accepted: %d", rec.Code)
	}
	if rec := call(t, server, "/worker/reply", fixtureWorkerKey, server.URL,
		map[string]string{"id": newJob.ID, "text": "new reply"}); rec.Code != http.StatusNoContent {
		t.Fatalf("new request did not get a worker: %d", rec.Code)
	}
	response := <-fresh
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "new reply") {
		t.Fatalf("new completion failed: %d %s", response.Code, response.Body)
	}
}

func TestAbsentWorkerTimesOut(t *testing.T) {
	server := startTestBridge(t, 30*time.Millisecond)
	result := call(t, server, "/v1/chat/completions", fixtureAPIKey, "", completionRequest(false))
	if result.Code != http.StatusGatewayTimeout {
		t.Fatalf("missing worker returned %d", result.Code)
	}
}

func startTestBridge(t *testing.T, timeout time.Duration) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(nil)
	server.Config.Handler = newBridge(server.Listener.Addr().String(), fixtureAPIKey, fixtureWorkerKey, timeout, nil)
	server.Start()
	t.Cleanup(server.Close)
	return server
}

func call(t *testing.T, server *httptest.Server, path, key, origin string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	server.Config.Handler.ServeHTTP(rec, request(t, t.Context(), server, path, key, origin, payload))
	return rec
}

func request(t *testing.T, ctx context.Context, server *httptest.Server, path, key, origin string, payload any) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, server.URL+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return req
}

func asyncCompletion(t *testing.T, server *httptest.Server, ctx context.Context, stream bool) <-chan *httptest.ResponseRecorder {
	t.Helper()
	req := request(t, ctx, server, "/v1/chat/completions", fixtureAPIKey, "", completionRequest(stream))
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		server.Config.Handler.ServeHTTP(rec, req)
		result <- rec
	}()
	return result
}

func completionRequest(stream bool) map[string]any {
	return map[string]any{
		"model": modelID, "stream": stream,
		"messages": []map[string]string{{"role": "user", "content": "test message"}},
	}
}

func waitForClaim(t *testing.T, server *httptest.Server) job {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		res := call(t, server, "/worker/claim", fixtureWorkerKey, server.URL, nil)
		if res.Code == http.StatusOK {
			var claimed job
			if err := json.Unmarshal(res.Body.Bytes(), &claimed); err != nil {
				t.Fatal("worker received an invalid job")
			}
			return claimed
		}
		if res.Code != http.StatusNoContent {
			t.Fatalf("could not claim job: %d", res.Code)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("worker did not receive a request")
	return job{}
}
