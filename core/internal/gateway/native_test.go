// SPDX-License-Identifier: GPL-3.0-only

package gateway_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
)

func TestNativeProtocols(t *testing.T) {
	const responseJSON = `{"id":"resp_fixture","model":"upstream-response","output":[{"type":"function_call"}]}`
	const messageSSE = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Cookie") != "" || r.Header.Get("OpenAI-Project") != "" {
			t.Error("forwarded unrelated client credentials or project headers")
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/v1/responses":
			if r.Header.Get("Authorization") != "Bearer fixture-secret-2" || r.Header.Get("X-Api-Key") != "" ||
				r.Header.Get("Anthropic-Beta") != "" || r.Header.Get("Accept") != "application/json" {
				t.Error("invalid Responses upstream headers")
			}
			if string(body["model"]) != `"upstream-response"` || string(body["input"]) != `"hello"` ||
				string(body["previous_response_id"]) != `"resp_previous"` ||
				string(body["tools"]) != `[{"type":"function","name":"example"}]` {
				t.Errorf("Responses request lost native fields: %+v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, responseJSON)
		case "/v1/messages":
			if r.Header.Get("Authorization") != "" || r.Header.Get("X-Api-Key") != "fixture-secret-1" ||
				r.Header.Get("Anthropic-Version") != "2023-06-01" ||
				r.Header.Get("Anthropic-Beta") != "fixture-beta" || r.Header.Get("Accept") != "text/event-stream" {
				t.Error("invalid Messages upstream headers")
			}
			if string(body["model"]) != `"upstream-message"` || string(body["max_tokens"]) != "20" ||
				string(body["system"]) != `[{"type":"text","text":"Keep it brief"}]` {
				t.Errorf("Messages request lost native fields: %+v", body)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, messageSSE)
		default:
			t.Errorf("unexpected upstream path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)
	store, cfg := setup(t, upstream.URL)
	cfg.Models = append(cfg.Models,
		config.Route{ID: "response", Provider: "fixture", UpstreamModel: "upstream-response",
			Protocol: config.Responses, Privacy: config.Consumer, CredentialLabel: "second"},
		config.Route{ID: "message", Provider: "fixture", UpstreamModel: "upstream-message",
			Protocol: config.Messages, Privacy: config.Consumer, CredentialLabel: "first"},
	)
	handler := newGateway(t, cfg, store)
	responseRequest := `{"model":"response","input":"hello","previous_response_id":"resp_previous","tools":[{"type":"function","name":"example"}]}`
	messageRequest := `{"model":"message","stream":true,"max_tokens":20,"system":[{"type":"text","text":"Keep it brief"}],"messages":[{"role":"user","content":"hello"}]}`

	for _, session := range []string{"original", "continuation"} {
		r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(responseRequest))
		r.Header.Set("Authorization", "Bearer "+store.ClientKey())
		r.Header.Set("X-Api-Key", "do-not-forward")
		r.Header.Set("OpenAI-Project", "do-not-forward")
		r.Header.Set("Anthropic-Beta", "do-not-forward")
		r.Header.Set("X-ALLAIIS-Session", session)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Body.String() != responseJSON || w.Header().Get("X-ALLAIIS-Model") != "response" {
			t.Fatalf("Responses returned %d, %s, %v", w.Code, w.Body.String(), w.Header())
		}
	}

	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(messageRequest))
	r.Header.Set("X-Api-Key", store.ClientKey())
	r.Header.Set("Cookie", "do-not-forward")
	r.Header.Set("Anthropic-Version", "2023-06-01")
	r.Header.Set("Anthropic-Beta", "fixture-beta")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != messageSSE ||
		w.Header().Get("Content-Type") != "text/event-stream" || w.Header().Get("X-ALLAIIS-Model") != "message" {
		t.Fatalf("Messages returned %d, %s, %v", w.Code, w.Body.String(), w.Header())
	}

	for _, tc := range []struct {
		name, path, body, errorType string
		headers                     http.Header
		status                      int
		code                        string
	}{
		{"Responses requires bearer", "/v1/responses", responseRequest, "invalid_api_key",
			http.Header{"X-Api-Key": {store.ClientKey()}}, http.StatusUnauthorized, ""},
		{"Messages requires the local key", "/v1/messages", messageRequest, "authentication_error",
			http.Header{"X-Api-Key": {"fixture-secret-1"}}, http.StatusUnauthorized, ""},
		{"two credential headers", "/v1/messages", messageRequest, "authentication_error",
			http.Header{"Authorization": {"Bearer " + store.ClientKey()}, "X-Api-Key": {store.ClientKey()}}, http.StatusUnauthorized, ""},
		{"repeated version", "/v1/messages", messageRequest, "invalid_request_error",
			http.Header{"X-Api-Key": {store.ClientKey()}, "Anthropic-Version": {"2023-06-01", "2026-10-21"}}, http.StatusBadRequest, ""},
		{"Responses on a chat route", "/v1/responses", strings.Replace(responseRequest, `"response"`, `"demo"`, 1),
			"model_not_found", http.Header{"Authorization": {"Bearer " + store.ClientKey()}}, http.StatusNotFound, ""},
		{"Messages privacy floor", "/v1/messages", messageRequest, "not_found_error",
			http.Header{"X-Api-Key": {store.ClientKey()}, "X-Allaiis-Min-Privacy": {"zdr"}}, http.StatusNotFound, "model_not_found"},
		{"background Responses", "/v1/responses", `{"model":"response","background":true,"input":"hello"}`,
			"background generation", http.Header{"Authorization": {"Bearer " + store.ClientKey()}}, http.StatusBadRequest, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			r.Header = tc.headers
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.errorType) {
				t.Fatalf("got %d %s, want %d with %q", w.Code, w.Body.String(), tc.status, tc.errorType)
			}
			// An Anthropic body still has to carry the stable code.
			if tc.code != "" && !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("no code in %s", w.Body)
			}
			// An Anthropic body still has to carry the stable code.
			if tc.code != "" && !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("no code in %s", w.Body)
			}
		})
	}
	if calls.Load() != 3 {
		t.Fatalf("upstream received %d requests, want 3", calls.Load())
	}
}

func TestProviderRequestTooLarge(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil || !strings.Contains(string(body), "keep the entire conversation") {
			t.Error("the request was changed before sending")
		}
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		fmt.Fprint(w, "provider-private details")
	}))
	t.Cleanup(upstream.Close)
	store, cfg := setup(t, upstream.URL)
	body := strings.Replace(payload, "hello", "keep the entire conversation", 1)
	res := complete(newGateway(t, cfg, store), store.ClientKey(), "", body)
	if res.Code != http.StatusRequestEntityTooLarge || !strings.Contains(res.Body.String(), "request_too_large") ||
		strings.Contains(res.Body.String(), "provider-private details") || calls.Load() != 1 {
		t.Fatalf("unexpected upstream 413 handling: %d %s, %d calls", res.Code, res.Body.String(), calls.Load())
	}
}
