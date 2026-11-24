// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

// responsesRequest flattens an OpenAI Responses payload into web fields.
func responsesRequest(payload []byte, model string) (webprov.Request, *webReason, []toolSpec, error) {
	var input struct {
		Instructions string          `json:"instructions"`
		Input        json.RawMessage `json:"input"`
		MaxTokens    int             `json:"max_output_tokens"`
		Temperature  *float64        `json:"temperature"`
		TopP         *float64        `json:"top_p"`
		Reasoning    struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		Text struct {
			Format struct {
				Type       string          `json:"type"`
				Name       string          `json:"name"`
				JSONSchema json.RawMessage `json:"schema"`
			} `json:"format"`
		} `json:"text"`
		Previous string          `json:"previous_response_id"`
		Stop     json.RawMessage `json:"stop"`
		Store    *bool           `json:"store"`
		Tools    []struct {
			Type       string          `json:"type"`
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"tools"`
		ToolChoice    any   `json:"tool_choice"`
		ParallelCalls *bool `json:"parallel_tool_calls"`
	}
	if err := decodeJSONObject(payload, &input); err != nil {
		return webprov.Request{}, nil, nil, err
	}
	if trimmed := strings.TrimSpace(string(input.Input)); trimmed == "" || trimmed == "null" || trimmed == "[]" {
		return webprov.Request{}, nil, nil, errors.New("input is required: a string or an array of items")
	}
	request := webprov.Request{
		RequestID: randomID(), Model: model,
		Temperature: input.Temperature, TopP: input.TopP, MaxTokens: input.MaxTokens,
		StopSequences: stopList(input.Stop),
		Store:         input.Store,
	}
	request.Reasoning = input.Reasoning.Effort != "" || strings.Contains(strings.ToLower(model), "reasoning")
	reason := &webReason{reasoning: request.Reasoning}
	reason.system = input.Instructions

	var text string
	if json.Unmarshal(input.Input, &text) != nil {
		if err := responsesTurns(input.Input, reason); err != nil {
			return webprov.Request{}, nil, nil, err
		}
	} else if text != "" {
		reason.turns = append(reason.turns, webprov.Message{Role: "user", Content: text})
	}
	// Instructions and developer items are in; the stand-in for a
	// structured-output switch goes after them.
	format := input.Text.Format
	if err := checkFormat(format.Type, "text.format.type"); err != nil {
		return webprov.Request{}, nil, nil, err
	}
	if name, schema, wanted := structuredFormat(format.Type, format.JSONSchema, format.Name); wanted {
		reason.jsonMode(name, schema)
	}
	tools := make([]toolSpec, 0, len(input.Tools))
	for _, tool := range input.Tools {
		if tool.Type != "" && tool.Type != "function" {
			return webprov.Request{}, nil, nil, fmt.Errorf(
				"tool type %q is not supported; only function tools", tool.Type)
		}
		if tool.Name == "" {
			return webprov.Request{}, nil, nil, errors.New("every tool must have a non-empty name")
		}
		if tool.Name == "" {
			continue
		}
		spec := toolSpec{Type: "function"}
		spec.Function.Name = tool.Name
		spec.Function.Parameters = tool.Parameters
		tools = append(tools, spec)
	}
	if len(tools) > 0 {
		if name := forcedToolName(input.ToolChoice); name != "" && !offeredTool(tools, name) {
			return webprov.Request{}, nil, nil, fmt.Errorf(
				"tool_choice names %q, which is not in the offered tools", name)
		}
		if reason.choiceNote(input.ToolChoice, input.ParallelCalls) {
			tools = nil
		}
	}
	if len(reason.turns) == 0 {
		reason.turns = append(reason.turns, webprov.Message{Role: "user", Content: ""})
	}
	if input.Previous != "" {
		request.Parent = input.Previous
	}
	request.Messages = reason.messages(reason.system)
	return request, reason, tools, nil
}

// responsesTurns maps typed input items onto web turns.
func responsesTurns(raw json.RawMessage, reason *webReason) error {
	var items []struct {
		Type      string          `json:"type"`
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		Name      string          `json:"name"`
		CallID    string          `json:"call_id"`
		Arguments string          `json:"arguments"`
		Output    json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return errors.New("input must be a string or an array of items")
	}
	for _, item := range items {
		switch item.Type {
		case "", "message":
			text, images, missing := messageParts(item.Content)
			if missing > 0 {
				return attachmentError()
			}
			switch item.Role {
			case "assistant":
				if text == "" {
					text = "(empty)"
				}
				reason.turns = append(reason.turns, webprov.Message{Role: "assistant", Content: text})
			// A developer item is the Responses spelling of a system turn, so
			// it joins the system prompt instead of becoming a user message.
			case "developer", "system":
				if len(images) > 0 {
					return systemImageError()
				}
				if text == "" {
					continue
				}
				if reason.system != "" {
					reason.system += "\n\n"
				}
				reason.system += text
			default:
				reason.turns = append(reason.turns, webprov.Message{Role: "user", Content: text, Images: images})
			}
		case "function_call":
			call := toolCall{ID: item.CallID, Type: "function"}
			if call.ID == "" {
				call.ID = "call_" + randomID()
			}
			call.Function.Name = item.Name
			call.Function.Arguments = item.Arguments
			if call.Function.Arguments == "" {
				call.Function.Arguments = "{}"
			}
			last := len(reason.turns) - 1
			if last >= 0 && reason.turns[last].Role == "assistant" {
				reason.turns[last].Content = renderCallList(reason.turns[last].Content, []toolCall{call})
				continue
			}
			reason.turns = append(reason.turns, webprov.Message{Role: "assistant", Content: renderCallList("", []toolCall{call})})
		case "function_call_output":
			// The blank line is what tells the model the call was answered;
			// without it it tends to call the same tool again.
			text, images, missing := messageParts(item.Output)
			if missing > 0 {
				return attachmentError()
			}
			reason.turns = append(reason.turns, webprov.Message{Role: "user", Content: "\n" + text, Images: images})
		}
	}
	return nil
}

// responsesShape renders OpenAI Responses events and JSON.
type responsesShape struct {
	w       http.ResponseWriter
	id      string
	model   string
	created int64
	item    int
	open    bool
	tooling bool
	started bool
	// text accumulates the streamed message so the completed object carries
	// what a client would otherwise have to rebuild from the deltas.
	message   strings.Builder
	toolCalls []toolCall
}

func (s *responsesShape) hello() {
	if s.started {
		return
	}
	s.started = true
	created, _ := json.Marshal(map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id": s.id, "object": "response", "created_at": s.created,
			"status": "in_progress", "model": s.model, "output": []any{},
		},
	})
	fmt.Fprintf(s.w, "event: response.created\ndata: %s\n\n", created)
}

func (s *responsesShape) part(text string) {
	payload, _ := json.Marshal(map[string]any{
		"type": "response.output_text.delta", "item_id": s.id,
		"output_index": s.item, "content_index": 0, "delta": text,
	})
	fmt.Fprintf(s.w, "event: response.output_text.delta\ndata: %s\n\n", payload)
}

func (s *responsesShape) reasoning(text string) {
	s.hello()
	payload, _ := json.Marshal(map[string]any{
		"type": "response.reasoning_summary_text.delta", "item_id": s.id,
		"output_index": s.item, "delta": text,
	})
	fmt.Fprintf(s.w, "event: response.reasoning_summary_text.delta\ndata: %s\n\n", payload)
}

func (s *responsesShape) text(text string) {
	s.hello()
	if s.open && s.tooling {
		s.open = false
	}
	if !s.open {
		s.open = true
		s.item++
		added, _ := json.Marshal(map[string]any{
			"type": "response.output_item.added", "output_index": s.item,
			"item": map[string]any{"type": "message", "id": s.id, "status": "in_progress", "role": "assistant", "content": []any{}},
		})
		fmt.Fprintf(s.w, "event: response.output_item.added\ndata: %s\n\n", added)
	}
	s.message.WriteString(text)
	s.part(text)
}

func (s *responsesShape) calls(calls []toolCall) {
	s.hello()
	s.toolCalls = append(s.toolCalls, calls...)
	for _, call := range calls {
		s.item++
		s.tooling = true
		added, _ := json.Marshal(map[string]any{
			"type": "response.output_item.added", "output_index": s.item,
			"item": map[string]any{"type": "function_call", "id": call.ID, "call_id": call.ID, "name": call.Function.Name, "arguments": ""},
		})
		fmt.Fprintf(s.w, "event: response.output_item.added\ndata: %s\n\n", added)
		delta, _ := json.Marshal(map[string]any{
			"type": "response.function_call_arguments.delta", "item_id": call.ID,
			"output_index": s.item, "delta": call.Function.Arguments,
		})
		fmt.Fprintf(s.w, "event: response.function_call_arguments.delta\ndata: %s\n\n", delta)
		// A client that finalises an item on its done event needs both.
		argsDone, _ := json.Marshal(map[string]any{
			"type": "response.function_call_arguments.done", "item_id": call.ID,
			"output_index": s.item, "arguments": call.Function.Arguments,
		})
		fmt.Fprintf(s.w, "event: response.function_call_arguments.done\ndata: %s\n\n", argsDone)
		closed, _ := json.Marshal(map[string]any{
			"type": "response.output_item.done", "output_index": s.item,
			"item": map[string]any{
				"type": "function_call", "id": call.ID, "call_id": call.ID,
				"name": call.Function.Name, "arguments": call.Function.Arguments,
			},
		})
		fmt.Fprintf(s.w, "event: response.output_item.done\ndata: %s\n\n", closed)
	}
}

func (s *responsesShape) end(_ string, usage map[string]any) {
	s.hello()
	output := make([]any, 0, 1+len(s.toolCalls))
	if s.open {
		s.open = false
		text := s.message.String()
		item := map[string]any{
			"type": "message", "id": s.id, "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": text}},
		}
		done, _ := json.Marshal(map[string]any{
			"type": "response.output_text.done", "item_id": s.id,
			"output_index": s.item, "content_index": 0, "text": text,
		})
		fmt.Fprintf(s.w, "event: response.output_text.done\ndata: %s\n\n", done)
		closed, _ := json.Marshal(map[string]any{
			"type": "response.output_item.done", "output_index": s.item, "item": item,
		})
		fmt.Fprintf(s.w, "event: response.output_item.done\ndata: %s\n\n", closed)
		output = append(output, item)
	}
	for _, call := range s.toolCalls {
		output = append(output, map[string]any{
			"type": "function_call", "id": call.ID, "call_id": call.ID,
			"name": call.Function.Name, "arguments": call.Function.Arguments,
		})
	}
	done, _ := json.Marshal(map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id": s.id, "object": "response", "created_at": s.created,
			"status": "completed", "model": s.model, "output": output,
			"usage": responsesUsage(usage),
		},
	})
	fmt.Fprintf(s.w, "event: response.completed\ndata: %s\n\n", done)
}

// responsesUsage maps the internal counters onto the Responses fields.
func responsesUsage(usage map[string]any) map[string]any {
	if usage == nil {
		return nil
	}
	input, _ := usage["prompt_tokens"].(int)
	output, _ := usage["completion_tokens"].(int)
	return map[string]any{
		"input_tokens": input, "output_tokens": output, "total_tokens": input + output,
		"input_tokens_details":  map[string]int{"cached_tokens": 0},
		"output_tokens_details": map[string]int{"reasoning_tokens": 0},
	}
}

func (s *responsesShape) reply(content, reasoning string, calls []toolCall, usage map[string]any, finish string) {
	output := make([]any, 0, 2+len(calls))
	if reasoning != "" {
		output = append(output, map[string]any{
			"type": "reasoning", "id": s.id, "status": "completed",
			"summary": []any{map[string]any{"type": "summary_text", "text": reasoning}},
		})
	}
	if content != "" {
		output = append(output, map[string]any{
			"type": "message", "id": s.id, "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": content}},
		})
	}
	for _, call := range calls {
		output = append(output, map[string]any{
			"type": "function_call", "id": call.ID, "call_id": call.ID,
			"name": call.Function.Name, "arguments": call.Function.Arguments,
		})
	}
	status := "completed"
	if finish == "length" {
		status = "incomplete"
	}
	writeJSON(s.w, http.StatusOK, map[string]any{
		"id": s.id, "object": "response", "created_at": s.created, "status": status,
		"model": s.model, "output": output,
		"usage": map[string]any{
			"input_tokens": usage["prompt_tokens"], "output_tokens": usage["completion_tokens"],
		},
	})
}
