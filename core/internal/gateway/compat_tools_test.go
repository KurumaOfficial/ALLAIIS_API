// SPDX-License-Identifier: GPL-3.0-only

package gateway_test

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// The fixture encodings are described here:
// https://github.com/RfadnjdExt/venice-ai-chat-proxy#tool-calling-emulated
func (policy toolPolicy) reminder() string {
	var example string
	switch policy.format {
	case formatGLM:
		example = "<tool_call>name<arg_key>key</arg_key><arg_value>value</arg_value></tool_call>"
	case formatProxy:
		example = `<proxy_call>{"name":"name","arguments":{"key":"value"}}</proxy_call>`
	case formatDSML:
		example = `<|DSML|calls><|DSML|invoke name="name"><|DSML|parameter name="key" string="true">value</|DSML|parameter></|DSML|invoke></|DSML|calls>`
	default:
		example = `<tool_call>{"name":"name","arguments":{"key":"value"}}</tool_call>`
	}
	return "For a tool call, use this format with every required argument: " + example
}

func (policy toolPolicy) instructions() string {
	data, _ := json.Marshal(policy.tools)
	prompt := "## Tools\n" + string(data) + "\n" + policy.reminder()
	switch policy.choice {
	case "auto":
		prompt += "\nCall a tool only if needed."
	case "required":
		prompt += "\nCall at least one listed tool."
	case "named":
		prompt += "\nCall the named tool: " + policy.named + "."
	}
	if !policy.parallel {
		prompt += "\nEmit at most one call."
	}
	return prompt
}

var (
	errInvalidTool  = errors.New("invalid tool call")
	toolOpen        = regexp.MustCompile(`<(?:tool_call|proxy_call)>|<[|｜]DSML[|｜]\s*(?:calls|function_calls|invoke)\b[^>]*>|<[|｜]tool[_▁]calls?[_▁]begin[|｜]>`)
	dsmlInvoke      = regexp.MustCompile(`<[|｜]DSML[|｜]\s*invoke\s+([^>]+)>`)
	dsmlParameter   = regexp.MustCompile(`<[|｜]DSML[|｜]\s*parameter\s+([^>]+)>`)
	dsmlCloseCalls  = regexp.MustCompile(`(?:</[|｜]DSML[|｜]\s*|<[|｜]/DSML[|｜]\s*)(?:calls|function_calls)>`)
	dsmlCloseInvoke = regexp.MustCompile(`(?:</[|｜]DSML[|｜]\s*|<[|｜]/DSML[|｜]\s*)invoke>`)
	dsmlCloseParam  = regexp.MustCompile(`(?:</[|｜]DSML[|｜]\s*|<[|｜]/DSML[|｜]\s*)parameter>`)
	dsmlCallEnd     = regexp.MustCompile(`<[|｜]tool[_▁]call[_▁]end[|｜]>`)
	dsmlCallsEnd    = regexp.MustCompile(`<[|｜]tool[_▁]calls[_▁]end[|｜]>`)
	dsmlSep         = regexp.MustCompile(`<[|｜]tool[_▁]sep[|｜]>`)
	dsmlAttr        = regexp.MustCompile(`([a-z]+)="([^"]*)"`)
)

type parsedCall struct {
	name string
	args map[string]any
}

func (policy toolPolicy) extract(text string, final bool) (string, []toolCall, error) {
	if len(policy.tools) == 0 {
		return text, nil, nil
	}
	loc := toolOpen.FindStringIndex(text)
	if loc == nil {
		if final && strings.HasSuffix(strings.TrimRight(text, " \t\r\n"), "<tool_call") {
			return "", nil, errInvalidTool
		}
		return text, nil, nil
	}
	content := text[:loc[0]]
	remaining := text[loc[0]:]
	var calls []toolCall
	for {
		loc = toolOpen.FindStringIndex(remaining)
		if loc == nil {
			return content, calls, nil
		}
		open := remaining[loc[0]:loc[1]]
		body := remaining[loc[1]:]
		parsed, tail, err := policy.parseEnvelope(open, body, final)
		if err != nil {
			return content, nil, err
		}
		if len(parsed) == 0 {
			return content, calls, nil
		}
		for _, call := range parsed {
			wire, err := policy.validateCall(call)
			if err != nil {
				return content, nil, err
			}
			calls = append(calls, wire)
			if !policy.parallel {
				return content, calls, nil
			}
		}
		remaining = tail
	}
}

func (policy toolPolicy) parseEnvelope(open, body string, final bool) ([]parsedCall, string, error) {
	switch open {
	case "<tool_call>", "<proxy_call>":
		end := "</" + open[1:]
		closeAt := strings.Index(body, end)
		// A closing tag inside a valid JSON string belongs to the argument.
		if closeAt >= 0 && strings.HasPrefix(strings.TrimSpace(body), "{") {
			decoder := json.NewDecoder(strings.NewReader(body))
			var object json.RawMessage
			if decoder.Decode(&object) == nil && decoder.InputOffset() > int64(closeAt) {
				offset := int(decoder.InputOffset())
				closeAt = strings.Index(body[offset:], end)
				if closeAt >= 0 {
					closeAt += offset
				}
			}
		}
		if closeAt < 0 && !final {
			return nil, "", nil
		}
		tail := ""
		if closeAt >= 0 {
			tail, body = body[closeAt+len(end):], body[:closeAt]
		}
		var call parsedCall
		var err error
		if open == "<tool_call>" && !strings.HasPrefix(strings.TrimSpace(body), "{") {
			call, err = policy.parseGLM(body)
		} else {
			call, err = parseToolJSON(body, final && closeAt < 0)
		}
		return []parsedCall{call}, tail, err
	}
	if strings.Contains(open, "DSML") {
		return parseDSML(open, body, final, policy.parallel)
	}
	return parseLegacyDSML(open, body, final, policy.parallel)
}

func (policy toolPolicy) parseGLM(body string) (parsedCall, error) {
	name, rest, hasArgs := strings.Cut(body, "<arg_key>")
	call := parsedCall{name: strings.TrimSpace(name), args: map[string]any{}}
	for hasArgs {
		key, tail, foundKey := strings.Cut(rest, "</arg_key>")
		raw, foundOpen := strings.CutPrefix(strings.TrimLeft(tail, " \t\r\n"), "<arg_value>")
		value, tail, foundClose := strings.Cut(raw, "</arg_value>")
		key = strings.TrimSpace(key)
		if !foundKey || !foundOpen || !foundClose || key == "" {
			return parsedCall{}, errInvalidTool
		}
		if _, duplicate := call.args[key]; duplicate {
			return parsedCall{}, errInvalidTool
		}
		if policy.argIsString(call.name, key) {
			call.args[key] = value
		} else {
			var decoded any
			if decodeJSON(value, &decoded) != nil {
				return parsedCall{}, errInvalidTool
			}
			call.args[key] = decoded
		}
		rest, hasArgs = strings.CutPrefix(strings.TrimLeft(tail, " \t\r\n"), "<arg_key>")
		if !hasArgs && strings.TrimSpace(rest) != "" {
			return parsedCall{}, errInvalidTool
		}
	}
	return call, nil
}

func (policy toolPolicy) argIsString(name, key string) bool {
	for _, tool := range policy.tools {
		if tool.Function.Name == name {
			properties, _ := tool.Function.Parameters["properties"].(map[string]any)
			property, _ := properties[key].(map[string]any)
			return property["type"] == "string"
		}
	}
	return false
}

func parseToolJSON(body string, truncated bool) (parsedCall, error) {
	var fields map[string]any
	if err := decodeJSON(body, &fields); err != nil || fields == nil {
		clean := repairToolJSON(body, truncated)
		if err := decodeJSON(clean, &fields); err != nil || fields == nil {
			return parsedCall{}, errInvalidTool
		}
	}
	name, _ := fields["name"].(string)
	if name == "" {
		return parsedCall{}, errInvalidTool
	}
	if raw, present := fields["arguments"]; present {
		switch args := raw.(type) {
		case map[string]any:
			return parsedCall{name: name, args: args}, nil
		case string:
			var object map[string]any
			if decodeJSON(args, &object) == nil && object != nil {
				return parsedCall{name: name, args: object}, nil
			}
		}
		return parsedCall{}, errInvalidTool
	}
	delete(fields, "name")
	return parsedCall{name: name, args: fields}, nil
}

// Repair common model output mistakes without completing an unfinished string value.
func repairToolJSON(input string, truncated bool) string {
	var out strings.Builder
	var stack []byte
	inString := false
	for i := 0; i < len(input); i++ {
		ch := input[i]
		if !inString {
			switch ch {
			case '"':
				inString = true
			case '{':
				stack = append(stack, '}')
			case '[':
				stack = append(stack, ']')
			case '}', ']':
				if len(stack) == 0 || stack[len(stack)-1] != ch {
					return input
				}
				stack = stack[:len(stack)-1]
			}
			out.WriteByte(ch)
			continue
		}
		switch ch {
		case '\\':
			if i+1 < len(input) && strings.ContainsRune(`"\/bfnrtu`, rune(input[i+1])) {
				out.WriteString(input[i : i+2])
				i++
			} else {
				out.WriteString(`\\`)
			}
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		case '"':
			following := strings.TrimLeft(input[i+1:], " \t\r\n")
			if following == "" || strings.ContainsRune(":,}]", rune(following[0])) {
				inString = false
			} else {
				out.WriteByte('\\')
			}
			out.WriteByte(ch)
		default:
			out.WriteByte(ch)
		}
	}
	if truncated && !inString {
		for i := len(stack) - 1; i >= 0; i-- {
			out.WriteByte(stack[i])
		}
	}
	return out.String()
}

func parseDSML(open, body string, final, parallel bool) ([]parsedCall, string, error) {
	closed := dsmlCloseCalls
	if dsmlInvoke.MatchString(open) {
		closed = dsmlCloseInvoke
	}
	end := closed.FindStringIndex(body)
	if end == nil && !final {
		// A single completed invocation can satisfy a non-parallel request.
		if parallel || !dsmlCloseInvoke.MatchString(body) {
			return nil, "", nil
		}
	}
	tail := ""
	if end != nil {
		tail, body = body[end[1]:], body[:end[0]]
	}
	if dsmlInvoke.MatchString(open) {
		body = open + body
	}
	var calls []parsedCall
	for {
		invoke := dsmlInvoke.FindStringSubmatchIndex(body)
		if invoke == nil {
			break
		}
		attrs := attributes(body[invoke[2]:invoke[3]])
		body = body[invoke[1]:]
		params := body
		if stop := dsmlCloseInvoke.FindStringIndex(body); stop != nil {
			params, body = body[:stop[0]], body[stop[1]:]
		} else {
			body = ""
		}
		values := map[string]any{}
		for strings.TrimSpace(params) != "" {
			params = strings.TrimLeft(params, " \t\r\n")
			param := dsmlParameter.FindStringSubmatchIndex(params)
			if param == nil || param[0] != 0 {
				return nil, "", errInvalidTool
			}
			arg := attributes(params[param[2]:param[3]])
			key := arg["name"]
			params = params[param[1]:]
			stop := dsmlCloseParam.FindStringIndex(params)
			if _, duplicate := values[key]; duplicate || key == "" || stop == nil {
				return nil, "", errInvalidTool
			}
			value := params[:stop[0]]
			if arg["string"] == "true" {
				values[key] = value
			} else {
				var decoded any
				if decodeJSON(value, &decoded) != nil {
					return nil, "", errInvalidTool
				}
				values[key] = decoded
			}
			params = params[stop[1]:]
		}
		calls = append(calls, parsedCall{name: attrs["name"], args: values})
		if !parallel {
			break
		}
	}
	if len(calls) == 0 {
		return nil, "", errInvalidTool
	}
	return calls, tail, nil
}

func attributes(tag string) map[string]string {
	result := make(map[string]string)
	for _, match := range dsmlAttr.FindAllStringSubmatch(tag, -1) {
		result[match[1]] = match[2]
	}
	return result
}

func parseLegacyDSML(open, body string, final, parallel bool) ([]parsedCall, string, error) {
	if strings.Contains(open, "calls") {
		end := dsmlCallsEnd.FindStringIndex(body)
		if end == nil && !final && parallel {
			return nil, "", nil
		}
		var calls []parsedCall
		tail := ""
		if end != nil {
			tail, body = body[end[1]:], body[:end[0]]
		}
		for loc := toolOpen.FindStringIndex(body); loc != nil; loc = toolOpen.FindStringIndex(body) {
			child := body[loc[0]:loc[1]]
			if strings.Contains(child, "calls") || !strings.Contains(child, "begin") {
				return nil, "", errInvalidTool
			}
			parsed, more, err := parseLegacyDSML(child, body[loc[1]:], final || end != nil, parallel)
			if err != nil || len(parsed) == 0 {
				return nil, "", err
			}
			calls = append(calls, parsed...)
			body = more
			if !parallel {
				break
			}
		}
		if len(calls) == 0 && (final || end != nil) {
			return nil, "", errInvalidTool
		}
		return calls, tail, nil
	}
	end := dsmlCallEnd.FindStringIndex(body)
	if end == nil && !final {
		return nil, "", nil
	}
	tail := ""
	if end != nil {
		tail, body = body[end[1]:], body[:end[0]]
	}
	sep := dsmlSep.FindStringIndex(body)
	if sep == nil || strings.TrimSpace(body[:sep[0]]) != "function" {
		return nil, "", errInvalidTool
	}
	nameAndJSON := strings.TrimSpace(body[sep[1]:])
	start := strings.IndexAny(nameAndJSON, " \r\n\t{`")
	if start < 0 {
		return nil, "", errInvalidTool
	}
	name, args := nameAndJSON[:start], strings.TrimSpace(nameAndJSON[start:])
	args = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(args, "```json"), "```"))
	var object map[string]any
	if decodeJSON(repairToolJSON(args, final && end == nil), &object) != nil || object == nil {
		return nil, "", errInvalidTool
	}
	return []parsedCall{{name: name, args: object}}, tail, nil
}

func (policy toolPolicy) validateCall(call parsedCall) (toolCall, error) {
	for _, tool := range policy.tools {
		if tool.Function.Name != call.name {
			continue
		}
		required, _ := tool.Function.Parameters["required"].([]any)
		for _, name := range required {
			key, ok := name.(string)
			if value, found := call.args[key]; !ok || !found || value == nil {
				return toolCall{}, errInvalidTool
			}
		}
		data, err := json.Marshal(call.args)
		if err != nil {
			return toolCall{}, errInvalidTool
		}
		return toolCall{ID: "call_" + rand.Text(), Type: "function",
			Function: toolArgument{Name: call.name, Arguments: string(data)}}, nil
	}
	return toolCall{}, errInvalidTool
}

func decodeJSON(text string, value any) error {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}
