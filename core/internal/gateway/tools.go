// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

// Web conversations have no native tool parameter, so a sentinel envelope and a
// streaming guard buffer emulate tool calls. The client never sees the marker.
const (
	toolOpen       = "<<<ALLAIIS_TOOL_CALL>>>"
	toolClose      = "<<<END_TOOL_CALL>>>"
	toolKeep       = 64
	toolRepairNote = "System validation error: the tool block was not valid JSON or missed required arguments. " +
		"Output ONLY a valid block starting with " + toolOpen + " and ending with " + toolClose + "."
)

var errNoToolBlock = errors.New("no tool block")

type webprovMessage = webprov.Message

type toolSpec = struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// toolEnvelope appends the call format to the system prompt.
func toolEnvelope(system string, tools []toolSpec) string {
	var names strings.Builder
	for i, tool := range tools {
		if i > 0 {
			names.WriteString(", ")
		}
		names.WriteString(tool.Function.Name)
	}
	var spec strings.Builder
	for _, tool := range tools {
		parameters := string(tool.Function.Parameters)
		if parameters == "" {
			parameters = `{"type":"object","properties":{}}`
		}
		spec.WriteString("- " + tool.Function.Name + ": " + tool.Function.Description + "\n  arguments schema: " + parameters + "\n")
	}
	return system + "\n\n## Tools\n" +
		"To call a tool, output ONLY one block that starts with " + toolOpen + " and ends with " + toolClose +
		", containing valid JSON of the shape {\"calls\":[{\"name\":\"...\",\"arguments\":{...}}]}." +
		" Call at least one of: " + names.String() + ". Do not wrap the block in Markdown.\n" + spec.String()
}

// toolGuard splits a text stream into visible text and parsed tool blocks.
type toolGuard struct {
	tools []toolSpec
	buf   strings.Builder
}

func (g *toolGuard) add(tools []toolSpec) { g.tools = tools }

// Write consumes a chunk and returns text that is safe to emit. A parsed block
// is returned through calls. A block that is still arriving stays buffered.
func (g *toolGuard) Write(chunk string) (text string, calls []toolCall, err error) {
	g.buf.WriteString(chunk)
	for {
		current := g.buf.String()
		start := strings.Index(current, toolOpen)
		if start < 0 {
			// Keep a possible marker prefix at the tail buffered.
			keep := 0
			for n := min(len(toolOpen)-1, len(current)); n > 0; n-- {
				if strings.HasSuffix(current, toolOpen[:n]) {
					keep = n
					break
				}
			}
			if len(current)-keep <= 0 {
				return "", nil, nil
			}
			flush := current[:len(current)-keep]
			g.buf.Reset()
			g.buf.WriteString(current[len(current)-keep:])
			return flush, nil, nil
		}
		head := current[:start]
		rest := current[start+len(toolOpen):]
		end := strings.Index(rest, toolClose)
		if end < 0 {
			g.buf.Reset()
			g.buf.WriteString(current[start:])
			return head, nil, nil
		}
		parsed, err := g.parse(rest[:end])
		g.buf.Reset()
		g.buf.WriteString(rest[end+len(toolClose):])
		if err != nil {
			return head, nil, err
		}
		return head, parsed, nil
	}
}

// Flush emits the remaining buffer, parsing a final block if present.
func (g *toolGuard) Flush() (string, []toolCall, error) {
	current := g.buf.String()
	g.buf.Reset()
	start := strings.Index(current, toolOpen)
	if start < 0 {
		return current, nil, nil
	}
	rest := strings.TrimSuffix(current[start+len(toolOpen):], toolClose)
	calls, err := g.parse(rest)
	return current[:start], calls, err
}

func (g *toolGuard) parse(block string) ([]toolCall, error) {
	block = strings.TrimSpace(block)
	block = strings.TrimPrefix(block, "```json")
	block = strings.TrimPrefix(block, "```")
	block = strings.TrimSuffix(block, "```")
	var envelope struct {
		Calls []struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"calls"`
	}
	if err := json.Unmarshal([]byte(block), &envelope); err != nil || len(envelope.Calls) == 0 {
		return nil, errNoToolBlock
	}
	calls := make([]toolCall, 0, len(envelope.Calls))
	for _, candidate := range envelope.Calls {
		known := false
		for _, tool := range g.tools {
			if tool.Function.Name == candidate.Name {
				known = true
				break
			}
		}
		if !known {
			return nil, errNoToolBlock
		}
		arguments := string(candidate.Arguments)
		if arguments == "" || arguments == "null" {
			return nil, errNoToolBlock
		}
		var object map[string]any
		if json.Unmarshal([]byte(arguments), &object) != nil {
			return nil, errNoToolBlock
		}
		call := toolCall{ID: "call_" + randomID(), Type: "function"}
		call.Function.Name = candidate.Name
		call.Function.Arguments = arguments
		calls = append(calls, call)
	}
	return calls, nil
}

// repairMessages extends the conversation with the failed attempt and a fix-up.
func repairMessages(messages []webprovMessage, raw string) []webprovMessage {
	fixed := append([]webprovMessage{}, messages...)
	fixed = append(fixed,
		webprovMessage{Role: "assistant", Content: raw},
		webprovMessage{Role: "user", Content: toolRepairNote})
	return fixed
}
