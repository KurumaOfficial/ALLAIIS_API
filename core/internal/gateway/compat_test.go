// SPDX-License-Identifier: GPL-3.0-only

package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const fixtureModelID = "zai-org-glm-5.2"

func fixture(t *testing.T, file string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "venice", file))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func serveFixture(t *testing.T, handler http.HandlerFunc) func(context.Context, []byte) (*http.Response, error) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 3 * time.Second
	return func(ctx context.Context, body []byte) (*http.Response, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Content-Type", "application/json")
		return client.Do(r)
	}
}

func fixtureReply(w http.ResponseWriter, status int, media, body string) {
	w.Header().Set("Content-Type", media)
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func toolRequest() compatRequest {
	return compatRequest{
		Model: fixtureModelID,
		Messages: []chatMessage{
			{Role: "system", Content: "Original system message."},
			{Role: "user", Content: "Write a local fixture file."},
		},
		Tools: []toolSpec{{Type: "function", Function: toolFunction{
			Name: "write_file", Description: "Return a file to the test runner.", Parameters: map[string]any{
				"type": "object", "required": []string{"path", "content"},
				"properties": map[string]any{
					"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"},
				},
			},
		}}},
	}
}

func jsonBody(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func deltaEvent(w io.Writer, kind, text string) {
	writeEvent(w, streamEvent{Type: kind, Delta: text})
}

func TestSSEFixtures(t *testing.T) {
	t.Setenv("ALLAIIS_CONTEXT_TRIM", "off")
	normal := fixture(t, "text.sse")
	var events []string
	if err := readEventStream(strings.NewReader(normal), func(event streamEvent) error {
		events = append(events, event.Type)
		if event.Type == "veniceai:tool_call_generation.in_progress" && len(event.Detected) == 0 {
			t.Error("detected_tools missing from fixture")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"response.created", "response.in_progress", "response.output_item.added", "response.output_item.done",
		"response.content_part.added", "response.content_part.done", "response.output_text.delta", "response.output_text.done",
		"response.reasoning_text.delta", "response.reasoning_summary_text.delta", "response.completed",
		"veniceai:tool_call_generation.in_progress", "veniceai:tool_call_generation.done", "veniceai:server_function_call",
	} {
		if !slices.Contains(events, name) {
			t.Errorf("missing event %s", name)
		}
	}
	for _, test := range []struct {
		file, media, code string
		status, response  int
	}{
		{"text.sse", "text/event-stream", "", 200, 200},
		{"truncated.sse", "text/event-stream", "upstream_stream_truncated", 200, 502},
		{"error.sse", "text/event-stream", "upstream_stream_error", 200, 502},
		{"failed.sse", "text/event-stream", "upstream_stream_error", 200, 502},
		{"incomplete.sse", "text/event-stream", "upstream_stream_incomplete", 200, 502},
		{"context-413.json", "application/json", "context_length_exceeded", 413, 400},
		{"forbidden-403.html", "text/html", "upstream_access_denied", 403, 403},
	} {
		t.Run(test.file, func(t *testing.T) {
			body := fixture(t, test.file)
			invoke := serveFixture(t, func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				fixtureReply(w, test.status, test.media, body)
			})
			result, err := runCompatibility(t.Context(), jsonBody(t, compatRequest{
				Model: fixtureModelID, Messages: []chatMessage{{Role: "user", Content: "hello"}},
			}), fixtureModel{}, 0, nil, invoke)
			reply := httptest.NewRecorder()
			writeCompatibility(reply, "fixture", fixtureModelID, result, false, err)
			if reply.Code != test.response || !strings.Contains(reply.Body.String(), test.code) ||
				strings.Contains(reply.Body.String(), "synthetic private") || strings.Contains(reply.Body.String(), "<html>") {
				t.Fatalf("got %d %s", reply.Code, reply.Body.String())
			}
			if test.response == 200 && (result.content != "Hello, world." || result.reasoning != "Plan. Verify. Done.") {
				t.Fatalf("wrong classification: %+v", result)
			}
		})
	}
}

func TestToolFixtureCorpus(t *testing.T) {
	t.Setenv("ALLAIIS_TOOL_FORMAT", "")
	for _, format := range []toolFormat{formatGLM, formatJSON, formatProxy, formatDSML} {
		t.Run(string(format), func(t *testing.T) {
			template := strings.TrimSpace(fixture(t, "tool-"+string(format)+".txt"))
			passed := 0
			for i := range 20 {
				t.Run(fmt.Sprint(i), func(t *testing.T) {
					path := fmt.Sprintf("fixtures/case-%02d-λ.js", i)
					source := fmt.Sprintf("const n = %d;\nconst quote = \"hello \\\"world\\\"\";\nconst path = \"C:\\\\test\\\\file\";\nconst tpl = `value: ${n}`;\n", i)
					wirePath, wireContent := path, source
					if format == formatJSON || format == formatProxy {
						wirePath, wireContent = string(jsonBody(t, path)), string(jsonBody(t, source))
					}
					envelope := strings.NewReplacer("PATH_FIXTURE", wirePath, "CONTENT_FIXTURE", wireContent).Replace(template)
					if i%4 == 0 {
						envelope = envelope[:strings.LastIndex(envelope, "</")]
					}
					req := toolRequest()
					// Ask for a different encoding to exercise auto-detection.
					if format == formatGLM {
						req.Model = "qwen3.5-fixture"
					}
					invoke := serveFixture(t, func(w http.ResponseWriter, r *http.Request) {
						io.Copy(io.Discard, r.Body)
						w.Header().Set("Content-Type", "text/event-stream")
						runes := []rune(envelope)
						first := 1 + i%len(runes)
						deltaEvent(w, "response.output_text.delta", string(runes[:first]))
						deltaEvent(w, "response.output_text.delta", string(runes[first:]))
						fmt.Fprint(w, "data: [DONE]\n\n")
					})
					result, err := runCompatibility(t.Context(), jsonBody(t, req), fixtureModel{}, 0, nil, invoke)
					if err != nil || len(result.calls) != 1 {
						t.Logf("parse failed: %+v %v", result, err)
						return
					}
					var args map[string]string
					call := result.calls[0]
					if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil ||
						call.Function.Name != "write_file" || args["path"] != path || args["content"] != source {
						t.Logf("arguments changed: %s", call.Function.Arguments)
						return
					}
					passed++
				})
			}
			t.Logf("%d/20 exact fixture matches", passed)
			if passed < 18 {
				t.Fatalf("success rate below 0.9: %d/20", passed)
			}
		})
	}
}

func TestToolParserRecovery(t *testing.T) {
	request := toolRequest()
	_, policy, _, err := prepareCompatibility(jsonBody(t, request), fixtureModel{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ input, content string }{
		{`<tool_call>{"name":"write_file","path":"file","content":"top level"}</tool_call>`, "top level"},
		{`<tool_call>{"name":"write_file","path":"file","content":"literal </tool_call> tag"}</tool_call>`, "literal </tool_call> tag"},
		{"<tool_call>{\"name\":\"write_file\",\"arguments\":{\"path\":\"file\",\"content\":\"literal \"quotes\"\nnext\"}}</tool_call>", "literal \"quotes\"\nnext"},
		{`<proxy_call>{"name":"write_file","arguments":{"path":"file","content":"complete value"`, "complete value"},
		{"<|tool▁call▁begin|>function<|tool▁sep|>write_file\n```json\n{\"path\":\"file\",\"content\":\"legacy\"}\n```<|tool▁call▁end|>", "legacy"},
	} {
		_, calls, err := policy.extract(test.input, true)
		if err != nil || len(calls) != 1 {
			t.Errorf("%q: calls=%v err=%v", test.input, calls, err)
			continue
		}
		var args map[string]string
		if json.Unmarshal([]byte(calls[0].Function.Arguments), &args) != nil || args["content"] != test.content {
			t.Errorf("wrong file content: %s", calls[0].Function.Arguments)
		}
	}
	for _, bad := range []string{
		`<tool_call>{"name":"write_file","arguments":{"path":"required content omitted"}}</tool_call>`,
		`<tool_call>{"name":"write_file","arguments":{"path":"file","content":"cut mid-value`,
		`<tool_call>write_file<arg_key>path</arg_key><arg_value>file</arg_value>`,
		`<tool_call`,
	} {
		_, calls, err := policy.extract(bad, true)
		if !errors.Is(err, errInvalidTool) || len(calls) != 0 {
			t.Errorf("unsafe recovery for %q: %v %v", bad, calls, err)
		}
	}
}

func TestToolsCancelAndRetry(t *testing.T) {
	const invalid = `<tool_call>{"name":"write_file","arguments":{"path":"a"}}</tool_call>`
	const valid = `<tool_call>{"name":"write_file","arguments":{"path":"a","content":"abc"}}</tool_call>`
	for _, test := range []struct {
		name          string
		first, second string
		wantCalls     int
		wantAttempts  int32
		one           bool
	}{
		{"retry recovers", invalid, valid, 1, 2, false},
		{"retry falls back to second text", invalid, "fallback " + invalid, 0, 2, false},
		{"multiple blocks", valid + valid, "", 2, 1, false},
		{"parallel false stops at first", valid + invalid, "", 1, 1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var attempts atomic.Int32
			canceled := make(chan struct{}, 2)
			invoke := serveFixture(t, func(w http.ResponseWriter, r *http.Request) {
				data, _ := io.ReadAll(r.Body)
				reply := test.first
				if attempts.Add(1) == 2 {
					var second compatPayload
					if json.Unmarshal(data, &second) != nil || !strings.Contains(second.Messages[len(second.Messages)-1].Content, "every required argument") {
						t.Error("format reminder missing")
					}
					reply = test.second
				}
				w.Header().Set("Content-Type", "text/event-stream")
				deltaEvent(w, "response.output_text.delta", reply)
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					canceled <- struct{}{}
				case <-time.After(2 * time.Second):
					t.Error("the upstream kept running after a tool block")
				}
			})
			input := toolRequest()
			if test.one {
				no := false
				input.Parallel = &no
			}
			result, err := runCompatibility(t.Context(), jsonBody(t, input), fixtureModel{}, 0, nil, invoke)
			if err != nil || len(result.calls) != test.wantCalls || attempts.Load() != test.wantAttempts {
				t.Fatalf("result=%+v attempts=%d err=%v", result, attempts.Load(), err)
			}
			if test.wantCalls == 0 && result.content != test.second {
				t.Fatalf("expected only final attempt text, got %q", result.content)
			}
			for range test.wantAttempts {
				select {
				case <-canceled:
				case <-time.After(time.Second):
					t.Fatal("missing cancellation")
				}
			}
		})
	}
}

func TestReasoningBudgetCancelsAndReturnsStreamError(t *testing.T) {
	var attempts atomic.Int32
	canceled := make(chan struct{}, 2)
	invoke := serveFixture(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if attempts.Add(1) == 2 && !bytes.Contains(raw, []byte("Keep your reasoning brief")) {
			t.Error("retry did not ask for shorter reasoning")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		deltaEvent(w, "response.reasoning_text.delta", strings.Repeat("行", defaultReasoningLimit+1))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			canceled <- struct{}{}
		case <-time.After(2 * time.Second):
			t.Error("reasoning stream was not canceled")
		}
	})
	result, err := runCompatibility(t.Context(), jsonBody(t, toolRequest()), fixtureModel{}, 0, nil, invoke)
	rec := httptest.NewRecorder()
	writeCompatibility(rec, "fixture", fixtureModelID, result, true, err)
	if attempts.Load() != 2 || rec.Code != 200 || rec.Header().Get("Content-Type") != "text/event-stream" ||
		!strings.Contains(rec.Body.String(), `"type":"invalid_request_error"`) ||
		!strings.Contains(rec.Body.String(), `"code":"reasoning_limit_exceeded"`) || strings.Contains(rec.Body.String(), "行") {
		t.Fatalf("unexpected failure response: %d %s", rec.Code, rec.Body.String())
	}
	for range 2 {
		select {
		case <-canceled:
		case <-time.After(time.Second):
			t.Fatal("missing cancellation")
		}
	}
}

func TestToolPolicyHistoryAndNUL(t *testing.T) {
	t.Setenv("ALLAIIS_TOOL_FORMAT", "")
	input := toolRequest()
	input.ToolChoice = json.RawMessage(`"required"`)
	input.Messages = append(input.Messages,
		chatMessage{Role: "user", Content: "cont\x00inue"},
		chatMessage{Role: "assistant"},
		chatMessage{Role: "assistant", ToolCalls: []toolCall{{ID: "call_1", Function: toolArgument{
			Name: "write_file", Arguments: `{"path":"f","content":"text"}`,
		}}}},
		chatMessage{Role: "tool", ToolCallID: "call_1", Content: "first"},
		chatMessage{Role: "tool", ToolCallID: "call_2", Content: "sec\x00ond"},
	)
	input.Tools[0].Function.Description += "\x00"
	payload, policy, nul, err := prepareCompatibility(jsonBody(t, input), fixtureModel{})
	if err != nil || nul != 3 || policy.format != formatGLM {
		t.Fatalf("prepare=%+v %d %v", policy, nul, err)
	}
	system := payload.SystemPrompt.Instructions
	if !strings.HasPrefix(system, "Original system message.\n\n## Tools\n") ||
		!strings.Contains(system, "Call at least one") ||
		*payload.IncludeSystemPrompt || *payload.WebEnabled || *payload.WebScrapeEnabled || *payload.XSearchEnabled {
		t.Fatalf("unexpected system or flags: %+v", payload)
	}
	if len(payload.Messages) != 4 || payload.Messages[0].Content != "Write a local fixture file.\n\ncontinue" ||
		payload.Messages[1].Role != "assistant" || payload.Messages[1].Content != "" ||
		!strings.Contains(payload.Messages[2].Content, "<tool_call>") || payload.Messages[3].Role != "user" ||
		strings.Count(payload.Messages[3].Content, "<tool_result>") != 2 {
		t.Fatalf("unexpected history: %+v", payload.Messages)
	}
	for model, want := range map[string]toolFormat{
		"qwen3-coder": formatJSON, "qwen3-8b": formatJSON, "qwen-3-8-27b": formatProxy,
		"qwen3.5": formatProxy, "qwen3.10-fixture": formatProxy, "deepseek-v4.1": formatDSML, "glm-5.2": formatGLM,
	} {
		if got, err := modelToolFormat(model); err != nil || got != want {
			t.Errorf("format for %q: %q %v", model, got, err)
		}
	}
	t.Setenv("ALLAIIS_TOOL_FORMAT", "dsml")
	input.ToolChoice = json.RawMessage(`{"type":"function","function":{"name":"write_file"}}`)
	payload, policy, _, err = prepareCompatibility(jsonBody(t, input), fixtureModel{})
	if err != nil || policy.format != formatDSML || !strings.Contains(payload.SystemPrompt.Instructions, "named tool: write_file") {
		t.Fatal("named selection or format override missing")
	}
	input.Tools = append(input.Tools, toolSpec{Type: "function", Function: toolFunction{Name: "required"}})
	input.ToolChoice = json.RawMessage(`{"type":"function","function":{"name":"required"}}`)
	_, policy, _, err = prepareCompatibility(jsonBody(t, input), fixtureModel{})
	if err != nil || len(policy.tools) != 1 || policy.named != "required" {
		t.Fatal("named tool was treated as a tool choice mode")
	}
	input.ToolChoice = json.RawMessage(`"none"`)
	payload, policy, _, err = prepareCompatibility(jsonBody(t, input), fixtureModel{Agent: true})
	if err != nil || len(policy.tools) != 0 || payload.WebEnabled != nil || payload.SystemPrompt.Instructions != "Original system message." {
		t.Fatal("tool_choice none left emulation enabled")
	}
	if _, _, err := cleanPayloadJSON([]byte(`{"x":"ok","x\u0000":"collision"}`)); err == nil {
		t.Fatal("NUL cleanup silently merged keys")
	}
}

func TestUsageFromSentPayloadAndFinalSSEChunk(t *testing.T) {
	stream := fixture(t, "text.sse")
	var upstreamSize atomic.Int32
	invoke := serveFixture(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !bytes.Contains(raw, []byte("## Tools")) {
			t.Error("usage did not include tool prompt")
		}
		upstreamSize.Store(int32(len(raw)))
		fixtureReply(w, 200, "text/event-stream", stream)
	})
	input := toolRequest()
	input.Messages[1].Content = "行\x00"
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	result, err := runCompatibility(t.Context(), jsonBody(t, input), fixtureModel{}, 0, log, invoke)
	want := estimateUsage(int(upstreamSize.Load()), len("Hello, world.Plan. Verify. Done."))
	if err != nil || result.usage != want || estimateUsage(7, 8) != (tokenUsage{2, 3, 5, true}) {
		t.Fatalf("usage=%+v want=%+v err=%v", result.usage, want, err)
	}
	for _, stream := range []bool{false, true} {
		rec := httptest.NewRecorder()
		writeCompatibility(rec, "fixture", fixtureModelID, result, stream, err)
		if !strings.Contains(rec.Body.String(), `"usage":`+string(jsonBody(t, want))) ||
			(stream && !strings.Contains(rec.Body.String(), `"choices":[]`)) {
			t.Fatalf("missing usage info: %s", rec.Body.String())
		}
	}
	for _, key := range []string{"time", "origin", "request_bytes", "status", "model", "content_chars",
		"reasoning_chars", "tools", "calls", "attempts", "duration_ms"} {
		if !strings.Contains(logs.String(), `"`+key+`":`) {
			t.Errorf("missing log field %s", key)
		}
	}
	if !strings.Contains(logs.String(), `"nul_removed":1`) ||
		strings.Contains(logs.String(), "Original system message") || strings.Contains(logs.String(), "Hello, world.") {
		t.Fatalf("unexpected log data: %s", logs.String())
	}
}

func TestContextOverflowTrimsOrReturns400(t *testing.T) {
	tooLong := fixture(t, "context-413.json")
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprint("trim off=", disabled), func(t *testing.T) {
			if disabled {
				t.Setenv("ALLAIIS_CONTEXT_TRIM", "off")
			} else {
				t.Setenv("ALLAIIS_CONTEXT_TRIM", "")
			}
			var lengths []int
			var mu sync.Mutex
			invoke := serveFixture(t, func(w http.ResponseWriter, r *http.Request) {
				var payload compatPayload
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				message := payload.Messages[0].Content
				mu.Lock()
				lengths = append(lengths, len(message))
				retried := len(lengths) > 1
				mu.Unlock()
				if retried && !strings.Contains(message, trimmedMarker) {
					t.Error("trim marker missing")
				}
				fixtureReply(w, 413, "application/json", tooLong)
			})
			result, err := runCompatibility(t.Context(), jsonBody(t, compatRequest{
				Model: fixtureModelID, Messages: []chatMessage{{Role: "user", Content: strings.Repeat("x", 1000)}},
			}), fixtureModel{}, 0, nil, invoke)
			want := []int{1000, 750, 500, 350}
			if disabled {
				want = want[:1]
			}
			mu.Lock()
			sentSizes := slices.Clone(lengths)
			mu.Unlock()
			if !slices.Equal(want, sentSizes) {
				t.Fatalf("sent sizes %v, want %v", sentSizes, want)
			}
			rec := httptest.NewRecorder()
			writeCompatibility(rec, "fixture", fixtureModelID, result, false, err)
			if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"code":"context_length_exceeded"`) {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
	payload := compatPayload{Messages: []turn{
		{Role: "user", Content: strings.Repeat("a", 70)},
		{Role: "assistant", Content: strings.Repeat("b", 50)},
		{Role: "user", Content: strings.Repeat("世", 40)},
	}}
	if trimmed := trimHistory(payload, 75); len(trimmed.Messages) != 2 || trimmed.Messages[1] != payload.Messages[2] {
		t.Fatalf("oldest turn was not removed first: %+v", trimmed.Messages)
	}
}

func TestOfflineCatalogAndAgentToolRejection(t *testing.T) {
	var models []fixtureModel
	if err := json.Unmarshal([]byte(fixture(t, "models.json")), &models); err != nil {
		t.Fatal(err)
	}
	free := offlineCatalog(models)
	if len(free) != 2 || free[0].ID != fixtureModelID || free[0].Context != 131072 || free[0].Output != 8192 ||
		!free[0].toolsSupported() || free[1].toolsSupported() {
		t.Fatalf("wrong free models: %+v", free)
	}
	input := toolRequest()
	input.Model = free[1].ID
	result, err := runCompatibility(t.Context(), jsonBody(t, input), free[1], 0, nil,
		func(context.Context, []byte) (*http.Response, error) {
			t.Fatal("unsupported tools reached upstream")
			return nil, nil
		})
	rec := httptest.NewRecorder()
	writeCompatibility(rec, "fixture", input.Model, result, false, err)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"code":"tools_not_supported"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}
