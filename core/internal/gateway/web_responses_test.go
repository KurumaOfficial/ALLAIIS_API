// SPDX-License-Identifier: GPL-3.0-only

package gateway_test

import (
	"encoding/base64"
	"encoding/json"
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

func responsesGateway(t *testing.T, upstream string) (http.Handler, *vault.Store) {
	handler, _, store := responsesGatewayRebuild(t, upstream)
	return handler, store
}

// responsesGatewayRebuild also returns a builder for a second gateway instance
// over the same vault, which is how a restart is simulated.
func responsesGatewayRebuild(t *testing.T, upstream string) (http.Handler, func() http.Handler, *vault.Store) {
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
	web := webprov.New(5 * time.Second).WithHTTP(http.DefaultClient).WithBase(upstream)
	build := func() http.Handler {
		routes, err := router.New(t.Context(), cfg, store)
		if err != nil {
			t.Fatal(err)
		}
		return gateway.NewWithWeb(routes, store.ClientKey(), cfg.MinPrivacy, web)
	}
	return build(), build, store
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

// TestResponsesToolResultMatchesMessages pins the turn the model sees after a
// tool result: the Responses shape sent it without the blank line the Messages
// shape puts in front, and the model answered by calling the same tool again.
func TestResponsesToolResultMatchesMessages(t *testing.T) {
	var fromMessages, fromResponses map[string]any
	messagesServer := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		fromMessages = body
		writeFrames(w, "done")
	})
	handler, store := messagesGateway(t, messagesServer.URL)
	anthropicCall(handler, store.ClientKey(), `{"model":"claude-venice-web-fixture","max_tokens":64,"messages":[`+
		`{"role":"user","content":"read go.mod"},`+
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"go.mod"}}]},`+
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"go 1.26"}]}],`+
		`"tools":[{"name":"read_file","input_schema":{"type":"object"}}]}`)

	responsesServer := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		fromResponses = body
		writeFrames(w, "done")
	})
	handler, store = responsesGateway(t, responsesServer.URL)
	responsesCall(handler, store.ClientKey(), `{"model":"gpt-venice-web-fixture","max_output_tokens":64,"input":[`+
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"read go.mod"}]},`+
		`{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"go.mod\"}"},`+
		`{"type":"function_call_output","call_id":"call_1","output":"go 1.26"}],`+
		`"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]}`)

	if fromMessages == nil || fromResponses == nil {
		t.Fatal("one of the upstreams was never called")
	}
	left, _ := json.Marshal(fromMessages["prompt"])
	right, _ := json.Marshal(fromResponses["prompt"])
	if string(left) != string(right) {
		t.Fatalf("prompt differs\nmessages: %s\nresponses: %s", left, right)
	}
}

// TestResponsesVisionSendsTheImage pins the flat image_url that the Responses
// shape uses; it used to be decoded as an object, which dropped the whole part
// list and sent the model JSON text instead of a picture.
func TestResponsesVisionSendsTheImage(t *testing.T) {
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
	handler, store := responsesGateway(t, server.URL)
	payload := `{"model":"gpt-venice-web-fixture","input":[{"role":"user","content":[` +
		`{"type":"input_text","text":"colour?"},` +
		`{"type":"input_image","image_url":"data:image/png;base64,` + png + `"}]}]}`
	result := responsesCall(handler, store.ClientKey(), payload)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "Red") {
		t.Fatalf("got %d %s", result.Code, result.Body.String())
	}
}

// TestResponsesDeveloperItemIsASystemTurn pins the role split: a developer item
// used to arrive as a user message, so its rules read like something the user
// had just said.
func TestResponsesDeveloperItemIsASystemTurn(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		messages := body["prompt"].([]any)
		if first := messages[0].(map[string]any); first["role"] != "system" ||
			!strings.Contains(first["content"].(string), "answer in French") {
			t.Errorf("developer item not in the system turn: %v", messages[0])
		}
		if len(messages) != 2 || messages[1].(map[string]any)["role"] != "user" {
			t.Errorf("history shape %v", messages)
		}
		writeFrames(w, "done")
	})
	handler, store := responsesGateway(t, server.URL)
	result := responsesCall(handler, store.ClientKey(), `{"model":"gpt-venice-web-fixture","input":[`+
		`{"type":"message","role":"developer","content":[{"type":"input_text","text":"answer in French"}]},`+
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	if result.Code != http.StatusOK {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

func TestResponsesSchemaGoesIntoTheSystemTurn(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		system := body["prompt"].([]any)[0].(map[string]any)
		if system["role"] != "system" {
			t.Fatalf("first turn is %v", system["role"])
		}
		text := system["content"].(string)
		if !strings.Contains(text, "invoice") || !strings.Contains(text, `"total"`) ||
			!strings.Contains(text, "JSON only") {
			t.Errorf("schema not spelled out: %q", text)
		}
		writeFrames(w, `{"total": 12}`)
	})
	handler, store := responsesGateway(t, server.URL)
	result := responsesCall(handler, store.ClientKey(), `{"model":"gpt-venice-web-fixture","input":"invoice please",`+
		`"text":{"format":{"type":"json_schema","name":"invoice","schema":{"type":"object","properties":{"total":{"type":"number"}}}}}}`)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `total\": 12`) {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
	if !strings.Contains(result.Header().Get("X-ALLAIIS-Emulated-Features"), "json_mode") {
		t.Errorf("json mode not declared: %q", result.Header().Get("X-ALLAIIS-Emulated-Features"))
	}
}

func TestResponsesJSONObjectNeedsNoSchema(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		text := body["prompt"].([]any)[0].(map[string]any)["content"].(string)
		if !strings.Contains(text, "JSON only") || strings.Contains(text, "JSON Schema") {
			t.Errorf("instruction %q", text)
		}
		writeFrames(w, "{}")
	})
	handler, store := responsesGateway(t, server.URL)
	result := responsesCall(handler, store.ClientKey(), `{"model":"gpt-venice-web-fixture","input":"hi",`+
		`"text":{"format":{"type":"json_object"}}}`)
	if result.Code != http.StatusOK {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

func TestResponsesFollowUpByPreviousId(t *testing.T) {
	var bodies []map[string]any
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			writeFrames(w, "first answer")
			return
		}
		messages := body["prompt"].([]any)
		roles := make([]string, 0, len(messages))
		for _, raw := range messages {
			roles = append(roles, raw.(map[string]any)["role"].(string))
		}
		if strings.Join(roles, ",") != "user,assistant,user" {
			t.Fatalf("follow-up shape %v", roles)
		}
		if !strings.Contains(messages[1].(map[string]any)["content"].(string), "first answer") {
			t.Error("previous answer was not replayed")
		}
		writeFrames(w, "second answer")
	})
	handler, rebuild, store := responsesGatewayRebuild(t, server.URL)
	first := responsesCall(handler, store.ClientKey(), `{"model":"gpt-venice-web-fixture","input":"one"}`)
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("no response id: %s", first.Body)
	}
	// A new handler shares only the vault, like a restarted process.
	second := responsesCall(rebuild(), store.ClientKey(),
		`{"model":"gpt-venice-web-fixture","previous_response_id":"`+created.ID+`","input":"two"}`)
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), "second answer") {
		t.Fatalf("follow-up failed: %d %s", second.Code, second.Body)
	}
}
