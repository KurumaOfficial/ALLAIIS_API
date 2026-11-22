// SPDX-License-Identifier: GPL-3.0-only

package gateway_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const toolPayload = `{"model":"` + webModel + `","tools":[{"type":"function","function":{` +
	`"name":"read_file","description":"read","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}],` +
	`"messages":[{"role":"system","content":"be terse"},{"role":"user","content":"read a.txt"}]}`

func toolBlock(path string) string {
	return `<<<ALLAIIS_TOOL_CALL>>>{"calls":[{"name":"read_file","arguments":{"path":"` + path + `"}}]}<<<END_TOOL_CALL>>>`
}

func TestWebToolCallJSON(t *testing.T) {
	server := venueWithToolBodies(t, toolBlock("a.txt"))
	handler, store := webGateway(t, server.URL)
	result := complete(handler, store.ClientKey(), "", toolPayload)
	var reply struct {
		Choices []struct {
			Finish  string `json:"finish_reason"`
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &reply); err != nil {
		t.Fatalf("%v: %s", err, result.Body)
	}
	if len(reply.Choices) == 0 {
		t.Fatalf("no choices: %d %s", result.Code, result.Body)
	}
	call := reply.Choices[0].Message.ToolCalls
	if result.Code != http.StatusOK || reply.Choices[0].Finish != "tool_calls" ||
		reply.Choices[0].Message.Content != "" || len(call) != 1 || call[0].Function.Name != "read_file" ||
		!strings.Contains(call[0].Function.Arguments, "a.txt") {
		t.Fatalf("unexpected reply: %d %s", result.Code, result.Body)
	}
	if !strings.Contains(result.Header().Get("X-ALLAIIS-Emulated-Features"), "tools") {
		t.Fatal("emulation was not declared")
	}
}

func TestWebToolCallStream(t *testing.T) {
	server := venueWithToolBodies(t, "Reading "+toolBlock("b.txt"))
	handler, store := webGateway(t, server.URL)
	body := strings.Replace(toolPayload, `"messages"`, `"stream":true,"messages"`, 1)
	result := complete(handler, store.ClientKey(), "", body)
	text := result.Body.String()
	if result.Code != http.StatusOK || !strings.Contains(text, `"tool_calls"`) ||
		!strings.Contains(text, `"name":"read_file"`) || !strings.Contains(text, `"finish_reason":"tool_calls"`) ||
		!strings.Contains(text, `"content":"Reading`) || strings.Contains(text, "ALLAIIS_TOOL_CALL") {
		t.Fatalf("unexpected stream: %d %s", result.Code, text)
	}
}

func TestWebToolHistoryReplaysAssistantCalls(t *testing.T) {
	var checked atomic.Bool
	server := venueWithToolBodiesFunc(t, func(w http.ResponseWriter, body map[string]any) {
		messages := body["prompt"].([]any)
		roles := make([]string, 0, len(messages))
		for _, raw := range messages {
			roles = append(roles, raw.(map[string]any)["role"].(string))
		}
		if strings.Join(roles, ",") != "system,user,assistant,user" {
			t.Fatalf("history not replayed: %v", roles)
		}
		assistant := messages[2].(map[string]any)
		if !strings.Contains(assistant["content"].(string), "<<<ALLAIIS_TOOL_CALL>>>") {
			t.Errorf("assistant call not rendered: %v", assistant)
		}
		checked.Store(true)
		writeFrames(w, "Go 1.26.2")
	})
	handler, store := webGateway(t, server.URL)
	body := `{"model":"` + webModel + `","messages":[` +
		`{"role":"user","content":"read go.mod"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}]},` +
		`{"role":"tool","tool_call_id":"call_1","content":"go 1.26.2"}]` +
		`,"tools":[{"type":"function","function":{"name":"read_file","description":"read","parameters":{"type":"object"}}}]}`
	result := complete(handler, store.ClientKey(), "", body)
	if result.Code != http.StatusOK || !checked.Load() || !strings.Contains(result.Body.String(), "1.26.2") {
		t.Fatalf("got %d %s", result.Code, result.Body.String())
	}
}

func TestWebToolRepairRetry(t *testing.T) {
	var attempts atomic.Int32
	server := venueWithToolBodiesFunc(t, func(w http.ResponseWriter, body map[string]any) {
		if attempts.Add(1) == 1 {
			writeFrames(w, `<<<ALLAIIS_TOOL_CALL>>>{"calls":[{"name":"read_file","arguments":{"path":}}]}<<<END_TOOL_CALL>>>`)
			return
		}
		messages := body["prompt"].([]any)
		last := messages[len(messages)-1].(map[string]any)
		if !strings.Contains(last["content"].(string), "System validation error") {
			t.Errorf("repair message missing: %v", last)
		}
		writeFrames(w, toolBlock("fixed.txt"))
	})
	handler, store := webGateway(t, server.URL)
	result := complete(handler, store.ClientKey(), "", toolPayload)
	if result.Code != http.StatusOK || attempts.Load() != 2 || !strings.Contains(result.Body.String(), "fixed.txt") {
		t.Fatalf("repair failed: %d attempts=%d %s", result.Code, attempts.Load(), result.Body)
	}
}

func venueWithToolBodies(t *testing.T, reply string) *httptest.Server {
	t.Helper()
	return venueWithToolBodiesFunc(t, func(w http.ResponseWriter, _ map[string]any) { writeFrames(w, reply) })
}

func venueWithToolBodiesFunc(t *testing.T, reply func(http.ResponseWriter, map[string]any)) *httptest.Server {
	return veniceFixture(t, reply)
}

// writeFrames emits a reply as newline-delimited content events in small slices.
func writeFrames(w http.ResponseWriter, text string) {
	runes := []rune(text)
	for start := 0; start < len(runes); start += 7 {
		end := min(start+7, len(runes))
		frame, _ := json.Marshal(map[string]string{"kind": "content", "content": string(runes[start:end])})
		fmt.Fprintf(w, "%s\n", frame)
	}
}

func TestWebToolCallStreamDSML(t *testing.T) {
	server := venueWithToolBodies(t, "Reading <|DSML|calls><|DSML|invoke name=\"read_file\">"+
		"<|DSML|parameter name=\"path\" string=\"true\">c.txt</|DSML|parameter></|DSML|invoke></|DSML|calls>")
	handler, store := webGateway(t, server.URL)
	body := strings.Replace(toolPayload, `"messages"`, `"stream":true,"messages"`, 1)
	result := complete(handler, store.ClientKey(), "", body)
	text := result.Body.String()
	if result.Code != http.StatusOK || !strings.Contains(text, `"finish_reason":"tool_calls"`) ||
		!strings.Contains(text, `c.txt`) || strings.Contains(text, "DSML") {
		t.Fatalf("unexpected stream: %d %s", result.Code, text)
	}
}

func TestChatStreamUsageOnlyWhenRequested(t *testing.T) {
	run := func(include bool) string {
		server := venueWithToolBodies(t, "hello")
		handler, store := webGateway(t, server.URL)
		body := `{"model":"` + webModel + `","stream":true,`
		if include {
			body += `"stream_options":{"include_usage":true},`
		}
		body += `"messages":[{"role":"user","content":"hi"}]}`
		return complete(handler, store.ClientKey(), "", body).Body.String()
	}
	if text := run(true); !strings.Contains(text, `"usage":{`) || !strings.Contains(text, `"estimated":true`) {
		t.Fatalf("usage missing: %s", text)
	}
	if text := run(false); strings.Contains(text, `"usage":`) {
		t.Fatalf("unsolicited usage: %s", text)
	}
}

func TestStreamedToolCallIndexesKeepRising(t *testing.T) {
	reply := "A<|DSML|calls><|DSML|invoke name=\"read_file\">" +
		"<|DSML|parameter name=\"path\" string=\"true\">first-file.txt</|DSML|parameter></|DSML|invoke>" +
		"<|DSML|invoke name=\"read_file\"><|DSML|parameter name=\"path\" string=\"true\">second.txt</|DSML|parameter>" +
		"</|DSML|invoke></|DSML|calls>"
	server := venueWithToolBodies(t, reply)
	handler, store := webGateway(t, server.URL)
	body := strings.Replace(toolPayload, `"messages"`, `"stream":true,"messages"`, 1)
	result := complete(handler, store.ClientKey(), "", body)
	text := result.Body.String()
	if result.Code != http.StatusOK || !strings.Contains(text, `"index":0`) ||
		!strings.Contains(text, `"index":1`) || !strings.Contains(text, "second.txt") {
		t.Fatalf("indexes merged: %d %s", result.Code, text)
	}
}

func TestWhitespaceAssistantTextKeepsToolCalls(t *testing.T) {
	var seen map[string]any
	server := venueWithToolBodiesFunc(t, func(w http.ResponseWriter, body map[string]any) {
		seen = body
		writeFrames(w, "ok")
	})
	handler, store := webGateway(t, server.URL)
	payload := `{"model":"` + webModel + `","messages":[{"role":"user","content":"read a"},` +
		`{"role":"assistant","content":"\n\n","tool_calls":[{"id":"c1","type":"function",` +
		`"function":{"name":"read_file","arguments":"{\"path\":\"a.txt\"}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"A"},{"role":"user","content":"again"}]}`
	result := complete(handler, store.ClientKey(), "", payload)
	if result.Code != http.StatusOK {
		t.Fatalf("status %d %s", result.Code, result.Body)
	}
	prompt, _ := seen["prompt"].([]any)
	if len(prompt) < 2 {
		t.Fatalf("prompt %v", seen["prompt"])
	}
	assistant, _ := prompt[1].(map[string]any)["content"].(string)
	if !strings.Contains(assistant, "ALLAIIS_TOOL_CALL") || !strings.Contains(assistant, "a.txt") {
		t.Fatalf("tool call lost: %q", assistant)
	}
}

func TestDefaultResponseCapWhenClientOmitsTokens(t *testing.T) {
	var seen map[string]any
	server := venueWithToolBodiesFunc(t, func(w http.ResponseWriter, body map[string]any) {
		seen = body
		writeFrames(w, "ok")
	})
	handler, store := webGateway(t, server.URL)
	payload := `{"model":"` + webModel + `","messages":[{"role":"user","content":"hi"}]}`
	if result := complete(handler, store.ClientKey(), "", payload); result.Code != http.StatusOK {
		t.Fatalf("status %d %s", result.Code, result.Body)
	}
	if seen["maxTokens"] != float64(8192) {
		t.Fatalf("default cap %v", seen["maxTokens"])
	}
	payload = `{"model":"` + webModel + `","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	if result := complete(handler, store.ClientKey(), "", payload); result.Code != http.StatusOK {
		t.Fatalf("status %d %s", result.Code, result.Body)
	}
	if seen["maxTokens"] != float64(100) {
		t.Fatalf("explicit cap %v", seen["maxTokens"])
	}
}

func TestReplyNearTheTokenCapReportsLength(t *testing.T) {
	server := venueWithToolBodies(t, strings.Repeat("word ", 40))
	handler, store := webGateway(t, server.URL)
	payload := `{"model":"` + webModel + `","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
	result := complete(handler, store.ClientKey(), "", payload)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"finish_reason":"length"`) {
		t.Fatalf("finish %d %s", result.Code, result.Body)
	}
}

func TestReasoningCountsTowardTheCap(t *testing.T) {
	server := venueWithToolBodiesFunc(t, func(w http.ResponseWriter, body map[string]any) {
		frame, _ := json.Marshal(map[string]string{"kind": "reasoning", "content": strings.Repeat("think ", 60)})
		fmt.Fprintf(w, "%s\n", frame)
		frame, _ = json.Marshal(map[string]string{"kind": "content", "content": "short"})
		fmt.Fprintf(w, "%s\n", frame)
	})
	handler, store := webGateway(t, server.URL)
	payload := `{"model":"` + webModel + `","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
	result := complete(handler, store.ClientKey(), "", payload)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"finish_reason":"length"`) {
		t.Fatalf("finish %d %s", result.Code, result.Body)
	}
	if !strings.Contains(result.Body.String(), `"completion_tokens":365`) {
		t.Fatalf("reasoning not counted: %s", result.Body)
	}
}

func TestWebRepairsACallWithoutArguments(t *testing.T) {
	var attempts atomic.Int32
	server := venueWithToolBodiesFunc(t, func(w http.ResponseWriter, _ map[string]any) {
		if attempts.Add(1) == 1 {
			// A call that forgot its arguments, the client would reject it.
			writeFrames(w, `<<<ALLAIIS_TOOL_CALL>>>{"calls":[{"name":"read_file"}]}<<<END_TOOL_CALL>>>`)
			return
		}
		writeFrames(w, toolBlock("fixed.txt"))
	})
	handler, store := webGateway(t, server.URL)
	result := complete(handler, store.ClientKey(), "", toolPayload)
	if result.Code != http.StatusOK || attempts.Load() != 2 || !strings.Contains(result.Body.String(), "fixed.txt") {
		t.Fatalf("empty call not repaired: %d attempts=%d %s", result.Code, attempts.Load(), result.Body)
	}
}

func TestWebDeliversAnEmptyCallWhenRepairRunsOut(t *testing.T) {
	server := venueWithToolBodies(t, `<<<ALLAIIS_TOOL_CALL>>>{"calls":[{"name":"read_file"}]}<<<END_TOOL_CALL>>>`)
	handler, store := webGateway(t, server.URL)
	body := strings.Replace(toolPayload, `"messages"`, `"stream":true,"messages"`, 1)
	result := complete(handler, store.ClientKey(), "", body)
	text := result.Body.String()
	if result.Code != http.StatusOK || !strings.Contains(text, `"finish_reason":"tool_calls"`) ||
		!strings.Contains(text, `"read_file"`) {
		t.Fatalf("empty call dropped: %d %s", result.Code, text)
	}
}

func TestWebRepairsACallMissingARequiredField(t *testing.T) {
	var attempts atomic.Int32
	server := venueWithToolBodiesFunc(t, func(w http.ResponseWriter, _ map[string]any) {
		if attempts.Add(1) == 1 {
			// The field exists here but the required one does not.
			writeFrames(w, `<<<ALLAIIS_TOOL_CALL>>>{"calls":[{"name":"read_file","arguments":{"offset":1}}]}<<<END_TOOL_CALL>>>`)
			return
		}
		writeFrames(w, toolBlock("fixed.txt"))
	})
	handler, store := webGateway(t, server.URL)
	result := complete(handler, store.ClientKey(), "", toolPayload)
	if result.Code != http.StatusOK || attempts.Load() != 2 || !strings.Contains(result.Body.String(), "fixed.txt") {
		t.Fatalf("missing field not repaired: %d attempts=%d %s", result.Code, attempts.Load(), result.Body)
	}
}

func TestKnownToolWithFilledFieldsIsNotRepaired(t *testing.T) {
	var attempts atomic.Int32
	server := venueWithToolBodiesFunc(t, func(w http.ResponseWriter, _ map[string]any) {
		attempts.Add(1)
		writeFrames(w, toolBlock("a.txt"))
	})
	handler, store := webGateway(t, server.URL)
	result := complete(handler, store.ClientKey(), "", toolPayload)
	if result.Code != http.StatusOK || attempts.Load() != 1 {
		t.Fatalf("unnecessary repair: %d attempts=%d", result.Code, attempts.Load())
	}
}
