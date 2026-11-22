// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

// Web conversations have no native tool parameter, so a sentinel envelope and a
// streaming guard buffer emulate tool calls. The client never sees the marker.
const (
	toolOpen      = "<<<ALLAIIS_TOOL_CALL>>>"
	toolClose     = "<<<END_TOOL_CALL>>>"
	toolKeep      = 64
	maxToolBuffer = 512 << 10
	// toolOverlap is the longest marker prefix search look-back.
	toolOverlap    = 24
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
//
// OpenCode's Code Mode tells the model that the tools can only be reached from
// its `execute` sandbox, while it still declares them. A model reading only that
// section spends turns searching. The header says both routes exist and lists the
// argument shape of each tool, so no discovery is needed.
func toolEnvelope(system string, tools []toolSpec) string {
	var names, shapes strings.Builder
	for i, tool := range tools {
		if i > 0 {
			names.WriteString(", ")
			shapes.WriteString(" ")
		}
		names.WriteString(tool.Function.Name)
		shapes.WriteString(toolShape(tool))
	}
	var spec strings.Builder
	for _, tool := range tools {
		parameters := string(tool.Function.Parameters)
		if parameters == "" {
			parameters = `{"type":"object","properties":{}}`
		}
		spec.WriteString("- " + tool.Function.Name + ": " + tool.Function.Description + "\n  arguments schema: " + parameters + "\n")
	}
	header := "[Tool calls available: " + names.String() + ". Use them directly, do not search for them."
	if codeMode(system) {
		header += " A direct call by name works; `execute` can also reach them as tools.<name> and offers search()."
	}
	header += "]\n"
	if shapes.Len() > 0 {
		header += "Shapes:" + shapes.String() + "\n"
	}
	header += "\n"
	return header + system + "\n\n## Tools\n" +
		"To call a tool, output ONLY a block that starts with " + toolOpen + " and ends with " + toolClose +
		", containing valid JSON of the shape {\"calls\":[{\"name\":\"...\",\"arguments\":{...}}]}." +
		" Call at least one of: " + names.String() + ". Do not wrap the block in Markdown.\n" +
		"Prefer the tool's exact name and a JSON argument object; a direct call works even where a sandbox lists the same tools. Claude-style XML is also accepted:\n" +
		"<invoke name=\"tool\"><parameter name=\"argument\">value</parameter></invoke>\n" + spec.String()
}

// codeMode reports whether the harness prompt routes tools through a sandbox.
func codeMode(system string) bool {
	return strings.Contains(system, "only work inside code you pass to")
}

// toolShape renders a tool's required arguments, plus up to two optional ones,
// so the model can call it without looking the schema up.
func toolShape(tool toolSpec) string {
	var required []string
	var optional []string
	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(tool.Function.Parameters, &schema); err == nil {
		required = schema.Required
		for name := range schema.Properties {
			if !slices.Contains(required, name) {
				optional = append(optional, name+"?")
			}
		}
		sort.Strings(optional)
		if len(optional) > 2 {
			optional = optional[:2]
		}
	}
	shape := tool.Function.Name + "{" + strings.Join(append(required, optional...), ",") + "}"
	return shape
}

// toolGuard splits a text stream into visible text and parsed tool blocks.
// toolCounters record how often the parser had to reshape a call. They are
// process-wide because the guard is built per request and /metrics is scraped
// across requests.
var toolCounters struct {
	unwrap  atomic.Int64
	fold    atomic.Int64
	suffix  atomic.Int64
	unknown atomic.Int64
}

// ToolNameStats reports how often a tool name was repaired and how often one
// stayed unknown, which tells the doctor whether the model can see the tools.
func ToolNameStats() (repaired, unknown int64) {
	return toolCounters.fold.Load() + toolCounters.suffix.Load(), toolCounters.unknown.Load()
}

// unknownNames remembers which names stayed unknown, so the warning can name
// them instead of only counting. The set is bounded: names beyond the cap
// replace the least frequent one, which keeps the map small and the report
// focused on what keeps happening.
const maxUnknownNames = 16

var unknownNames struct {
	mu    sync.Mutex
	count map[string]int
}

// noteUnknownName counts one unknown name under the cap.
func noteUnknownName(name string) {
	unknownNames.mu.Lock()
	defer unknownNames.mu.Unlock()
	if unknownNames.count == nil {
		unknownNames.count = map[string]int{}
	}
	if _, seen := unknownNames.count[name]; seen || len(unknownNames.count) < maxUnknownNames {
		unknownNames.count[name]++
		return
	}
	trimUnknownNames(maxUnknownNames - 1)
	unknownNames.count[name] = 1
}

// ToolUnknownNames lists the most frequent unknown names, newest first on a tie.
func ToolUnknownNames(limit int) []string {
	unknownNames.mu.Lock()
	defer unknownNames.mu.Unlock()
	names := make([]string, 0, len(unknownNames.count))
	for name := range unknownNames.count {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if unknownNames.count[names[i]] != unknownNames.count[names[j]] {
			return unknownNames.count[names[i]] > unknownNames.count[names[j]]
		}
		return names[i] < names[j]
	})
	if limit > 0 && len(names) > limit {
		names = names[:limit]
	}
	return names
}

type toolGuard struct {
	tools   []toolSpec
	buf     strings.Builder
	recent  []string
	scanned int
}

// unique drops a call that repeats the previous one, which happens when a model
// emits a sentinel block and the XML form for the same call.
func (g *toolGuard) unique(calls []toolCall) []toolCall {
	calls = g.resolve(calls)
	kept := make([]toolCall, 0, len(calls))
	for _, call := range calls {
		key := call.Function.Name + "\x00" + call.Function.Arguments
		seen := false
		for _, old := range g.recent {
			if old == key {
				seen = true
				break
			}
		}
		if seen {
			continue
		}
		if len(g.recent) == 8 {
			g.recent = g.recent[1:]
		}
		g.recent = append(g.recent, key)
		kept = append(kept, call)
	}
	return kept
}

func (g *toolGuard) add(tools []toolSpec) { g.tools = tools }

// reset drops buffered and dedupe state before a retry.
func (g *toolGuard) reset() {
	g.buf.Reset()
	g.recent = nil
	g.scanned = 0
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

// resolve rewrites a name that differs from exactly one advertised tool only by
// case or separators, so `Read-File` reaches the client as `read_file`.
func (g *toolGuard) resolve(calls []toolCall) []toolCall {
	for i := range calls {
		name := calls[i].Function.Name
		if g.known(name) {
			continue
		}
		key := nameKey(name)
		if key == "" {
			continue
		}
		match, count := "", 0
		for _, tool := range g.tools {
			if nameKey(tool.Function.Name) == key {
				match, count = tool.Function.Name, count+1
			}
		}
		if count == 1 {
			calls[i].Function.Name = match
			toolCounters.fold.Add(1)
			saveToolNameState()
		}
	}
	for i := range calls {
		// "shell command" names the tool and repeats what the argument carries.
		name := calls[i].Function.Name
		if g.known(name) {
			continue
		}
		head, _, _ := strings.Cut(name, " ")
		if head != name && g.known(head) {
			calls[i].Function.Name = head
			toolCounters.suffix.Add(1)
			saveToolNameState()
			continue
		}
		// Nothing matched: the name goes to the client as it is.
		toolCounters.unknown.Add(1)
		noteUnknownName(name)
		saveToolNameState()
	}
	return calls
}

// nameKey folds case and drops the separators models sprinkle into tool names.
func nameKey(name string) string {
	var folded strings.Builder
	for _, r := range strings.ToLower(name) {
		switch r {
		case '_', '-', ' ', '.':
			continue
		}
		folded.WriteRune(r)
	}
	return folded.String()
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
		from := min(g.scanned, len(current))
		if opensWithMarker(current) {
			from = 0
		}
		start := indexFrom(current, toolOpen, from)
		if start < 0 {
			if at := dsmlLeadingClose(current); at >= 0 {
				loc := dsmlEnd.FindStringSubmatchIndex(current[at:])
				if loc == nil {
					g.buf.Reset()
					g.buf.WriteString(current[at:])
					g.scanned = 0
					return current[:at], nil, nil
				}
				g.buf.Reset()
				g.buf.WriteString(current[at+loc[1]:])
				g.scanned = 0
				continue
			}
			if dsmlStart := dsmlIndexFrom(current, from); dsmlStart >= 0 {
				head, calls, used, complete, err := consumeDSML(current[dsmlStart:], false)
				if err != nil {
					g.buf.Reset()
					g.buf.WriteString(current[dsmlStart:])
					return current[:dsmlStart], nil, err
				}
				// A closed block with an unknown name is still delivered: the name
				// cannot change any more, and the client reports it better than a
				// stalled buffer.
				if !complete {
					g.buf.Reset()
					g.buf.WriteString(current[dsmlStart:])
					if g.buf.Len() > maxToolBuffer {
						return current[:dsmlStart], nil, errNoToolBlock
					}
					g.scanned = max(0, g.buf.Len()-toolOverlap)
					return current[:dsmlStart], nil, nil
				}
				calls = g.unique(calls)
				rest := current[dsmlStart+used:]
				g.buf.Reset()
				g.buf.WriteString(rest)
				g.scanned = 0
				return current[:dsmlStart] + head, calls, nil
			}
			if xmlStart := invokeIndexFrom(current, from); xmlStart >= 0 {
				head, calls, used, complete, err := consumeInvokes(current[xmlStart:])
				if err != nil {
					g.buf.Reset()
					g.buf.WriteString(current[xmlStart:])
					return current[:xmlStart], nil, err
				}
				if !complete {
					g.buf.Reset()
					g.buf.WriteString(current[xmlStart:])
					if g.buf.Len() > maxToolBuffer {
						return current[:xmlStart], nil, errNoToolBlock
					}
					g.scanned = max(0, g.buf.Len()-toolOverlap)
					return current[:xmlStart], nil, nil
				}
				if g.buf.Len() > maxToolBuffer {
					return current[:xmlStart], nil, errNoToolBlock
				}
				calls = g.unique(calls)
				rest := current[xmlStart+used:]
				g.buf.Reset()
				g.buf.WriteString(rest)
				g.scanned = 0
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
			g.scanned = 0
			return flush, nil, nil
		}
		head := current[:start]
		rest := current[start+len(toolOpen):]
		end := strings.Index(rest, toolClose)
		if end < 0 {
			g.buf.Reset()
			g.buf.WriteString(current[start:])
			if g.buf.Len() > maxToolBuffer {
				return head, nil, errNoToolBlock
			}
			g.scanned = max(0, g.buf.Len()-toolOverlap)
			return head, nil, nil
		}
		parsed, err := g.parse(rest[:end])
		g.buf.Reset()
		g.buf.WriteString(rest[end+len(toolClose):])
		g.scanned = 0
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
			fragment := trimPartialClose(current[dsmlStart:], toolClose)
			head, calls, _, _, err := consumeDSML(fragment, true)
			if err == nil && len(calls) == 0 {
				// The marker was prose, not a block: keep it visible.
				return current, nil, nil
			}
			if err == nil {
				calls = g.unique(calls)
			}
			return current[:dsmlStart] + head, calls, err
		}
		if xmlStart := invokeIndex(current); xmlStart >= 0 {
			head, calls, _, _, err := consumeInvokes(current[xmlStart:])
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

// opensWithMarker reports whether the buffer starts with a block marker, in
// which case scanning must start at zero again.
func opensWithMarker(current string) bool {
	for _, marker := range []string{toolOpen, "<invoke", "<function_calls", "<|DSML|", "<｜DSML｜", "<|/DSML|", "<｜/DSML｜"} {
		if strings.HasPrefix(current, marker) {
			return true
		}
	}
	return false
}

// indexFrom finds sub at or after from.
func indexFrom(current, sub string, from int) int {
	if from >= len(current) {
		return -1
	}
	if index := strings.Index(current[from:], sub); index >= 0 {
		return from + index
	}
	return -1
}

// DeepSeek emits its native DSML tool syntax as text. The bar can be ASCII or
// full width and the closing tag may be written two ways.
var (
	dsmlPrefix = regexp.MustCompile(`<[|｜]DSML[|｜]\s*(?:calls|function_calls|invoke)\b`)
	dsmlCalls  = regexp.MustCompile(`<[|｜]DSML[|｜]\s*(?:calls|function_calls)\b[^>]*>`)
	// Inner tags may arrive without the DSML prefix when a model mixes dialects.
	dsmlInvoke = regexp.MustCompile(`<(?:[|｜]DSML[|｜]\s*)?invoke\s+([^>]+)>`)
	dsmlParam  = regexp.MustCompile(`<(?:[|｜]DSML[|｜]\s*)?parameter\s+([^>]+)>`)
	dsmlEnd    = regexp.MustCompile(`(?:</(?:[|｜]DSML[|｜]\s*)?|<[|｜]/DSML[|｜]\s*)(calls|function_calls|invoke|parameter)\s*>`)
)

// dsmlIndex reports the first DSML block start, envelope or bare invoke.
func dsmlIndex(current string) int { return dsmlIndexFrom(current, 0) }

// dsmlIndexFrom reports the first DSML block start at or after from.
func dsmlIndexFrom(current string, from int) int {
	if from >= len(current) {
		return -1
	}
	if loc := dsmlPrefix.FindStringIndex(current[from:]); loc != nil {
		return from + loc[0]
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
func consumeDSML(fragment string, final bool) (head string, calls []toolCall, used int, complete bool, err error) {
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
			if !final {
				return prose.String(), calls, start, false, nil
			}
			from, to = len(body), len(body)
		}
		values, consumed, paramErr := dsmlValues(body[:from], final)
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
		} else if final {
			offset = len(fragment)
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

// lastCloseAt finds the last closing tag with the wanted name.
func lastCloseAt(body, want string) (start, end int) {
	from, to := -1, -1
	for offset := 0; offset < len(body); {
		loc := dsmlEnd.FindStringSubmatchIndex(body[offset:])
		if loc == nil {
			break
		}
		if body[offset+loc[2]:offset+loc[3]] == want {
			from, to = offset+loc[0], offset+loc[1]
		}
		offset += loc[1]
	}
	return from, to
}

// dsmlValues reads parameter elements; string="true" keeps the body verbatim.
func dsmlValues(body string, final bool) (map[string]any, int, error) {
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
		segment := body
		if next := dsmlParam.FindStringIndex(body); next != nil {
			segment = body[:next[0]]
		}
		from, to := lastCloseAt(segment, "parameter")
		if key == "" {
			return nil, 0, errNoToolBlock
		}
		if from < 0 {
			if !final {
				return nil, 0, errNoToolBlock
			}
			from, to = len(body), len(body)
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

// argumentWrappers are the member names a model uses for the real argument
// object when it adds a level of nesting the schema does not have. A model can
// stack them (arguments, then input), so unwrapping repeats a few times.
var argumentWrappers = []string{"arguments", "input", "params", "parameters", "args"}

// maxArgumentDepth bounds the unwrap so a self-referencing wrapper cannot spin.
const maxArgumentDepth = 4

// unwrapArguments flattens lone wrapper members so the tool sees the argument
// object it declared. The wrapper value is usually an object, but a model that
// stringifies it must be unwrapped too.
func unwrapArguments(values map[string]any) any {
	flattened := false
	defer func() {
		if flattened {
			toolCounters.unwrap.Add(1)
		}
	}()
	var current any = values
	for range maxArgumentDepth {
		object, ok := current.(map[string]any)
		if !ok || len(object) != 1 {
			return current
		}
		var inner any
		found := false
		for _, name := range argumentWrappers {
			if value, ok := object[name]; ok {
				inner, found = value, true
				break
			}
		}
		if !found {
			return current
		}
		// A stringified object unwraps when it decodes, or when it is a run of
		// name=value pairs. Anything else, including a lone real string argument,
		// stays as it is.
		if text, ok := inner.(string); ok {
			var decoded any
			if json.Unmarshal([]byte(text), &decoded) != nil {
				pairs := keyValueArguments(text)
				if pairs == nil {
					return current
				}
				decoded = pairs
			}
			inner = decoded
		}
		switch inner.(type) {
		case map[string]any, []any:
			current = inner
			flattened = true
		default:
			return current
		}
	}
	return current
}

// keyValueArguments parses "command=ls timeout=5", but only when every token is
// a simple name=value pair, so ordinary prose stays a string.
func keyValueArguments(text string) map[string]any {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil
	}
	object := make(map[string]any, len(fields))
	for _, field := range fields {
		name, value, ok := strings.Cut(field, "=")
		if !ok || !argumentName(name) {
			return nil
		}
		object[name] = value
	}
	return object
}

// argumentName reports whether name is a bare identifier a schema could declare.
func argumentName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case (r >= '0' && r <= '9') || r == '-' || r == '.':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// buildCallFromValues marshals typed arguments into a tool call.
func buildCallFromValues(name string, values map[string]any) (toolCall, error) {
	if name == "" {
		return toolCall{}, errNoToolBlock
	}
	encoded, err := json.Marshal(unwrapArguments(values))
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

func invokeIndex(current string) int { return invokeIndexFrom(current, 0) }

// invokeIndexFrom reports the first XML invoke marker at or after from.
func invokeIndexFrom(current string, from int) int {
	found := -1
	for _, marker := range invokeMarkers {
		if index := indexFrom(current, marker, from); index >= 0 && (found < 0 || index < found) {
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

// lastCloseBefore finds the last closing marker before the next opening marker,
// so a literal close tag inside a value does not cut it short.
func lastCloseBefore(body, openMarker, closeMarker string) int {
	segment := body
	if next := strings.Index(body, openMarker); next >= 0 {
		segment = body[:next]
	}
	last, offset := -1, 0
	for {
		index := strings.Index(segment[offset:], closeMarker)
		if index < 0 {
			return last
		}
		last = offset + index
		offset = last + len(closeMarker)
	}
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
			end := lastCloseBefore(after, "<parameter", "</parameter>")
			if end < 0 {
				object[parameter] = strings.TrimSpace(after)
				body = ""
				break
			}
			object[parameter] = parameterValue(after[:end])
			body = after[end+len("</parameter>"):]
		}
		encoded, err := json.Marshal(unwrapArguments(object))
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
	var stack []string
	nested := false
	decoder := xml.NewDecoder(strings.NewReader("<value>" + escapeStrayClose(raw) + "</value>"))
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch value := token.(type) {
		case xml.StartElement:
			if value.Name.Local != "value" {
				stack = append(stack, value.Name.Local)
				nested = true
			}
		case xml.EndElement:
			if value.Name.Local == "value" {
				break
			}
			if len(stack) > 0 && stack[len(stack)-1] == value.Name.Local {
				stack = stack[:len(stack)-1]
				if text.Len() > 0 {
					fragments = append(fragments, typedOrText(text.String()))
					text.Reset()
				}
			} else {
				// A close tag for an element this value never opened.
				if text.Len() > 0 {
					text.WriteString("</" + value.Name.Local + ">")
				}
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

// escapeStrayClose escapes close tags for elements the value never opened, so
// the XML decoder does not stop at a literal "</parameter>" in the text.
func escapeStrayClose(raw string) string {
	var out strings.Builder
	var stack []string
	letter := func(b byte) bool {
		return b == '_' || b == '-' || b == ':' || b >= 'a' && b <= 'z' ||
			b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
	}
	for i := 0; i < len(raw); {
		if raw[i] != '<' {
			out.WriteByte(raw[i])
			i++
			continue
		}
		closing, j := false, i+1
		if j < len(raw) && raw[j] == '/' {
			closing, j = true, j+1
		}
		start := j
		for j < len(raw) && letter(raw[j]) {
			j++
		}
		name := raw[start:j]
		end := -1
		if name != "" {
			end = strings.IndexByte(raw[j:], '>')
		}
		if end < 0 {
			out.WriteString("&lt;")
			i++
			continue
		}
		tag := raw[i : j+end+1]
		self := strings.HasSuffix(strings.TrimSpace(tag[:len(tag)-1]), "/")
		switch {
		case !closing:
			if !self {
				stack = append(stack, name)
			}
			out.WriteString(tag)
		case len(stack) > 0 && stack[len(stack)-1] == name:
			stack = stack[:len(stack)-1]
			out.WriteString(tag)
		default:
			out.WriteString("&lt;/" + name + "&gt;")
		}
		i = j + end + 1
	}
	return out.String()
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

	decode := func(text string) ([]map[string]any, bool) {
		var envelope struct {
			Calls []map[string]any `json:"calls"`
		}
		if json.Unmarshal([]byte(text), &envelope) == nil && len(envelope.Calls) > 0 {
			return envelope.Calls, true
		}
		// A lone call object without the envelope is common enough to accept.
		var one map[string]any
		if json.Unmarshal([]byte(text), &one) == nil && one["name"] != nil {
			return []map[string]any{one}, true
		}
		return nil, false
	}
	candidates, ok := decode(block)
	if !ok {
		candidates, ok = decode(escapeControl(block))
	}
	if !ok {
		return nil, errNoToolBlock
	}
	calls := make([]toolCall, 0, len(candidates))
	for _, item := range candidates {
		// The name is not checked against the advertised tools: a closed block is
		// final, and the client reports an unknown name better than a failed turn.
		name, _ := item["name"].(string)
		if name == "" {
			return nil, errNoToolBlock
		}
		// Everything but the name is the argument map; unwrapArguments then
		// flattens a lone wrapper such as "input" or "params".
		values := make(map[string]any, len(item))
		for key, value := range item {
			if key != "name" {
				values[key] = value
			}
		}
		call, err := buildCallFromValues(name, values)
		if err != nil {
			return nil, err
		}
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
