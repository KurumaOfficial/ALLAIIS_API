// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

type toolSpec struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function toolArgument `json:"function"`
}

type toolArgument struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
}

type compatRequest struct {
	Model      string          `json:"model"`
	Messages   []chatMessage   `json:"messages"`
	Tools      []toolSpec      `json:"tools,omitempty"`
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
	Parallel   *bool           `json:"parallel_tool_calls,omitempty"`
}

type compatPayload struct {
	Model        string `json:"modelId"`
	SystemPrompt struct {
		Instructions string `json:"instructions"`
	} `json:"systemPrompt"`
	Messages            []turn `json:"messages"`
	IncludeSystemPrompt *bool  `json:"includeVeniceSystemPrompt,omitempty"`
	WebEnabled          *bool  `json:"webEnabled,omitempty"`
	WebScrapeEnabled    *bool  `json:"webScrapeEnabled,omitempty"`
	XSearchEnabled      *bool  `json:"xSearchEnabled,omitempty"`
}

type turn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// These names identify encodings in the offline compatibility fixtures.
type toolFormat string

const (
	formatGLM   toolFormat = "glm"
	formatJSON  toolFormat = "json"
	formatProxy toolFormat = "proxy"
	formatDSML  toolFormat = "dsml"
)

var toolFormats = []struct {
	model  *regexp.Regexp
	format toolFormat
}{
	{regexp.MustCompile(`glm`), formatGLM},
	{regexp.MustCompile(`deepseek`), formatDSML},
	{regexp.MustCompile(`qwen-?[0-9]+[^ ]*coder`), formatJSON},
	{regexp.MustCompile(`qwen-?(?:3[.-](?:[5-9]|[1-9][0-9]+)|(?:[4-9]|[1-9][0-9]+))(?:[.-]|$)`), formatProxy},
}

func modelToolFormat(model string) (toolFormat, error) {
	switch format := toolFormat(os.Getenv("ALLAIIS_TOOL_FORMAT")); format {
	case "":
	case formatGLM, formatJSON, formatProxy, formatDSML:
		return format, nil
	default:
		return "", fmt.Errorf("invalid ALLAIIS_TOOL_FORMAT %q", format)
	}
	for _, rule := range toolFormats {
		if rule.model.MatchString(strings.ToLower(model)) {
			return rule.format, nil
		}
	}
	return formatJSON, nil
}

type toolPolicy struct {
	format   toolFormat
	tools    []toolSpec
	choice   string
	named    string
	parallel bool
}

func prepareCompatibility(body []byte, model fixtureModel) (compatPayload, toolPolicy, int, error) {
	var request compatRequest
	clean, nul, err := cleanPayloadJSON(body)
	if err != nil || json.Unmarshal(clean, &request) != nil || !modelName.MatchString(request.Model) {
		return compatPayload{}, toolPolicy{}, nul, invalidInput("invalid_request", "Expected a model and text messages.")
	}
	policy, err := chooseTools(request, model)
	if err != nil {
		return compatPayload{}, toolPolicy{}, nul, err
	}
	payload := compatPayload{Model: request.Model}
	var instructions []string
	for _, message := range request.Messages {
		if message.Role == "system" || message.Role == "developer" {
			instructions = append(instructions, message.Content)
			continue
		}
		next, err := historyTurn(message, &nul)
		if err != nil {
			return compatPayload{}, toolPolicy{}, nul, err
		}
		payload.Messages = appendTurn(payload.Messages, next)
	}
	if len(policy.tools) > 0 {
		instructions = append(instructions, policy.instructions())
		disabled := false
		payload.IncludeSystemPrompt = &disabled
		payload.WebEnabled = &disabled
		payload.WebScrapeEnabled = &disabled
		payload.XSearchEnabled = &disabled
	}
	payload.SystemPrompt.Instructions = strings.Join(instructions, "\n\n")
	return payload, policy, nul, nil
}

func chooseTools(request compatRequest, model fixtureModel) (toolPolicy, error) {
	policy := toolPolicy{parallel: request.Parallel == nil || *request.Parallel, choice: "auto"}
	if len(request.ToolChoice) > 0 && string(request.ToolChoice) != "null" {
		var choice string
		if json.Unmarshal(request.ToolChoice, &choice) == nil {
			switch choice {
			case "none":
				return policy, nil
			case "auto", "required":
				policy.choice = choice
			default:
				return policy, invalidInput("invalid_tool_choice", "Invalid tool choice.")
			}
		} else {
			var named toolSpec
			if json.Unmarshal(request.ToolChoice, &named) != nil || named.Type != "function" || named.Function.Name == "" {
				return policy, invalidInput("invalid_tool_choice", "Expected a named function.")
			}
			policy.choice, policy.named = "named", named.Function.Name
		}
	}
	if len(request.Tools) == 0 && policy.choice == "auto" {
		return policy, nil
	}
	if !model.toolsSupported() {
		return policy, invalidInput("tools_not_supported", "This model does not support tools.")
	}
	seen := make(map[string]bool)
	for _, tool := range request.Tools {
		name := tool.Function.Name
		if tool.Type != "function" || !functionName.MatchString(name) || seen[name] {
			return policy, invalidInput("invalid_tool", "Tool names must be unique function names.")
		}
		seen[name] = true
		if policy.named == "" || policy.named == name {
			policy.tools = append(policy.tools, tool)
		}
	}
	if len(policy.tools) == 0 {
		return policy, invalidInput("invalid_tool_choice", "The requested tool is missing.")
	}
	var err error
	policy.format, err = modelToolFormat(request.Model)
	return policy, err
}

var (
	functionName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	modelName    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,159}$`)
)

func historyTurn(message chatMessage, nul *int) (turn, error) {
	content := message.Content
	switch message.Role {
	case "user":
	case "assistant":
		for _, call := range message.ToolCalls {
			args := json.RawMessage(call.Function.Arguments)
			var object map[string]json.RawMessage
			if json.Unmarshal(args, &object) != nil || object == nil {
				return turn{}, invalidInput("invalid_tool_history", "Historical tool arguments must be a JSON object.")
			}
			clean, removed, err := cleanPayloadJSON(args)
			if err != nil {
				return turn{}, invalidInput("invalid_tool_history", "Invalid historical tool arguments.")
			}
			*nul += removed
			envelope, _ := json.Marshal(map[string]any{
				"id": call.ID, "name": call.Function.Name, "arguments": json.RawMessage(clean),
			})
			if content != "" {
				content += "\n\n"
			}
			content += "<tool_call>" + string(envelope) + "</tool_call>"
		}
	case "tool":
		envelope, _ := json.Marshal(map[string]string{"tool_call_id": message.ToolCallID, "content": content})
		return turn{Role: "user", Content: "<tool_result>" + string(envelope) + "</tool_result>"}, nil
	default:
		return turn{}, invalidInput("invalid_role", "Unsupported message role.")
	}
	return turn{Role: message.Role, Content: content}, nil
}

func appendTurn(history []turn, next turn) []turn {
	if len(history) != 0 {
		last := &history[len(history)-1]
		if last.Role == next.Role && !(next.Role == "assistant" && (next.Content == "" || last.Content == "")) {
			last.Content += "\n\n" + next.Content
			return history
		}
	}
	return append(history, next)
}

func cleanPayloadJSON(body []byte) ([]byte, int, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, 0, err
	}
	if decoder.Decode(new(json.RawMessage)) != io.EOF {
		return nil, 0, fmt.Errorf("expected one JSON value")
	}
	nul := 0
	clean, err := stripNUL(value, &nul)
	if err != nil {
		return nil, nul, err
	}
	encoded, err := json.Marshal(clean)
	return encoded, nul, err
}

func stripNUL(value any, removed *int) (any, error) {
	switch value := value.(type) {
	case string:
		*removed += strings.Count(value, "\x00")
		return strings.ReplaceAll(value, "\x00", ""), nil
	case []any:
		for i, item := range value {
			clean, err := stripNUL(item, removed)
			if err != nil {
				return nil, err
			}
			value[i] = clean
		}
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, item := range value {
			*removed += strings.Count(key, "\x00")
			key = strings.ReplaceAll(key, "\x00", "")
			if _, exists := result[key]; exists {
				return nil, fmt.Errorf("NUL removal would merge JSON fields")
			}
			clean, err := stripNUL(item, removed)
			if err != nil {
				return nil, err
			}
			result[key] = clean
		}
		return result, nil
	}
	return value, nil
}

const trimmedMarker = "\n[... omitted to fit context ...]\n"

func trimHistory(payload compatPayload, percent int) compatPayload {
	payload.Messages = slices.Clone(payload.Messages)
	size := 0
	for _, message := range payload.Messages {
		size += len(message.Content)
	}
	target := size * percent / 100
	for size > target && len(payload.Messages) > 1 {
		size -= len(payload.Messages[0].Content)
		payload.Messages = payload.Messages[1:]
	}
	if len(payload.Messages) == 1 && size > target && target > len(trimmedMarker) {
		message := &payload.Messages[0]
		if !strings.Contains(message.Content, "<tool_result>") && !strings.Contains(message.Content, "<tool_call>") {
			keep := target - len(trimmedMarker)
			start := keep / 2
			end := len(message.Content) - (keep - start)
			for start > 0 && !utf8.RuneStart(message.Content[start]) {
				start--
			}
			for end < len(message.Content) && !utf8.RuneStart(message.Content[end]) {
				end++
			}
			message.Content = message.Content[:start] + trimmedMarker + message.Content[end:]
		}
	}
	return payload
}

type fixtureModel struct {
	ID          string `json:"id"`
	UsesCredits *bool  `json:"usesCredits"`
	Agent       bool   `json:"agent"`
	Context     int    `json:"context"`
	Output      int    `json:"output"`
}

func (m fixtureModel) toolsSupported() bool { return !m.Agent }

func offlineCatalog(models []fixtureModel) []fixtureModel {
	return slices.DeleteFunc(slices.Clone(models), func(model fixtureModel) bool {
		return model.ID == "" || model.UsesCredits == nil || *model.UsesCredits
	})
}
