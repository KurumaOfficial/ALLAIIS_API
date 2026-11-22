// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// chatShape renders OpenAI Chat Completions events and JSON.
type chatShape struct {
	w       http.ResponseWriter
	id      string
	model   string
	created int64
}

func (s *chatShape) reasoning(text string) {
	writeChunk(s.w, s.id, s.model, s.created, map[string]any{"reasoning_content": text})
}

func (s *chatShape) text(text string) {
	writeChunk(s.w, s.id, s.model, s.created, map[string]any{"content": text})
}

func (s *chatShape) calls(calls []toolCall) {
	writeToolChunks(s.w, s.id, s.model, s.created, calls, false)
}

func (s *chatShape) end(reason string) {
	if reason == "tool_calls" {
		writeToolChunks(s.w, s.id, s.model, s.created, nil, true)
	}
	finished(s.w, s.id, s.model, s.created, reason)
	fmt.Fprint(s.w, "data: [DONE]\n\n")
}

func (s *chatShape) reply(content string, calls []toolCall, usage map[string]any) {
	message := map[string]any{"role": "assistant", "content": content}
	finish := "stop"
	if len(calls) > 0 {
		message["content"] = nil
		message["tool_calls"] = calls
		finish = "tool_calls"
	}
	writeJSON(s.w, http.StatusOK, map[string]any{
		"id": s.id, "object": "chat.completion", "created": s.created, "model": s.model,
		"choices": []any{map[string]any{"index": 0, "finish_reason": finish, "message": message}},
		"usage":   usage,
	})
}

func writeChunk(w http.ResponseWriter, id, model string, created int64, delta map[string]any) {
	data, _ := json.Marshal(map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}},
	})
	fmt.Fprintf(w, "data: %s\n\n", data)
}

func nowUnix() int64 { return time.Now().Unix() }

// writeToolChunks emits tool deltas, or only the final empty delta when calls is nil.
func writeToolChunks(w http.ResponseWriter, id, model string, created int64, calls []toolCall, final bool) {
	if !final && len(calls) == 0 {
		return
	}
	delta := make([]any, 0, len(calls))
	for index, call := range calls {
		delta = append(delta, map[string]any{
			"index": index, "id": call.ID, "type": "function",
			"function": map[string]string{"name": call.Function.Name, "arguments": call.Function.Arguments},
		})
	}
	if len(delta) == 0 {
		return
	}
	writeChunk(w, id, model, created, map[string]any{"tool_calls": delta})
}

func finished(w http.ResponseWriter, id, model string, created int64, reason string) {
	data, _ := json.Marshal(map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": reason}},
	})
	fmt.Fprintf(w, "data: %s\n\n", data)
}
