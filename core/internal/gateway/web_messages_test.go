// SPDX-License-Identifier: GPL-3.0-only

package gateway_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

// TestMessagesNamesTheMatchedStopSequence pins the Anthropic contract: a reply
// cut by one of the caller's stop_sequences reports stop_reason stop_sequence
// and the sequence itself, not a generic end_turn with null.
func TestMessagesNamesTheMatchedStopSequence(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, _ map[string]any) {
		writeFrames(w, "one two STOP three")
	})
	handler, store := messagesGateway(t, server.URL)
	request := `{"model":"claude-venice-web-fixture","max_tokens":64,"stop_sequences":["STOP"],` +
		`"messages":[{"role":"user","content":"count"}]}`

	plain := anthropicCall(handler, store.ClientKey(), request)
	var reply struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		StopReason   string  `json:"stop_reason"`
		StopSequence *string `json:"stop_sequence"`
	}
	if err := json.Unmarshal(plain.Body.Bytes(), &reply); err != nil {
		t.Fatalf("%v in %s", err, plain.Body)
	}
	if reply.StopReason != "stop_sequence" || reply.StopSequence == nil || *reply.StopSequence != "STOP" {
		t.Errorf("stop %q %v", reply.StopReason, reply.StopSequence)
	}
	if len(reply.Content) == 0 || reply.Content[0].Text != "one two " {
		t.Errorf("content %+v", reply.Content)
	}

	streamed := anthropicCall(handler, store.ClientKey(),
		strings.Replace(request, `"messages"`, `"stream":true,"messages"`, 1))
	text := streamed.Body.String()
	if !strings.Contains(text, `"stop_reason":"stop_sequence"`) || !strings.Contains(text, `"stop_sequence":"STOP"`) {
		t.Errorf("stream did not name the sequence: %s", text)
	}
}

// TestEveryShapeNamesTheJSONMistake pins the shared decoder on the entry points
// that still answered "expected a JSON object" for any failure, including a
// truncated body.
func TestEveryShapeNamesTheJSONMistake(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, _ map[string]any) {
		writeFrames(w, "hi")
	})
	chatHandler, chatStore := webGateway(t, server.URL)
	messagesHandler, messagesStore := messagesGateway(t, server.URL)
	responsesHandler, responsesStore := responsesGateway(t, server.URL)

	for _, tc := range []struct {
		name   string
		result *httptest.ResponseRecorder
	}{
		{"chat", complete(chatHandler, chatStore.ClientKey(), "", `{"model":"x","messages":[`)},
		{"messages", anthropicCall(messagesHandler, messagesStore.ClientKey(), `{"model":"x","messages":[`)},
		{"responses", responsesCall(responsesHandler, responsesStore.ClientKey(), `{"model":"x","input":`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.result.Code != http.StatusBadRequest ||
				!strings.Contains(tc.result.Body.String(), "not valid JSON") {
				t.Fatalf("got %d %s", tc.result.Code, tc.result.Body)
			}
		})
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(`{"model":`))
	request.Header.Set("x-api-key", messagesStore.ClientKey())
	request.Header.Set("Anthropic-Version", "2023-06-01")
	result := httptest.NewRecorder()
	messagesHandler.ServeHTTP(result, request)
	if result.Code != http.StatusBadRequest || !strings.Contains(result.Body.String(), "not valid JSON") {
		t.Fatalf("count_tokens: %d %s", result.Code, result.Body)
	}
}

// TestMessagesVisionReadsABase64Block pins the Anthropic image shape: a base64
// source block used to fall through and the model answered as if no image had
// been sent.
func TestMessagesVisionReadsABase64Block(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("pngbytes"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/mint"):
			io.WriteString(w, `{"tokens":[{"token":"t"}]}`)
		case r.URL.Path == "/inference/chat/agents/venice":
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
	handler, store := messagesGateway(t, server.URL)
	body := `{"model":"claude-venice-web-fixture","max_tokens":32,"messages":[{"role":"user","content":[` +
		`{"type":"text","text":"colour?"},{"type":"image","source":{"type":"base64",` +
		`"media_type":"image/png","data":"` + png + `"}}]}]}`
	result := anthropicCall(handler, store.ClientKey(), body)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "Red") {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

// TestMessagesRefusesADocumentBlock pins honesty for the one attachment shape
// with no path to the web endpoint: it used to be ignored, so the model answered
// as if the document had been read.
func TestMessagesRefusesADocumentBlock(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, _ map[string]any) {
		t.Error("upstream was called")
		writeFrames(w, "hi")
	})
	handler, store := messagesGateway(t, server.URL)
	body := `{"model":"claude-venice-web-fixture","max_tokens":32,"messages":[{"role":"user","content":[` +
		`{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="}}]}]}`
	result := anthropicCall(handler, store.ClientKey(), body)
	if result.Code != http.StatusBadRequest ||
		!strings.Contains(result.Body.String(), "attachment could not be read") {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

// TestMessagesKeepsTheAnthropicTemperatureScale pins the protocol's own range:
// Anthropic stops at one even though Venice accepts two.
func TestMessagesKeepsTheAnthropicTemperatureScale(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, _ map[string]any) {
		t.Error("upstream was called")
		writeFrames(w, "hi")
	})
	handler, store := messagesGateway(t, server.URL)
	result := anthropicCall(handler, store.ClientKey(), `{"model":"claude-venice-web-fixture","max_tokens":8,`+
		`"temperature":1.5,"messages":[{"role":"user","content":"hi"}]}`)
	if result.Code != http.StatusBadRequest ||
		!strings.Contains(result.Body.String(), "between 0 and 1") {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

// TestMessagesRefusesAForcedToolThatIsNotOffered pins the same check on the
// Anthropic spelling: the instruction used to name a tool the model never got.
func TestMessagesRefusesAForcedToolThatIsNotOffered(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, _ map[string]any) {
		t.Error("upstream was called")
		writeFrames(w, "hi")
	})
	handler, store := messagesGateway(t, server.URL)
	result := anthropicCall(handler, store.ClientKey(), `{"model":"claude-venice-web-fixture","max_tokens":8,`+
		`"tools":[{"name":"read_file","input_schema":{"type":"object"}}],`+
		`"tool_choice":{"type":"tool","name":"missing"},"messages":[{"role":"user","content":"hi"}]}`)
	if result.Code != http.StatusBadRequest ||
		!strings.Contains(result.Body.String(), "not in the offered tools") {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

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

// TestMessagesCountTokensAnswersWithoutUpstream pins the endpoint Anthropic
// clients call before a turn: it must not spend a message.
func TestMessagesCountTokensAnswersWithoutUpstream(t *testing.T) {
	called := false
	server := veniceFixture(t, func(w http.ResponseWriter, _ map[string]any) {
		called = true
	})
	handler, store := messagesGateway(t, server.URL)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens",
		strings.NewReader(`{"model":"claude-venice-web-fixture","messages":[{"role":"user","content":"read go.mod"}],`+
			`"system":"be terse","tools":[{"name":"read_file","input_schema":{"type":"object"}}]}`))
	request.Header.Set("x-api-key", store.ClientKey())
	request.Header.Set("anthropic-version", "2023-06-01")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK {
		t.Fatalf("count: %d %s", result.Code, result.Body)
	}
	var reply struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &reply); err != nil || reply.InputTokens <= 0 {
		t.Fatalf("reply %s", result.Body)
	}
	if called {
		t.Fatal("count_tokens called upstream")
	}
}

func TestMessagesCountTokensRefusesAnUnknownModel(t *testing.T) {
	server := veniceFixture(t, func(http.ResponseWriter, map[string]any) {})
	handler, store := messagesGateway(t, server.URL)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens",
		strings.NewReader(`{"model":"nope","messages":[]}`))
	request.Header.Set("x-api-key", store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusNotFound || !strings.Contains(result.Body.String(), "model_not_found") {
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
