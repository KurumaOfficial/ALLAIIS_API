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
		Tools    []struct {
			Type       string          `json:"type"`
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"tools"`
		ToolChoice    any   `json:"tool_choice"`
		ParallelCalls *bool `json:"parallel_tool_calls"`
	}
	if err := json.Unmarshal(payload, &input); err != nil {
		return webprov.Request{}, nil, nil, errors.New("expected a JSON object")
	}
	request := webprov.Request{
		RequestID: randomID(), Model: model,
		Temperature: input.Temperature, TopP: input.TopP, MaxTokens: input.MaxTokens,
		StopSequences: stopList(input.Stop),
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
	if name, schema, wanted := structuredFormat(format.Type, format.JSONSchema, format.Name); wanted {
		reason.jsonMode(name, schema)
	}
	tools := make([]toolSpec, 0, len(input.Tools))
	for _, tool := range input.Tools {
		if tool.Type != "" && tool.Type != "function" {
			continue
		}
		if tool.Name == "" {
			continue
		}
		spec := toolSpec{Type: "function"}
		spec.Function.Name = tool.Name
		spec.Function.Parameters = tool.Parameters
		tools = append(tools, spec)
	}
	if choice, ok := input.ToolChoice.(string); ok && choice == "none" {
		tools = nil
	}
	if len(tools) > 0 {
		reason.note(toolChoiceNote(input.ToolChoice, input.ParallelCalls))
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
			text, images := messageParts(item.Content)
			switch item.Role {
			case "assistant":
				if text == "" {
					text = "(empty)"
				}
				reason.turns = append(reason.turns, webprov.Message{Role: "assistant", Content: text})
			// A developer item is the Responses spelling of a system turn, so
			// it joins the system prompt instead of becoming a user message.
			case "developer", "system":
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
			text, _ := messageParts(item.Output)
			reason.turns = append(reason.turns, webprov.Message{Role: "user", Content: "\n" + text})
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
	s.part(text)
}

func (s *responsesShape) calls(calls []toolCall) {
	s.hello()
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
	}
}

func (s *responsesShape) end(_ string, usage map[string]any) {
	s.hello()
	done, _ := json.Marshal(map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id": s.id, "object": "response", "created_at": s.created,
			"status": "completed", "model": s.model, "output": []any{},
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
