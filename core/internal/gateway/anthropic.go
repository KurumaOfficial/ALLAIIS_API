// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

// messagesRequest flattens an Anthropic Messages payload into web fields.
func messagesRequest(payload []byte, model string) (webprov.Request, *webReason, []toolSpec, error) {
	var input struct {
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		MaxTokens   int             `json:"max_tokens"`
		Temperature *float64        `json:"temperature"`
		TopP        *float64        `json:"top_p"`
		Stops       []string        `json:"stop_sequences"`
		ToolChoice  json.RawMessage `json:"tool_choice"`
		Tools       []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Schema      json.RawMessage `json:"input_schema"`
		} `json:"tools"`
		Thinking *struct {
			Type         string `json:"type"`
			BudgetTokens int    `json:"budget_tokens"`
		} `json:"thinking"`
	}
	if err := decodeJSONObject(payload, &input); err != nil {
		return webprov.Request{}, nil, nil, err
	}
	request := webprov.Request{
		RequestID: randomID(), Model: model,
		Temperature: input.Temperature, TopP: input.TopP, MaxTokens: input.MaxTokens,
		StopSequences: cleanStops(input.Stops),
	}
	request.Reasoning = input.Thinking != nil || strings.Contains(strings.ToLower(model), "reasoning")
	if input.Thinking != nil && input.Thinking.Type != "disabled" && input.Thinking.BudgetTokens > 0 {
		request.ReasoningEffort = effortFor(input.Thinking.BudgetTokens)
	}
	reason := &webReason{reasoning: request.Reasoning}
	if text, images, missing := messageParts(input.System); missing > 0 {
		return webprov.Request{}, nil, nil, attachmentError()
	} else if len(images) > 0 {
		return webprov.Request{}, nil, nil, systemImageError()
	} else if text != "" {
		reason.system = text
	}
	for _, message := range input.Messages {
		text, images, missing := messageParts(message.Content)
		if missing > 0 {
			return webprov.Request{}, nil, nil, attachmentError()
		}
		switch message.Role {
		case "assistant":
			if calls := anthropicToolCalls(message.Content); len(calls) > 0 {
				text = renderCallList(text, calls)
			}
			if text == "" {
				text = "(empty)"
			}
			reason.turns = append(reason.turns, webprov.Message{Role: "assistant", Content: text})
		default:
			reason.turns = append(reason.turns, webprov.Message{Role: "user", Content: text, Images: images})
		}
	}
	tools := make([]toolSpec, 0, len(input.Tools))
	for _, tool := range input.Tools {
		spec := toolSpec{Type: "function"}
		spec.Function.Name = tool.Name
		spec.Function.Description = tool.Description
		spec.Function.Parameters = tool.Schema
		tools = append(tools, spec)
	}
	if len(tools) > 0 {
		// The Anthropic field stays raw here; choiceNote wants a decoded value.
		var choice any
		_ = json.Unmarshal(input.ToolChoice, &choice)
		if name := forcedToolName(choice); name != "" && !offeredTool(tools, name) {
			return webprov.Request{}, nil, nil, fmt.Errorf(
				"tool_choice names %q, which is not in the offered tools", name)
		}
		if reason.choiceNote(choice, nil) {
			tools = nil
		}
	}
	if len(reason.turns) == 0 {
		reason.turns = append(reason.turns, webprov.Message{Role: "user", Content: ""})
	}
	request.Messages = reason.messages(reason.system)
	return request, reason, tools, nil
}

// effortFor maps a thinking budget onto the coarse effort scale.
func effortFor(budget int) string {
	switch {
	case budget < 2048:
		return "low"
	case budget < 8192:
		return "medium"
	case budget < 32000:
		return "high"
	default:
		return "max"
	}
}

// anthropicShape renders Anthropic Messages events and JSON.
type anthropicShape struct {
	w       http.ResponseWriter
	id      string
	model   string
	block   int
	open    bool
	tooling bool
	started bool
	// stopped is the caller's stop sequence that ended the reply, if any.
	stopped string
}

func (s *anthropicShape) begin(kind string, content map[string]any) {
	s.block++
	payload, _ := json.Marshal(map[string]any{
		"type": "content_block_start", "index": s.block,
		"content_block": merge(map[string]any{"type": kind}, content),
	})
	fmt.Fprintf(s.w, "event: content_block_start\ndata: %s\n\n", payload)
	s.open = true
}

func (s *anthropicShape) delta(kind string, body map[string]any) {
	inner := merge(map[string]any{"type": kind}, body)
	payload, _ := json.Marshal(map[string]any{
		"type": "content_block_delta", "index": s.block, "delta": inner,
	})
	fmt.Fprintf(s.w, "event: content_block_delta\ndata: %s\n\n", payload)
}

func (s *anthropicShape) close() {
	if !s.open {
		return
	}
	payload, _ := json.Marshal(map[string]any{"type": "content_block_stop", "index": s.block})
	fmt.Fprintf(s.w, "event: content_block_stop\ndata: %s\n\n", payload)
	s.open = false
}

func (s *anthropicShape) hello() {
	if s.started {
		return
	}
	s.started = true
	payload, _ := json.Marshal(map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": s.id, "type": "message", "role": "assistant",
			"model": s.model, "content": []any{}, "stop_reason": nil,
			"stop_sequence": nil, "usage": map[string]int{"input_tokens": 0, "output_tokens": 0},
		},
	})
	fmt.Fprintf(s.w, "event: message_start\ndata: %s\n\n", payload)
}

func (s *anthropicShape) reasoning(text string) {
	s.hello()
	if !s.open {
		s.begin("thinking", map[string]any{"thinking": "", "signature": ""})
	}
	s.delta("thinking_delta", map[string]any{"thinking": text})
}

func (s *anthropicShape) text(text string) {
	s.hello()
	if !s.open || s.tooling {
		s.close()
		s.tooling = false
		s.begin("text", map[string]any{"text": ""})
	}
	s.delta("text_delta", map[string]any{"text": text})
}

func (s *anthropicShape) calls(calls []toolCall) {
	s.hello()
	if s.tooling {
		s.close()
	}
	s.tooling = true
	for _, call := range calls {
		s.begin("tool_use", map[string]any{"id": call.ID, "name": call.Function.Name, "input": map[string]any{}})
		s.delta("input_json_delta", map[string]any{"partial_json": call.Function.Arguments})
		s.close()
	}
}

func (s *anthropicShape) end(reason string, usage map[string]any) {
	s.hello()
	s.close()
	stop, sequence := s.stopReason(reason)
	payload, _ := json.Marshal(map[string]any{
		"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": sequence},
		"usage": anthropicUsage(usage),
	})
	fmt.Fprintf(s.w, "event: message_delta\ndata: %s\n\n", payload)
	fmt.Fprint(s.w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
}

// anthropicUsage maps the internal counters onto the Messages fields.
func anthropicUsage(usage map[string]any) map[string]any {
	output, _ := usage["completion_tokens"].(int)
	input, _ := usage["prompt_tokens"].(int)
	return map[string]any{"input_tokens": input, "output_tokens": output}
}

// anthropicToolCalls extracts tool_use blocks from an assistant turn.
func anthropicToolCalls(raw json.RawMessage) []toolCall {
	var blocks []struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var calls []toolCall
	for _, block := range blocks {
		if block.Type != "tool_use" || block.Name == "" {
			continue
		}
		arguments := string(block.Input)
		if arguments == "" || arguments == "null" {
			arguments = "{}"
		}
		call := toolCall{ID: block.ID, Type: "function"}
		if call.ID == "" {
			call.ID = "call_" + randomID()
		}
		call.Function.Name = block.Name
		call.Function.Arguments = arguments
		calls = append(calls, call)
	}
	return calls
}

// stoppedAt remembers the caller's own stop sequence that ended the reply.
func (s *anthropicShape) stoppedAt(word string) { s.stopped = word }

// stopReason maps a gateway finish reason to the Anthropic pair. A matched stop
// sequence wins over every generic reason: the client is waiting for its string.
func (s *anthropicShape) stopReason(reason string) (string, any) {
	if s.stopped != "" {
		return "stop_sequence", s.stopped
	}
	switch reason {
	case "length":
		return "max_tokens", nil
	case "tool_calls", "":
		return "tool_use", nil
	}
	return "end_turn", nil
}

func (s *anthropicShape) reply(content, reasoning string, calls []toolCall, usage map[string]any, finish string) {
	blocks := make([]any, 0, 2+len(calls))
	stop, sequence := s.stopReason(finish)
	if len(calls) > 0 {
		stop, sequence = "tool_use", nil
	}
	if reasoning != "" {
		blocks = append(blocks, map[string]any{"type": "thinking", "thinking": reasoning, "signature": ""})
	}
	if content != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": content})
	}
	for _, call := range calls {
		blocks = append(blocks, map[string]any{
			"type": "tool_use", "id": call.ID, "name": call.Function.Name,
			"input": json.RawMessage(call.Function.Arguments),
		})
	}
	payload, _ := json.Marshal(map[string]any{
		"id": s.id, "type": "message", "role": "assistant", "model": s.model,
		"content": blocks, "stop_reason": stop, "stop_sequence": sequence,
		"usage": map[string]any{
			"input_tokens": usage["prompt_tokens"], "output_tokens": usage["completion_tokens"],
		},
	})
	s.w.Header().Set("Content-Type", "application/json")
	s.w.WriteHeader(http.StatusOK)
	s.w.Write(payload)
}

func newShape(protocol config.Protocol, w http.ResponseWriter, id, model string, _ int64) webShape {
	switch protocol {
	case config.Messages:
		return &anthropicShape{w: w, id: id, model: model, block: -1}
	case config.Responses:
		return &responsesShape{w: w, id: id, model: model, created: nowUnix(), item: -1}
	}
	return &chatShape{w: w, id: id, model: model, created: nowUnix()}
}

func merge(base, extra map[string]any) map[string]any {
	for key, value := range extra {
		base[key] = value
	}
	return base
}
