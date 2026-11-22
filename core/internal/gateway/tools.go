// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"regexp"
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
		"To call a tool, output ONLY a block that starts with " + toolOpen + " and ends with " + toolClose +
		", containing valid JSON of the shape {\"calls\":[{\"name\":\"...\",\"arguments\":{...}}]}." +
		" Call at least one of: " + names.String() + ". Do not wrap the block in Markdown.\n" +
		"Claude-style XML is also accepted:\n" +
		"<invoke name=\"tool\"><parameter name=\"argument\">value</parameter></invoke>\n" + spec.String()
}

// toolGuard splits a text stream into visible text and parsed tool blocks.
type toolGuard struct {
	tools []toolSpec
	buf   strings.Builder
	last  *toolCall
}

// unique drops a call that repeats the previous one, which happens when a model
// emits a sentinel block and the XML form for the same call.
func (g *toolGuard) unique(calls []toolCall) []toolCall {
	kept := make([]toolCall, 0, len(calls))
	for _, call := range calls {
		if g.last != nil && call.Function.Name == g.last.Function.Name &&
			call.Function.Arguments == g.last.Function.Arguments {
			continue
		}
		copy := call
		g.last = &copy
		kept = append(kept, call)
	}
	return kept
}

func (g *toolGuard) add(tools []toolSpec) { g.tools = tools }

// reset drops buffered and dedupe state before a retry.
func (g *toolGuard) reset() {
	g.buf.Reset()
	g.last = nil
}

// known reports whether name was offered in this request.
func (g *toolGuard) known(name string) bool {
	for _, tool := range g.tools {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}

func (g *toolGuard) knownCalls(calls []toolCall) bool {
	for _, call := range calls {
		if !g.known(call.Function.Name) {
			return false
		}
	}
	return true
}

// Write consumes a chunk and returns text that is safe to emit. A parsed block
// is returned through calls. A block that is still arriving stays buffered.
func (g *toolGuard) Write(chunk string) (text string, calls []toolCall, err error) {
	g.buf.WriteString(chunk)
	for {
		current := g.buf.String()
		start := strings.Index(current, toolOpen)
		if start < 0 {
			if at := dsmlLeadingClose(current); at >= 0 {
				loc := dsmlEnd.FindStringSubmatchIndex(current[at:])
				if loc == nil {
					g.buf.Reset()
					g.buf.WriteString(current[at:])
					return current[:at], nil, nil
				}
				g.buf.Reset()
				g.buf.WriteString(current[at+loc[1]:])
				continue
			}
			if dsmlStart := dsmlIndex(current); dsmlStart >= 0 {
				head, calls, used, complete, err := consumeDSML(current[dsmlStart:])
				if err != nil {
					g.buf.Reset()
					g.buf.WriteString(current[dsmlStart:])
					return current[:dsmlStart], nil, err
				}
				if !complete || !g.knownCalls(calls) {
					g.buf.Reset()
					g.buf.WriteString(current[dsmlStart:])
					return current[:dsmlStart], nil, nil
				}
				calls = g.unique(calls)
				rest := current[dsmlStart+used:]
				g.buf.Reset()
				g.buf.WriteString(rest)
				return current[:dsmlStart] + head, calls, nil
			}
			if xmlStart := invokeIndex(current); xmlStart >= 0 {
				head, calls, used, complete, err := consumeInvokes(current[xmlStart:])
				if err != nil {
					g.buf.Reset()
					g.buf.WriteString(current[xmlStart:])
					return current[:xmlStart], nil, err
				}
				if !complete {
					g.buf.Reset()
					g.buf.WriteString(current[xmlStart:])
					return current[:xmlStart], nil, nil
				}
				if !g.knownCalls(calls) {
					g.buf.Reset()
					g.buf.WriteString(current[xmlStart:])
					return current[:xmlStart], nil, errNoToolBlock
				}
				calls = g.unique(calls)
				rest := current[xmlStart+used:]
				g.buf.Reset()
				g.buf.WriteString(rest)
				return current[:xmlStart] + head, calls, nil
			}
			// Keep a possible marker or tag prefix at the tail buffered.
			keep := markerKeep(current)
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
		return head, g.unique(parsed), nil
	}
}

// Flush emits the remaining buffer, parsing a final block if present.
func (g *toolGuard) Flush() (string, []toolCall, error) {
	current := g.buf.String()
	g.buf.Reset()
	for {
		at := dsmlLeadingClose(current)
		if at < 0 {
			break
		}
		loc := dsmlEnd.FindStringSubmatchIndex(current[at:])
		if loc == nil {
			current = current[:at]
			break
		}
		current = current[:at] + current[at+loc[1]:]
	}
	start := strings.Index(current, toolOpen)
	if start < 0 {
		if dsmlStart := dsmlIndex(current); dsmlStart >= 0 {
			head, calls, _, _, err := consumeDSML(current[dsmlStart:])
			if err == nil && len(calls) > 0 && !g.knownCalls(calls) {
				err = errNoToolBlock
			}
			if err == nil {
				calls = g.unique(calls)
			}
			return current[:dsmlStart] + head, calls, err
		}
		if xmlStart := invokeIndex(current); xmlStart >= 0 {
			head, calls, _, _, err := consumeInvokes(current[xmlStart:])
			if err == nil && len(calls) > 0 && !g.knownCalls(calls) {
				err = errNoToolBlock
			}
			if err == nil {
				calls = g.unique(calls)
			}
			return current[:xmlStart] + head, calls, err
		}
		return current, nil, nil
	}
	rest := trimPartialClose(current[start+len(toolOpen):], toolClose)
	calls, err := g.parse(rest)
	if err == nil {
		calls = g.unique(calls)
	}
	return current[:start], calls, err
}

var invokeMarkers = []string{"<invoke", "<function_calls"}

// DeepSeek emits its native DSML tool syntax as text. The bar can be ASCII or
// full width and the closing tag may be written two ways.
var (
	dsmlPrefix = regexp.MustCompile(`<[|｜]DSML[|｜]\s*(?:calls|function_calls|invoke)\b`)
	dsmlCalls  = regexp.MustCompile(`<[|｜]DSML[|｜]\s*(?:calls|function_calls)\b[^>]*>`)
	dsmlInvoke = regexp.MustCompile(`<[|｜]DSML[|｜]\s*invoke\s+([^>]+)>`)
	dsmlParam  = regexp.MustCompile(`<[|｜]DSML[|｜]\s*parameter\s+([^>]+)>`)
	dsmlEnd    = regexp.MustCompile(`(?:</[|｜]DSML[|｜]\s*|<[|｜]/DSML[|｜]\s*)(calls|function_calls|invoke|parameter)\s*>`)
)

// dsmlIndex reports the first DSML block start, envelope or bare invoke.
func dsmlIndex(current string) int {
	if loc := dsmlPrefix.FindStringIndex(current); loc != nil {
		return loc[0]
	}
	return -1
}

// dsmlTailKeep reports how many bytes of an unterminated DSML token to keep.
func dsmlTailKeep(current string) int {
	for _, marker := range []string{"<|/DSML|", "<｜/DSML｜", "</|DSML|", "</｜DSML｜", "<|DSML|", "<｜DSML｜"} {
		index := strings.LastIndex(current, marker)
		if index >= 0 && strings.IndexByte(current[index:], '>') < 0 {
			return len(current) - index
		}
	}
	return 0
}

// consumeDSML parses one calls envelope, or a run of bare invokes, into calls.
func consumeDSML(fragment string) (head string, calls []toolCall, used int, complete bool, err error) {
	offset := 0
	envelope := false
	if loc := dsmlCalls.FindStringIndex(fragment); loc != nil && loc[0] == 0 {
		offset, envelope = loc[1], true
	}
	var prose strings.Builder
	for {
		match := dsmlInvoke.FindStringSubmatchIndex(fragment[offset:])
		if match == nil {
			break
		}
		start := offset + match[0]
		prose.WriteString(fragment[offset:start])
		name := parameterName(fragment[offset+match[2] : offset+match[3]])
		body := fragment[offset+match[1]:]
		from, to := dsmlCloseAt(body, "invoke")
		if from < 0 {
			return prose.String(), calls, start, false, nil
		}
		values, consumed, paramErr := dsmlValues(body[:from])
		if paramErr != nil {
			return prose.String(), nil, start, true, paramErr
		}
		call, callErr := buildCallFromValues(name, values)
		if callErr != nil {
			return prose.String(), nil, start, true, callErr
		}
		calls = append(calls, call)
		offset += match[1] + to
		_ = consumed
	}
	if envelope {
		if from, to := dsmlCloseAt(fragment[offset:], "calls"); from >= 0 && strings.TrimSpace(fragment[offset:offset+from]) == "" {
			offset += to
		} else if len(calls) == 0 {
			return prose.String(), calls, offset, false, nil
		}
	}
	return prose.String(), calls, offset, true, nil
}

// dsmlLeadingClose reports a closing DSML tag at the start, past whitespace.
func dsmlLeadingClose(current string) int {
	trimmed := strings.TrimLeft(current, " \t\r\n")
	if trimmed == "" {
		return -1
	}
	for _, marker := range []string{"</|DSML|", "</｜DSML｜", "<|/DSML|", "<｜/DSML｜"} {
		if strings.HasPrefix(trimmed, marker) {
			return len(current) - len(trimmed)
		}
	}
	return -1
}

// dsmlCloseAt finds the next closing tag with the wanted name.
func dsmlCloseAt(body, want string) (start, end int) {
	for offset := 0; offset < len(body); {
		loc := dsmlEnd.FindStringSubmatchIndex(body[offset:])
		if loc == nil {
			return -1, -1
		}
		if body[offset+loc[2]:offset+loc[3]] == want {
			return offset + loc[0], offset + loc[1]
		}
		offset += loc[1]
	}
	return -1, -1
}

// dsmlValues reads parameter elements; string="true" keeps the body verbatim.
func dsmlValues(body string) (map[string]any, int, error) {
	values := map[string]any{}
	for {
		match := dsmlParam.FindStringSubmatchIndex(body)
		if match == nil {
			return values, len(body), nil
		}
		tag := body[match[2]:match[3]]
		name := body[match[0]:match[1]]
		body = body[match[1]:]
		key := parameterName(tag)
		from, to := dsmlCloseAt(body, "parameter")
		if key == "" || from < 0 {
			return nil, 0, errNoToolBlock
		}
		value := body[:from]
		if strings.Contains(name, `string="true"`) {
			values[key] = value
		} else {
			var decoded any
			if json.Unmarshal([]byte(strings.TrimSpace(value)), &decoded) != nil {
				decoded = value
			}
			values[key] = decoded
		}
		body = body[to:]
	}
}

// buildCallFromValues marshals typed arguments into a tool call.
func buildCallFromValues(name string, values map[string]any) (toolCall, error) {
	if name == "" {
		return toolCall{}, errNoToolBlock
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return toolCall{}, errNoToolBlock
	}
	call := toolCall{ID: "call_" + randomID(), Type: "function"}
	call.Function.Name = name
	call.Function.Arguments = string(encoded)
	return call, nil
}

// markerKeep returns the length of a trailing partial marker that must stay
// buffered.
func markerKeep(current string) int {
	keep := 0
	markers := append([]string{toolOpen}, invokeMarkers...)
	markers = append(markers, "<|DSML|", "<|/DSML|", "</|DSML|")
	for _, marker := range markers {
		for n := min(len(marker)-1, len(current)); n > keep; n-- {
			if strings.HasSuffix(current, marker[:n]) {
				keep = n
				break
			}
		}
	}
	keep = max(keep, dsmlTailKeep(current))
	return keep
}

func invokeIndex(current string) int {
	found := -1
	for _, marker := range invokeMarkers {
		if index := strings.Index(current, marker); index >= 0 && (found < 0 || index < found) {
			found = index
		}
	}
	return found
}

// consumeInvokes parses one or more complete XML invoke blocks. It reports how
// many bytes were consumed and whether the tail was complete.
func consumeInvokes(fragment string) (head string, calls []toolCall, used int, complete bool, err error) {
	var prose strings.Builder
	offset := 0
	if strings.HasPrefix(fragment, "<function_calls>") {
		offset = len("<function_calls>")
	}
	for {
		index := strings.Index(fragment[offset:], "<invoke")
		if index < 0 {
			break
		}
		start := offset + index
		prose.WriteString(fragment[offset:start])
		open := strings.Index(fragment[start:], ">")
		if open < 0 {
			return prose.String(), calls, start, false, nil
		}
		open += start
		if open > start && fragment[open-1] == '/' {
			call, err := buildCall(invokeName(fragment[start:open]))
			if err != nil {
				return prose.String(), nil, start, true, err
			}
			calls = append(calls, call)
			offset = open + 1
			continue
		}
		end := strings.Index(fragment[open:], "</invoke>")
		if end < 0 {
			return prose.String(), calls, start, false, nil
		}
		end += open
		call, err := buildCallFromBody(invokeName(fragment[start:open]), fragment[open+1:end])
		if err != nil {
			return prose.String(), nil, start, true, err
		}
		calls = append(calls, call)
		offset = end + len("</invoke>")
	}
	if strings.HasPrefix(fragment[offset:], "</function_calls>") {
		offset += len("</function_calls>")
	}
	return prose.String(), calls, offset, true, nil
}

// trimPartialClose removes a closing marker that was cut off mid-stream.
func trimPartialClose(block, marker string) string {
	block = strings.TrimSuffix(block, marker)
	for n := len(marker) - 1; n > 0; n-- {
		if strings.HasSuffix(block, marker[:n]) {
			return block[:len(block)-n]
		}
	}
	return block
}

// escapeControl escapes raw line breaks inside JSON strings, which models emit
// instead of \n in long tool arguments.
func escapeControl(raw string) string {
	var out strings.Builder
	out.Grow(len(raw))
	inString, escaped := false, false
	for _, r := range raw {
		if escaped {
			out.WriteRune(r)
			escaped = false
			continue
		}
		switch r {
		case '\\':
			out.WriteRune(r)
			escaped = true
		case '"':
			inString = !inString
			out.WriteRune(r)
		case '\n':
			if inString {
				out.WriteString(`\n`)
			} else {
				out.WriteRune(r)
			}
		case '\r':
			if inString {
				out.WriteString(`\r`)
			} else {
				out.WriteRune(r)
			}
		case '\t':
			if inString {
				out.WriteString(`\t`)
			} else {
				out.WriteRune(r)
			}
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// invokeName reads the name attribute from an invoke open tag.
func invokeName(tag string) string {
	for _, quote := range []byte{'"', '\''} {
		marker := []byte("name=" + string(quote))
		if index := strings.Index(tag, string(marker)); index >= 0 {
			rest := tag[index+len(marker):]
			if stop := strings.IndexByte(rest, quote); stop >= 0 {
				return rest[:stop]
			}
		}
	}
	return ""
}

func buildCall(name string) (toolCall, error) { return buildCallFromBody(name, "") }

// buildCallFromBody turns parameter elements, or a raw JSON body, into arguments.
func buildCallFromBody(name, body string) (toolCall, error) {
	if name == "" {
		return toolCall{}, errNoToolBlock
	}
	trimmed := strings.TrimSpace(body)
	arguments := ""
	if strings.HasPrefix(trimmed, "{") {
		arguments = trimmed
	} else {
		object := map[string]any{}
		for {
			index := strings.Index(body, "<parameter")
			if index < 0 {
				break
			}
			head := body[index+len("<parameter"):]
			open := strings.Index(head, ">")
			if open < 0 {
				break
			}
			tag, after := head[:open], head[open+1:]
			parameter := parameterName(tag)
			if parameter == "" {
				break
			}
			if strings.HasSuffix(tag, "/") {
				object[parameter] = ""
				body = after
				continue
			}
			end := strings.Index(after, "</parameter>")
			if end < 0 {
				object[parameter] = strings.TrimSpace(after)
				body = ""
				break
			}
			object[parameter] = parameterValue(after[:end])
			body = after[end+len("</parameter>"):]
		}
		encoded, err := json.Marshal(object)
		if err != nil {
			return toolCall{}, errNoToolBlock
		}
		arguments = string(encoded)
	}
	var object map[string]any
	if json.Unmarshal([]byte(arguments), &object) != nil {
		return toolCall{}, errNoToolBlock
	}
	call := toolCall{ID: "call_" + randomID(), Type: "function"}
	call.Function.Name = name
	call.Function.Arguments = arguments
	return call, nil
}

func parameterName(tag string) string {
	for _, quote := range []byte{'"', '\''} {
		marker := []byte("name=" + string(quote))
		if index := strings.Index(tag, string(marker)); index >= 0 {
			rest := tag[index+len(marker):]
			if stop := strings.IndexByte(rest, quote); stop >= 0 {
				return rest[:stop]
			}
		}
	}
	return ""
}

// parameterValue keeps JSON and scalars typed, and collects repeated nested
// elements into an array.
func parameterValue(raw string) any {
	trimmed := strings.TrimSpace(raw)
	if json.Valid([]byte(trimmed)) {
		var value any
		if json.Unmarshal([]byte(trimmed), &value) == nil {
			return value
		}
	}
	if scalar, ok := typedScalar(trimmed); ok {
		return scalar
	}
	if !strings.Contains(trimmed, "<") {
		return raw
	}
	var fragments []any
	var text strings.Builder
	nested := false
	decoder := xml.NewDecoder(strings.NewReader("<value>" + raw + "</value>"))
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch value := token.(type) {
		case xml.StartElement:
			if value.Name.Local != "value" {
				nested = true
			}
		case xml.EndElement:
			if value.Name.Local != "value" && text.Len() > 0 {
				fragments = append(fragments, typedOrText(text.String()))
				text.Reset()
			}
		case xml.CharData:
			text.Write(value)
		}
	}
	if text.Len() > 0 {
		fragments = append(fragments, typedOrText(text.String()))
	}
	if !nested && len(fragments) == 1 {
		return fragments[0]
	}
	return fragments
}

func typedOrText(raw string) any {
	trimmed := strings.TrimSpace(raw)
	if scalar, ok := typedScalar(trimmed); ok {
		return scalar
	}
	return raw
}

// typedScalar parses a bare number, boolean or null.
func typedScalar(trimmed string) (any, bool) {
	switch trimmed {
	case "true":
		return true, true
	case "false":
		return false, true
	case "null":
		return nil, true
	}
	if trimmed == "" {
		return nil, false
	}
	digits := 0
	for index, r := range trimmed {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '-' && index == 0:
		case r == '.' || r == 'e' || r == 'E' || r == '+':
		default:
			return nil, false
		}
	}
	if digits == 0 {
		return nil, false
	}
	var value any
	if json.Unmarshal([]byte(trimmed), &value) != nil {
		return nil, false
	}
	return value, true
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
		block = escapeControl(block)
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
