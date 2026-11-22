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
		!strings.Contains(text, `"content":"Reading"`) || strings.Contains(text, "ALLAIIS_TOOL_CALL") {
		t.Fatalf("unexpected stream: %d %s", result.Code, text)
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
