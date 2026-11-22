// SPDX-License-Identifier: GPL-3.0-only

package gateway_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/gateway"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/router"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

func responsesGateway(t *testing.T, upstream string) (http.Handler, *vault.Store) {
	t.Helper()
	store, _ := setup(t, upstream)
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{
		"venice-web-fixture": {BaseURL: upstream, Transport: config.TransportVeniceWeb},
	}
	cfg.Models = []config.Route{{
		ID: "gpt-venice-web-fixture", Provider: "venice-web-fixture",
		UpstreamModel: "openai-gpt-62-astra-pro", Privacy: config.ZDR,
		Protocol: config.Responses, CredentialLabel: "personal",
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
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

func responsesCall(handler http.Handler, key, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	return result
}

func TestResponsesProtocolJSON(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		messages := body["prompt"].([]any)
		if messages[0].(map[string]any)["content"] != "be brief" {
			t.Errorf("instructions missing: %v", messages[0])
		}
		writeFrames(w, "answer")
	})
	handler, store := responsesGateway(t, server.URL)
	result := responsesCall(handler, store.ClientKey(), `{"model":"gpt-venice-web-fixture","instructions":"be brief","input":"hi"}`)
	var reply struct {
		Object string `json:"object"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &reply); err != nil {
		t.Fatalf("%v: %s", err, result.Body)
	}
	if result.Code != http.StatusOK || reply.Object != "response" || reply.Output[0].Content[0].Text != "answer" {
		t.Fatalf("unexpected reply: %d %s", result.Code, result.Body)
	}
}

func TestResponsesProtocolStreamAndTool(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		writeFrames(w, toolBlock("a.txt"))
	})
	handler, store := responsesGateway(t, server.URL)
	body := `{"model":"gpt-venice-web-fixture","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"read"}]}],` +
		`"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]}`
	result := responsesCall(handler, store.ClientKey(), body)
	text := result.Body.String()
	for _, want := range []string{"event: response.created", `"type":"function_call"`, "response.function_call_arguments.delta", "event: response.completed"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
}

func TestResponsesProtocolHistory(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		messages := body["prompt"].([]any)
		roles := make([]string, 0, len(messages))
		for _, raw := range messages {
			roles = append(roles, raw.(map[string]any)["role"].(string))
		}
		if strings.Join(roles, ",") != "system,user,assistant,user" {
			t.Fatalf("history shape %v", roles)
		}
		writeFrames(w, "done")
	})
	handler, store := responsesGateway(t, server.URL)
	body := `{"model":"gpt-venice-web-fixture","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"read go.mod"}]},` +
		`{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"go.mod\"}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"go 1.26"}],` +
		`"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]}`
	result := responsesCall(handler, store.ClientKey(), body)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "done") {
		t.Fatalf("got %d %s", result.Code, result.Body.String())
	}
}
