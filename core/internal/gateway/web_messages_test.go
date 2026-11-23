// SPDX-License-Identifier: GPL-3.0-only

package gateway_test

import (
	"encoding/json"
	"fmt"
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

func messagesGateway(t *testing.T, upstream string) (http.Handler, *vault.Store) {
	t.Helper()
	store, _ := setup(t, upstream)
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{
		"venice-web-fixture": {BaseURL: upstream, Transport: config.TransportVeniceWeb},
	}
	cfg.Models = []config.Route{{
		ID: "claude-venice-web-fixture", Provider: "venice-web-fixture",
		UpstreamModel: "claude-opus-5-5", Privacy: config.ZDR,
		Protocol: config.Messages, CredentialLabel: "personal",
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

func anthropicCall(handler http.Handler, key, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	request.Header.Set("x-api-key", key)
	request.Header.Set("Content-Type", "application/json")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	return result
}

const messagesBody = `{"model":"claude-venice-web-fixture","max_tokens":64,"system":"be terse","messages":[{"role":"user","content":"say hi"}]}`

func TestMessagesProtocolStreams(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		messages := body["prompt"].([]any)
		if messages[0].(map[string]any)["role"] != "system" {
			t.Errorf("system turn missing: %v", messages)
		}
		writeFrames(w, "Hello")
	})
	handler, store := messagesGateway(t, server.URL)
	body := strings.Replace(messagesBody, `"messages"`, `"stream":true,"messages"`, 1)
	result := anthropicCall(handler, store.ClientKey(), body)
	text := result.Body.String()
	for _, want := range []string{
		"event: message_start", "event: content_block_start", `"type":"text_delta"`,
		`"stop_reason":"end_turn"`, "event: message_stop",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	if strings.Contains(text, "ALLAIIS_TOOL_CALL") {
		t.Fatal("sentinel leaked into the stream")
	}
}

func TestMessagesProtocolToolUse(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		writeFrames(w, toolBlock("a.txt"))
	})
	handler, store := messagesGateway(t, server.URL)
	body := `{"model":"claude-venice-web-fixture","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"read a.txt"}],` +
		`"tools":[{"name":"read_file","description":"read","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`
	result := anthropicCall(handler, store.ClientKey(), body)
	text := result.Body.String()
	if !strings.Contains(text, `"type":"tool_use"`) || !strings.Contains(text, `"partial_json"`) ||
		!strings.Contains(text, `"stop_reason":"tool_use"`) {
		t.Fatalf("unexpected stream: %s", text)
	}
}

func TestMessagesProtocolJSON(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		writeFrames(w, "plain answer")
	})
	handler, store := messagesGateway(t, server.URL)
	result := anthropicCall(handler, store.ClientKey(), messagesBody)
	var reply struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Stop string `json:"stop_reason"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &reply); err != nil {
		t.Fatalf("%v: %s", err, result.Body)
	}
	if result.Code != http.StatusOK || reply.Type != "message" || reply.Content[0].Text != "plain answer" {
		t.Fatalf("unexpected reply: %d %s", result.Code, result.Body)
	}
}

func TestMessagesForcedToolChoiceIsSpelledOut(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		system := body["prompt"].([]any)[0].(map[string]any)["content"].(string)
		if !strings.Contains(system, `"read_file"`) || !strings.Contains(system, "one call") {
			t.Errorf("choice not spelled out: %q", system)
		}
		writeFrames(w, toolBlock("a.txt"))
	})
	handler, store := messagesGateway(t, server.URL)
	result := anthropicCall(handler, store.ClientKey(), `{"model":"claude-venice-web-fixture","max_tokens":64,`+
		`"messages":[{"role":"user","content":"read a.txt"}],`+
		`"tools":[{"name":"read_file","input_schema":{"type":"object"}}],`+
		`"tool_choice":{"type":"tool","name":"read_file","disable_parallel_tool_use":true}}`)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"tool_use"`) {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

func TestMessagesToolChoiceNoneDropsTools(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		if strings.Contains(fmt.Sprint(body), "ALLAIIS_TOOL_CALL") {
			t.Errorf("tool instructions still sent: %v", body["prompt"])
		}
		writeFrames(w, "plain")
	})
	handler, store := messagesGateway(t, server.URL)
	result := anthropicCall(handler, store.ClientKey(), `{"model":"claude-venice-web-fixture","max_tokens":64,`+
		`"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"read_file","input_schema":{"type":"object"}}],`+
		`"tool_choice":"none"}`)
	if result.Code != http.StatusOK {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

func TestMessagesToolChoiceObjectNoneDropsTools(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		if strings.Contains(fmt.Sprint(body), "ALLAIIS_TOOL_CALL") {
			t.Errorf("tool instructions still sent: %v", body["prompt"])
		}
		writeFrames(w, "plain")
	})
	handler, store := messagesGateway(t, server.URL)
	result := anthropicCall(handler, store.ClientKey(), `{"model":"claude-venice-web-fixture","max_tokens":64,`+
		`"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"read_file","input_schema":{"type":"object"}}],`+
		`"tool_choice":{"type":"none"}}`)
	if result.Code != http.StatusOK {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

func TestMessagesProtocolHistory(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		messages := body["prompt"].([]any)
		roles := make([]string, 0, len(messages))
		for _, raw := range messages {
			roles = append(roles, raw.(map[string]any)["role"].(string))
		}
		if strings.Join(roles, ",") != "system,user,assistant,user" {
			t.Fatalf("history shape %v", roles)
		}
		if !strings.Contains(messages[2].(map[string]any)["content"].(string), "ALLAIIS_TOOL_CALL") {
			t.Errorf("assistant tool_use not replayed: %v", messages[2])
		}
		if !strings.Contains(messages[3].(map[string]any)["content"].(string), "go 1.26") {
			t.Errorf("tool_result text lost: %v", messages[3])
		}
		writeFrames(w, "done")
	})
	handler, store := messagesGateway(t, server.URL)
	body := `{"model":"claude-venice-web-fixture","max_tokens":64,"messages":[` +
		`{"role":"user","content":"read go.mod"},` +
		`{"role":"assistant","content":[{"type":"text","text":"reading"},{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"go.mod"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"go 1.26"}]}],` +
		`"tools":[{"name":"read_file","input_schema":{"type":"object"}}]}`
	result := anthropicCall(handler, store.ClientKey(), body)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "done") {
		t.Fatalf("got %d %s", result.Code, result.Body.String())
	}
}
