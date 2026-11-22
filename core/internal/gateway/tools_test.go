// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func guardFor(names ...string) *toolGuard {
	guard := &toolGuard{}
	tools := make([]toolSpec, 0, len(names))
	for _, name := range names {
		spec := toolSpec{Type: "function"}
		spec.Function.Name = name
		tools = append(tools, spec)
	}
	guard.add(tools)
	return guard
}

func TestToolGuardSplitsMarkers(t *testing.T) {
	guard := guardFor("read_file")
	block := `{"calls":[{"name":"read_file","arguments":{"path":"a.txt"}}]}`
	chunks := []string{"before ", "<<<ALLAIIS_TOOL", "_CALL>>>", block[:20], block[20:], "<<<END_TOOL_CALL>>>", " after"}
	var text strings.Builder
	var calls []toolCall
	for _, chunk := range chunks {
		safe, parsed, err := guard.Write(chunk)
		text.WriteString(safe)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		calls = append(calls, parsed...)
	}
	tail, more, err := guard.Flush()
	text.WriteString(tail)
	calls = append(calls, more...)
	if err != nil || text.String() != "before  after" || len(calls) != 1 {
		t.Fatalf("text=%q calls=%d err=%v", text.String(), len(calls), err)
	}
	if calls[0].Function.Name != "read_file" || !strings.Contains(calls[0].Function.Arguments, `"a.txt"`) {
		t.Fatalf("arguments were mangled: %+v", calls[0])
	}
}

func TestToolGuardRejectsBadBlocks(t *testing.T) {
	guard := guardFor("read_file")
	if _, _, err := guard.Write("<<<ALLAIIS_TOOL_CALL>>>{\"calls\":[{\"name\":\"read_file\",\"arguments\":{\"path\":}}]}<<<END_TOOL_CALL>>>"); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	guard = guardFor("read_file")
	// An unknown name is delivered, the client reports it.
	if _, calls, err := guard.Write("<<<ALLAIIS_TOOL_CALL>>>{\"calls\":[{\"name\":\"other\",\"arguments\":{}}]}<<<END_TOOL_CALL>>>"); err != nil ||
		len(calls) != 1 || calls[0].Function.Name != "other" {
		t.Fatalf("unknown tool dropped: %+v %v", calls, err)
	}
	guard = guardFor("read_file")
	if _, _, err := guard.Write("<<<ALLAIIS_TOOL_CALL>>>{\"calls\":[]}<<<END_TOOL_CALL>>>"); err == nil {
		t.Fatal("empty call list accepted")
	}
}

func TestToolGuardFlushesOpenBlockOnStop(t *testing.T) {
	guard := guardFor("read_file")
	if _, _, err := guard.Write(`<<<ALLAIIS_TOOL_CALL>>>{"calls":[{"name":"read_file","arguments":{"path":"b"}}]}`); err != nil {
		t.Fatal(err)
	}
	text, calls, err := guard.Flush()
	if err != nil || text != "" || len(calls) != 1 {
		t.Fatalf("text=%q calls=%d err=%v", text, len(calls), err)
	}
}

func TestToolGuardKeepsPlainText(t *testing.T) {
	guard := guardFor("read_file")
	safe, calls, err := guard.Write("just text")
	if err != nil || safe != "just text" || len(calls) != 0 {
		t.Fatalf("safe=%q err=%v", safe, err)
	}
	// A trailing marker prefix must stay buffered.
	safe, _, _ = guard.Write(" more <<<ALLA")
	if strings.HasSuffix(safe, "<<<") || !strings.Contains(safe, "more") {
		t.Fatalf("prefix was emitted: %q", safe)
	}
}

func TestFetchImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("abc"))
	}))
	defer server.Close()
	image, ok := fetchImage(server.URL)
	if !ok || string(image.Data) != "abc" || image.MIME != "image/png" {
		t.Fatalf("image %+v ok=%v", image, ok)
	}
	if _, ok := fetchImage("ftp://example.invalid/x.png"); ok {
		t.Fatal("non-http URL accepted")
	}
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>"))
	}))
	defer html.Close()
	if _, ok := fetchImage(html.URL); ok {
		t.Fatal("non-image content type accepted")
	}
}

func TestToolGuardParsesInvokeXML(t *testing.T) {
	guard := guardFor("read_file", "list_dir")
	fragment := "Reading now.\n<function_calls>\n<invoke name=\"read_file\">\n" +
		"<parameter name=\"path\">go.mod</parameter>\n</invoke>\n" +
		"<invoke name=\"list_dir\"><parameter name=\"depth\">2</parameter></invoke>\n</function_calls>\nDone."
	var text strings.Builder
	var calls []toolCall
	for _, chunk := range splitEvery(fragment, 7) {
		safe, parsed, err := guard.Write(chunk)
		if err != nil {
			t.Fatalf("unexpected error at %q: %v", chunk, err)
		}
		text.WriteString(safe)
		calls = append(calls, parsed...)
	}
	tail, more, err := guard.Flush()
	text.WriteString(tail)
	calls = append(calls, more...)
	if err != nil {
		t.Fatal(err)
	}
	if got := text.String(); !strings.Contains(got, "Reading now.") || strings.Contains(got, "<invoke") {
		t.Fatalf("visible text %q", got)
	}
	if len(calls) != 2 || calls[0].Function.Name != "read_file" ||
		calls[0].Function.Arguments != `{"path":"go.mod"}` || calls[1].Function.Arguments != `{"depth":2}` {
		t.Fatalf("calls %+v", calls)
	}
	if !strings.Contains(text.String(), "Done.") {
		t.Fatalf("trailing text lost: %q", text.String())
	}
}

func TestToolGuardInvokeJSONBodyAndUnknownTool(t *testing.T) {
	guard := guardFor("read_file")
	safe, calls, err := guard.Write(`<invoke name="read_file">{"path":"a.txt"}</invoke>`)
	if err != nil || len(calls) != 1 || calls[0].Function.Arguments != `{"path":"a.txt"}` {
		t.Fatalf("json body: %q %+v %v", safe, calls, err)
	}
	guard = guardFor("read_file")
	// A closed block with an unknown name is delivered so the client can report
	// it, instead of stalling the stream.
	if _, calls, err = guard.Write(`<invoke name="other"><parameter name="x">1</parameter></invoke>`); err != nil ||
		len(calls) != 1 || calls[0].Function.Name != "other" {
		t.Fatalf("unknown tool dropped: %+v %v", calls, err)
	}
	guard = guardFor("read_file")
	_, calls, err = guard.Write(`<invoke name="read_file"><parameter name="items"><item>a</item><item>2</item></parameter></invoke>`)
	if err != nil || calls[0].Function.Arguments != `{"items":["a",2]}` {
		t.Fatalf("nested list: %+v %v", calls, err)
	}
	guard = guardFor("read_file")
	_, calls, err = guard.Write(`<invoke name="read_file"><parameter name="path">2024</parameter></invoke>`)
	if err != nil || calls[0].Function.Arguments != `{"path":2024}` {
		t.Fatalf("scalar: %+v %v", calls, err)
	}
}

func TestToolGuardSelfClosingInvoke(t *testing.T) {
	guard := guardFor("ping")
	_, calls, err := guard.Write("<invoke name=\"ping\"/>")
	if err != nil || len(calls) != 1 || calls[0].Function.Arguments != "{}" {
		t.Fatalf("self closing: %+v %v", calls, err)
	}
}

func splitEvery(value string, size int) []string {
	var chunks []string
	for len(value) > size {
		chunks = append(chunks, value[:size])
		value = value[size:]
	}
	return append(chunks, value)
}

func TestToolGuardDropsDuplicatedDialect(t *testing.T) {
	guard := guardFor("read_file")
	block := `{"calls":[{"name":"read_file","arguments":{"path":"a.txt"}}]}`
	_, first, err := guard.Write("<<<ALLAIIS_TOOL_CALL>>>" + block + "<<<END_TOOL_CALL>>>")
	if err != nil || len(first) != 1 {
		t.Fatalf("first pass: %+v %v", first, err)
	}
	_, second, err := guard.Write(`<invoke name="read_file"><parameter name="path">a.txt</parameter></invoke>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("duplicate survived: %+v", second)
	}
	_, third, err := guard.Write(`<invoke name="read_file"><parameter name="path">b.txt</parameter></invoke>`)
	if err != nil || len(third) != 1 {
		t.Fatalf("different call dropped: %+v %v", third, err)
	}
}

func TestToolGuardParsesDeepSeekDSML(t *testing.T) {
	guard := guardFor("execute", "read")
	fragment := "Working.\n<|DSML|\u001bcalls>\n<|DSML|\u001binvoke name=\"execute\">\n" +
		"<|DSML|\u001bparameter name=\"code\" string=\"true\">const n = 1;\nreturn n;</|DSML|\u001bparameter>\n" +
		"</|DSML|\u001binvoke>\n<|DSML|\u001binvoke name=\"read\"><|DSML|\u001bparameter name=\"limit\">2000</|DSML|\u001bparameter>" +
		"</|DSML|\u001binvoke>\n</|DSML|\u001bcalls>\nDone."
	fragment = strings.ReplaceAll(fragment, "\u001b", "")
	var text strings.Builder
	var calls []toolCall
	for _, chunk := range splitEvery(fragment, 5) {
		safe, parsed, err := guard.Write(chunk)
		if err != nil {
			t.Fatalf("unexpected error at %q: %v", chunk, err)
		}
		text.WriteString(safe)
		calls = append(calls, parsed...)
	}
	tail, more, err := guard.Flush()
	text.WriteString(tail)
	calls = append(calls, more...)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text.String(), "DSML") {
		t.Fatalf("markers leaked: %q", text.String())
	}
	if len(calls) != 2 || calls[0].Function.Name != "execute" ||
		calls[0].Function.Arguments != `{"code":"const n = 1;\nreturn n;"}` ||
		calls[1].Function.Arguments != `{"limit":2000}` {
		t.Fatalf("calls %+v", calls)
	}
	if !strings.Contains(text.String(), "Working.") || !strings.Contains(text.String(), "Done.") {
		t.Fatalf("text %q", text.String())
	}
}

func TestToolGuardAcceptsBareDSMLInvokeAndFullWidthBar(t *testing.T) {
	guard := guardFor("read")
	fragment := "<|DSML|invoke name=\"read\"><|DSML|parameter name=\"path\">go.mod</|DSML|parameter></|DSML|invoke>"
	if _, calls, err := guard.Write(fragment); err != nil || len(calls) != 1 ||
		calls[0].Function.Arguments != `{"path":"go.mod"}` {
		t.Fatalf("bare invoke: %+v %v", calls, err)
	}
	guard = guardFor("read")
	wide := "<｜DSML｜invoke name=\"read\"><｜DSML｜parameter name=\"path\">go.mod</｜DSML｜parameter></｜DSML｜invoke>"
	if _, calls, err := guard.Write(wide); err != nil || len(calls) != 1 {
		t.Fatalf("full width: %+v %v", calls, err)
	}
}

func TestToolGuardFlushRepairsTruncatedBlock(t *testing.T) {
	guard := guardFor("execute")
	if _, _, err := guard.Write(toolOpen); err != nil {
		t.Fatal(err)
	}
	if _, _, err := guard.Write(`{"calls":[{"name":"execute","arguments":{"code":"line one` + "\n" + `line two"}}]}` +
		"<<<END_TOOL_CALL>>"); err != nil {
		t.Fatal(err)
	}
	tail, calls, err := guard.Flush()
	if err != nil || len(calls) != 1 || tail != "" {
		t.Fatalf("flush %q %+v %v", tail, calls, err)
	}
	if calls[0].Function.Arguments != `{"code":"line one\nline two"}` {
		t.Fatalf("arguments %s", calls[0].Function.Arguments)
	}
}

func TestToolGuardKeepsInlineMarkerAsText(t *testing.T) {
	guard := guardFor("read_file")
	if _, _, err := guard.Write("Use `<|DSML|calls>` for calls."); err != nil {
		t.Fatal(err)
	}
	tail, calls, err := guard.Flush()
	if err != nil || len(calls) != 0 || !strings.Contains(tail, "<|DSML|calls>") {
		t.Fatalf("inline marker %q %+v %v", tail, calls, err)
	}
}

func TestToolGuardDropsRepeatedNonAdjacentCall(t *testing.T) {
	guard := guardFor("read_file")
	var calls []toolCall
	for _, name := range []string{"a.txt", "b.txt", "a.txt"} {
		_, parsed, err := guard.Write(`<invoke name="read_file"><parameter name="path">` + name + `</parameter></invoke>`)
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, parsed...)
	}
	if len(calls) != 2 || calls[0].Function.Arguments != `{"path":"a.txt"}` ||
		calls[1].Function.Arguments != `{"path":"b.txt"}` {
		t.Fatalf("calls %+v", calls)
	}
}

func TestDSMLValueKeepsLiteralCloseTag(t *testing.T) {
	guard := guardFor("execute")
	fragment := `<|DSML|calls><|DSML|invoke name="execute"><|DSML|parameter name="code" string="true">` +
		`const s = "</|DSML| parameter>";` +
		`</|DSML| parameter></|DSML| invoke></|DSML| calls>`
	_, calls, err := guard.Write(fragment)
	if err != nil || len(calls) != 1 {
		t.Fatalf("calls %+v %v", calls, err)
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["code"] != `const s = "</|DSML| parameter>";` {
		t.Fatalf("arguments %q", decoded["code"])
	}
}

func TestInvokeXMLKeepsLiteralCloseTag(t *testing.T) {
	guard := guardFor("execute")
	fragment := `<invoke name="execute"><parameter name="code">` +
		`const s = "</parameter>";` +
		`</parameter></invoke>`
	_, calls, err := guard.Write(fragment)
	if err != nil || len(calls) != 1 {
		t.Fatalf("calls %+v %v", calls, err)
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["code"] != `const s = "</parameter>";` {
		t.Fatalf("arguments %q", decoded["code"])
	}
}

func TestGuardScansForwardThroughLongProse(t *testing.T) {
	guard := guardFor("read_file")
	prose := strings.Repeat("plain words without markers. ", 4000)
	var text strings.Builder
	for start := 0; start < len(prose); start += 997 {
		safe, calls, err := guard.Write(prose[start:min(start+997, len(prose))])
		if err != nil || len(calls) != 0 {
			t.Fatalf("prose %d %v", len(calls), err)
		}
		text.WriteString(safe)
	}
	_, calls, err := guard.Write(`<invoke name="read_file"><parameter name="path">a.txt</parameter></invoke>`)
	if err != nil || len(calls) != 1 {
		t.Fatalf("call after prose: %+v %v", calls, err)
	}
	if got := len(text.String()); got != len(prose) {
		t.Fatalf("prose length %d != %d", got, len(prose))
	}
}

func TestGuardRejectsOversizedUnclosedBlock(t *testing.T) {
	guard := guardFor("read_file")
	guard.Write(toolOpen + strings.Repeat("x", maxToolBuffer+1))
	if _, _, err := guard.Write("more"); err == nil {
		t.Fatal("oversized block accepted")
	}
}

func TestReasoningKeyIgnoresSurroundingSpace(t *testing.T) {
	store := newReasoningStore()
	store.save("alias", "\n\n"+toolOpen+`{"calls":[]}`+toolClose, "because")
	if got := store.load("alias", "\n"+toolOpen+`{"calls":[]}`+toolClose); got != "because" {
		t.Fatalf("reasoning %q", got)
	}
}

func TestToolGuardAcceptsMixedDialect(t *testing.T) {
	guard := guardFor("bash")
	fragment := "<|DSML|calls>\n<|DSML|invoke name=\"bash\">\n" +
		"<parameter name=\"arguments\">{\"command\":\"go test\"}</parameter>\n" +
		"</invoke>\n</|DSML|calls>"
	text, calls, err := guard.Write(fragment)
	if err != nil || len(calls) != 1 || strings.TrimSpace(text) != "" {
		t.Fatalf("mixed: %q %+v %v", text, calls, err)
	}
	if calls[0].Function.Name != "bash" || calls[0].Function.Arguments != `{"command":"go test"}` {
		t.Fatalf("call %+v", calls[0])
	}
}

func TestInvokeArgumentsWrapperIsFlattened(t *testing.T) {
	guard := guardFor("bash")
	_, calls, err := guard.Write(`<invoke name="bash"><parameter name="arguments">{"command":"go test"}</parameter></invoke>`)
	if err != nil || len(calls) != 1 || calls[0].Function.Arguments != `{"command":"go test"}` {
		t.Fatalf("wrapper: %+v %v", calls, err)
	}
}

func TestToolEnvelopePutsNamesFirst(t *testing.T) {
	guard := guardFor("read_file", "shell")
	_ = guard
	tools := make([]toolSpec, 2)
	tools[0].Function.Name = "read_file"
	tools[1].Function.Name = "shell"
	envelope := toolEnvelope("base", tools)
	if !strings.HasPrefix(envelope, "[Tool calls available: read_file, shell.") {
		t.Fatalf("header %q", envelope[:60])
	}
}

func TestToolGuardFlushRepairsUnclosedDSML(t *testing.T) {
	guard := guardFor("execute")
	if _, _, err := guard.Write("<|DSML|calls>\n<|DSML|invoke name=\"execute\">\n<|DSML|parameter name=\"arguments\">" +
		`{"code":"go test"}` + toolClose); err != nil {
		t.Fatal(err)
	}
	tail, calls, err := guard.Flush()
	if err != nil || len(calls) != 1 || strings.TrimSpace(tail) != "" {
		t.Fatalf("flush %q %+v %v", tail, calls, err)
	}
	if calls[0].Function.Arguments != `{"code":"go test"}` {
		t.Fatalf("arguments %s", calls[0].Function.Arguments)
	}
}

func TestRenderCallListCompactsArguments(t *testing.T) {
	var call toolCall
	call.Function.Name = "shell"
	call.Function.Arguments = `{"command": "ls -la", "timeout": 5}`
	got := renderCallList("\n\n", []toolCall{call})
	want := "\n\n" + "\n<<<ALLAIIS_TOOL_CALL>>>" +
		`{"calls":[{"name":"shell","arguments":{"command":"ls -la","timeout":5}}]}` +
		"<<<END_TOOL_CALL>>>"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestReasoningReplaySurvivesClientRendering(t *testing.T) {
	store := newReasoningStore()
	var call toolCall
	call.Function.Name = "shell"
	call.Function.Arguments = `{"command": "ls -la"}`
	store.save("alias", renderCallList("", []toolCall{call}), "thinking")
	// A client trims the prose and re-serializes the arguments without spaces.
	client := "\n<<<ALLAIIS_TOOL_CALL>>>" +
		`{"calls":[{"name":"shell","arguments":{"command":"ls -la"}}]}` +
		"<<<END_TOOL_CALL>>>"
	if got := store.load("alias", client); got != "thinking" {
		t.Fatalf("load %q", got)
	}
}

func TestReasoningStoreFindsBareProse(t *testing.T) {
	store := newReasoningStore()
	var call toolCall
	call.Function.Name = "shell"
	call.Function.Arguments = `{"command":"ls"}`
	text := "I will look around.\n\n"
	block := renderCallList(text, []toolCall{call})
	store.save("alias", block, "thinking")
	store.save("alias", text, "thinking")
	if got := store.load("alias", "I will look around."); got != "thinking" {
		t.Fatalf("prose load %q", got)
	}
	if got := store.load("alias", block); got != "thinking" {
		t.Fatalf("block load %q", got)
	}
}

func TestToolEnvelopeListsShapesAndCodeMode(t *testing.T) {
	tools := make([]toolSpec, 1)
	tools[0].Function.Name = "read"
	tools[0].Function.Parameters = json.RawMessage(
		`{"type":"object","properties":{"filePath":{"type":"string"},"limit":{"type":"integer"}},` +
			`"required":["filePath"]}`)
	code := "Use the `execute` tool to call the tools listed below." +
		" They only work inside code you pass to `execute`."
	envelope := toolEnvelope(code, tools)
	if !strings.Contains(envelope, "tools.<name>") {
		t.Fatalf("sandbox route missing: %q", envelope[:200])
	}
	if !strings.Contains(envelope, "read{filePath,limit?}") {
		t.Fatalf("shape missing: %q", envelope[:200])
	}
	if plain := toolEnvelope("base", tools); strings.Contains(plain, "tools.<name>") {
		t.Fatal("code mode leaked into a plain prompt")
	}
}

func TestToolShapeSkipsOptionalOverflow(t *testing.T) {
	tools := make([]toolSpec, 1)
	tools[0].Function.Name = "write"
	tools[0].Function.Parameters = json.RawMessage(
		`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"},` +
			`"c":{"type":"string"},"d":{"type":"string"}},"required":["a"]}`)
	if shape := toolShape(tools[0]); shape != "write{a,b?,c?}" {
		t.Fatalf("shape %q", shape)
	}
}

func TestInvokeArgumentsStringWrapperIsFlattened(t *testing.T) {
	guard := guardFor("bash")
	_, calls, err := guard.Write(`<invoke name="bash"><parameter name="arguments">` +
		`{"command":"go test ./..."}</parameter></invoke>`)
	if err != nil || len(calls) != 1 || calls[0].Function.Arguments != `{"command":"go test ./..."}` {
		t.Fatalf("string wrapper: %+v %v", calls, err)
	}
}

func TestUnknownToolIsDeliveredWhileStreaming(t *testing.T) {
	guard := guardFor("shell")
	if _, calls, err := guard.Write(`<invoke name="bash">`); err != nil || len(calls) != 0 {
		t.Fatalf("open block parsed too early: %+v %v", calls, err)
	}
	_, calls, err := guard.Write(`<parameter name="command">ls</parameter></invoke>`)
	if err != nil || len(calls) != 1 || calls[0].Function.Name != "bash" ||
		calls[0].Function.Arguments != `{"command":"ls"}` {
		t.Fatalf("unknown tool stalled: %+v %v", calls, err)
	}
}

func TestDSMLUnknownToolIsDelivered(t *testing.T) {
	guard := guardFor("other")
	block := `<|DSML|function_calls>\n<|DSML|invoke name="bash">\n` +
		`<|DSML|parameter name="command" string="true">ls</|DSML|parameter>\n` +
		"</|DSML|invoke>\n</|DSML|function_calls>" + toolClose
	_, calls, err := guard.Write(block)
	if err != nil || len(calls) != 1 || calls[0].Function.Name != "bash" {
		t.Fatalf("dsml unknown: %+v %v", calls, err)
	}
}

func TestToolGuardAcceptsABareCallObject(t *testing.T) {
	guard := guardFor("shell")
	_, calls, err := guard.Write(`<<<ALLAIIS_TOOL_CALL>>>{"name":"shell","arguments":{"command":"ls"}}` + toolClose)
	if err != nil || len(calls) != 1 || calls[0].Function.Arguments != `{"command":"ls"}` {
		t.Fatalf("bare call: %+v %v", calls, err)
	}
}

func TestToolGuardDefaultsMissingArguments(t *testing.T) {
	guard := guardFor("ping")
	_, calls, err := guard.Write(`<<<ALLAIIS_TOOL_CALL>>>{"calls":[{"name":"ping"}]}` + toolClose)
	if err != nil || len(calls) != 1 || calls[0].Function.Arguments != `{}` {
		t.Fatalf("no arguments: %+v %v", calls, err)
	}
}

func TestToolGuardFoldsNameCaseAndSeparators(t *testing.T) {
	for _, name := range []string{"Read-File", "READ_FILE", "read file", "ReadFile"} {
		g := guardFor("read_file")
		_, calls, err := g.Write(`<invoke name="` + name + `"><parameter name="path">a</parameter></invoke>`)
		if err != nil || len(calls) != 1 || calls[0].Function.Name != "read_file" {
			t.Fatalf("%s: %+v %v", name, calls, err)
		}
	}
}

func TestToolGuardKeepsAnAmbiguousName(t *testing.T) {
	guard := guardFor("ab", "a_b")
	_, calls, err := guard.Write(`<invoke name="AB"><parameter name="x">1</parameter></invoke>`)
	if err != nil || len(calls) != 1 || calls[0].Function.Name != "AB" {
		t.Fatalf("ambiguous: %+v %v", calls, err)
	}
}

func TestToolGuardUnwrapsArgumentWrappers(t *testing.T) {
	for _, key := range []string{"arguments", "input", "params", "parameters", "args"} {
		guard := guardFor("shell")
		block := `<<<ALLAIIS_TOOL_CALL>>>{"calls":[{"name":"shell","` + key + `":{"command":"ls"}}]}` + toolClose
		_, calls, err := guard.Write(block)
		if err != nil || len(calls) != 1 || calls[0].Function.Arguments != `{"command":"ls"}` {
			t.Fatalf("%s: %+v %v", key, calls, err)
		}
	}
}

func TestToolGuardUnwrapsAStringifiedWrapper(t *testing.T) {
	guard := guardFor("shell")
	_, calls, err := guard.Write(`<<<ALLAIIS_TOOL_CALL>>>{"calls":[{"name":"shell","input":"{\"command\":\"ls\"}"}]}` + toolClose)
	if err != nil || len(calls) != 1 || calls[0].Function.Arguments != `{"command":"ls"}` {
		t.Fatalf("stringified: %+v %v", calls, err)
	}
}

func TestToolGuardKeepsARealArgumentNamedInput(t *testing.T) {
	// Two members stay as they are even if one of them is called input.
	guard := guardFor("write")
	_, calls, err := guard.Write(`<<<ALLAIIS_TOOL_CALL>>>{"calls":[{"name":"write","input":"x","path":"a"}]}` + toolClose)
	if err != nil || len(calls) != 1 || calls[0].Function.Arguments != `{"input":"x","path":"a"}` {
		t.Fatalf("real input: %+v %v", calls, err)
	}
}

func TestToolGuardDropsATrailingNameWord(t *testing.T) {
	guard := guardFor("shell")
	_, calls, err := guard.Write(`<invoke name="shell command"><parameter name="command">ls</parameter></invoke>`)
	if err != nil || len(calls) != 1 || calls[0].Function.Name != "shell" {
		t.Fatalf("suffix: %+v %v", calls, err)
	}
}

func TestToolGuardKeepsAnUnknownSuffixedName(t *testing.T) {
	guard := guardFor("shell")
	_, calls, err := guard.Write(`<invoke name="bash command"><parameter name="command">ls</parameter></invoke>`)
	if err != nil || len(calls) != 1 || calls[0].Function.Name != "bash command" {
		t.Fatalf("unknown suffix: %+v %v", calls, err)
	}
}

func TestToolGuardUnwrapsStackedWrappers(t *testing.T) {
	guard := guardFor("shell")
	block := `<<<ALLAIIS_TOOL_CALL>>>{"calls":[{"name":"shell","arguments":{"input":{"command":"ls"}}}]}` + toolClose
	_, calls, err := guard.Write(block)
	if err != nil || len(calls) != 1 || calls[0].Function.Arguments != `{"command":"ls"}` {
		t.Fatalf("stacked: %+v %v", calls, err)
	}
}

func TestToolGuardKeepsALoneStringArgument(t *testing.T) {
	// "input" is a real argument here, not a wrapper: the string is not JSON.
	guard := guardFor("write")
	_, calls, err := guard.Write(`<<<ALLAIIS_TOOL_CALL>>>{"calls":[{"name":"write","input":"hello"}]}` + toolClose)
	if err != nil || len(calls) != 1 || calls[0].Function.Arguments != `{"input":"hello"}` {
		t.Fatalf("lone string: %+v %v", calls, err)
	}
}

func TestToolGuardStopsUnwrappingWhenEmpty(t *testing.T) {
	guard := guardFor("ping")
	_, calls, err := guard.Write(`<<<ALLAIIS_TOOL_CALL>>>{"calls":[{"name":"ping","input":{"arguments":{}}}]}` + toolClose)
	if err != nil || len(calls) != 1 || calls[0].Function.Arguments != `{}` {
		t.Fatalf("empty inner: %+v %v", calls, err)
	}
}

func TestToolGuardUnwrapsKeyValueWrapper(t *testing.T) {
	guard := guardFor("shell")
	_, calls, err := guard.Write(`<<<ALLAIIS_TOOL_CALL>>>{"calls":[{"name":"shell","input":"command=ls"}]}` + toolClose)
	if err != nil || len(calls) != 1 || calls[0].Function.Arguments != `{"command":"ls"}` {
		t.Fatalf("key=value: %+v %v", calls, err)
	}
}

func TestToolGuardKeepsProseInAWrapper(t *testing.T) {
	guard := guardFor("shell")
	_, calls, err := guard.Write(`<<<ALLAIIS_TOOL_CALL>>>{"calls":[{"name":"shell","input":"SELECT * FROM t WHERE a=1"}]}` + toolClose)
	if err != nil || len(calls) != 1 || calls[0].Function.Arguments != `{"input":"SELECT * FROM t WHERE a=1"}` {
		t.Fatalf("prose: %+v %v", calls, err)
	}
}

func TestToolGuardUnwrapsSeveralKeyValuePairs(t *testing.T) {
	guard := guardFor("shell")
	_, calls, err := guard.Write(`<<<ALLAIIS_TOOL_CALL>>>{"calls":[{"name":"shell","args":"command=ls timeout=5"}]}` + toolClose)
	if err != nil || len(calls) != 1 || calls[0].Function.Arguments != `{"command":"ls","timeout":"5"}` {
		t.Fatalf("pairs: %+v %v", calls, err)
	}
}

func TestToolCountersTrackReshaping(t *testing.T) {
	unwrapBefore, foldBefore, suffixBefore, unknownBefore :=
		toolCounters.unwrap.Load(), toolCounters.fold.Load(),
		toolCounters.suffix.Load(), toolCounters.unknown.Load()

	guard := guardFor("shell")
	if _, _, err := guard.Write(`<<<ALLAIIS_TOOL_CALL>>>{"calls":[{"name":"Shell","input":{"arguments":{"command":"ls"}}}]}` + toolClose); err != nil {
		t.Fatal(err)
	}
	guard = guardFor("shell")
	if _, _, err := guard.Write(`<<<ALLAIIS_TOOL_CALL>>>{"calls":[{"name":"shell command","arguments":{"command":"ls"}}]}` + toolClose); err != nil {
		t.Fatal(err)
	}
	guard = guardFor("shell")
	if _, _, err := guard.Write(`<<<ALLAIIS_TOOL_CALL>>>{"calls":[{"name":"bash","arguments":{"command":"ls"}}]}` + toolClose); err != nil {
		t.Fatal(err)
	}

	if got := toolCounters.unwrap.Load() - unwrapBefore; got != 3 {
		t.Fatalf("unwrap counter: %d", got)
	}
	if got := toolCounters.fold.Load() - foldBefore; got != 1 {
		t.Fatalf("fold counter: %d", got)
	}
	if got := toolCounters.suffix.Load() - suffixBefore; got != 1 {
		t.Fatalf("suffix counter: %d", got)
	}
	// The folded names are known again by the time the suffix pass runs, so only
	// the genuinely unknown bash counts here.
	if got := toolCounters.unknown.Load() - unknownBefore; got != 1 {
		t.Fatalf("unknown counter: %d", got)
	}
}

func TestToolGuardRemembersUnknownNames(t *testing.T) {
	for _, name := range []string{"uxtool_a", "uxtool_a", "uxtool_b"} {
		guard := guardFor("shell")
		_, calls, err := guard.Write(`<invoke name="` + name + `"><parameter name="command">ls</parameter></invoke>`)
		if err != nil || len(calls) != 1 || calls[0].Function.Name != name {
			t.Fatalf("%s: %+v %v", name, calls, err)
		}
	}
	names := ToolUnknownNames(0)
	index := func(name string) int {
		for i, got := range names {
			if got == name {
				return i
			}
		}
		return -1
	}
	if index("uxtool_a") < 0 || index("uxtool_b") < 0 {
		t.Fatalf("names not remembered: %v", names)
	}
	if index("uxtool_a") > index("uxtool_b") {
		t.Fatalf("names not ordered by frequency: %v", names)
	}
}

func TestToolUnknownNamesIsBounded(t *testing.T) {
	for i := 0; i < maxUnknownNames+8; i++ {
		noteUnknownName("tool" + strconv.Itoa(i))
	}
	if got := len(ToolUnknownNames(0)); got > maxUnknownNames {
		t.Fatalf("set not bounded: %d", got)
	}
	// Whatever still has a place moves to the top when it repeats.
	first := ToolUnknownNames(1)[0]
	noteUnknownName(first)
	if ToolUnknownNames(1)[0] != first {
		t.Fatalf("repeated name lost: %v", ToolUnknownNames(3))
	}
}

func TestToolNameStateSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	(&Gateway{}).WithStateDir(context.Background(), dir)

	for _, name := range []string{"sx_bash", "sx_bash", "sx_grep"} {
		guard := guardFor("shell")
		if _, _, err := guard.Write(`<invoke name="` + name + `"><parameter name="command">ls</parameter></invoke>`); err != nil {
			t.Fatal(err)
		}
	}
	toolNameFile.mu.Lock()
	toolNameFile.lastSave = time.Time{}
	toolNameFile.mu.Unlock()
	saveToolNameState()

	// Forget everything the way a restart would.
	toolCounters.fold.Store(0)
	toolCounters.suffix.Store(0)
	toolCounters.unknown.Store(0)
	unknownNames.mu.Lock()
	unknownNames.count = nil
	unknownNames.mu.Unlock()

	(&Gateway{}).WithStateDir(context.Background(), dir)

	_, unknown := ToolNameStats()
	if unknown < 2 {
		t.Fatalf("unknown count lost: %d", unknown)
	}
	names := ToolUnknownNames(0)
	index := func(name string) int {
		for i, got := range names {
			if got == name {
				return i
			}
		}
		return -1
	}
	// Other tests leave names behind, so only the relative order is checked.
	if index("sx_bash") < 0 || index("sx_grep") < 0 {
		t.Fatalf("names lost: %v", names)
	}
	if index("sx_bash") > index("sx_grep") {
		t.Fatalf("counts lost: %v", names)
	}
}

func TestToolNameStateIgnoresABrokenFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tool-names.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	unknownNames.mu.Lock()
	unknownNames.count = map[string]int{"sx_keep": 1}
	unknownNames.mu.Unlock()

	(&Gateway{}).WithStateDir(context.Background(), dir)

	if names := ToolUnknownNames(5); len(names) != 1 || names[0] != "sx_keep" {
		t.Fatalf("broken file clobbered memory: %v", names)
	}
	toolNameFile.mu.Lock()
	toolNameFile.path = ""
	toolNameFile.mu.Unlock()
}

func TestResetToolNameStateClearsEverything(t *testing.T) {
	dir := t.TempDir()
	g := (&Gateway{}).WithStateDir(context.Background(), dir)

	guard := guardFor("shell")
	if _, _, err := guard.Write(`<invoke name="rs_name"><parameter name="command">ls</parameter></invoke>`); err != nil {
		t.Fatal(err)
	}
	toolNameFile.mu.Lock()
	toolNameFile.lastSave = time.Time{}
	toolNameFile.mu.Unlock()
	saveToolNameState()
	file := filepath.Join(dir, "tool-names.json")
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("file not written: %v", err)
	}

	g.ResetToolNameState()

	if _, unknown := ToolNameStats(); unknown != 0 {
		t.Fatalf("counters kept: %d", unknown)
	}
	if names := ToolUnknownNames(0); len(names) != 0 {
		t.Fatalf("names kept: %v", names)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("file kept: %v", err)
	}
	toolNameFile.mu.Lock()
	toolNameFile.path = ""
	toolNameFile.mu.Unlock()
}

func TestWatcherFollowsAnOutsideReset(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	(&Gateway{}).WithStateDir(ctx, dir)

	guard := guardFor("shell")
	if _, _, err := guard.Write(`<invoke name="wx_gone"><parameter name="command">ls</parameter></invoke>`); err != nil {
		t.Fatal(err)
	}
	toolNameFile.mu.Lock()
	toolNameFile.lastSave = time.Time{}
	toolNameFile.mu.Unlock()
	saveToolNameState()
	if _, err := os.Stat(filepath.Join(dir, "tool-names.json")); err != nil {
		t.Fatalf("file not written: %v", err)
	}

	if err := os.Remove(filepath.Join(dir, "tool-names.json")); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * toolNameWatchEvery)
	for time.Now().Before(deadline) {
		if _, unknown := ToolNameStats(); unknown == 0 && len(ToolUnknownNames(0)) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("deletion not followed: %v", ToolUnknownNames(3))
}

func setToolNameTTL(t *testing.T, days int) {
	t.Helper()
	previous := toolNameTTL.Load()
	toolNameTTL.Store(int64(days) * 86400)
	t.Cleanup(func() { toolNameTTL.Store(previous) })
}

func seedUnknownName(name string, seen time.Time) {
	unknownNames.mu.Lock()
	defer unknownNames.mu.Unlock()
	if unknownNames.count == nil {
		unknownNames.count = map[string]int{}
	}
	if unknownNames.seen == nil {
		unknownNames.seen = map[string]time.Time{}
	}
	unknownNames.count[name] = 1
	unknownNames.seen[name] = seen
}

func TestToolNamesExpireOnSave(t *testing.T) {
	setToolNameTTL(t, 1)
	dir := t.TempDir()
	(&Gateway{}).WithStateDir(context.Background(), dir)
	seedUnknownName("ttl_old", time.Now().Add(-48*time.Hour))
	seedUnknownName("ttl_fresh", time.Now())

	toolNameFile.mu.Lock()
	toolNameFile.lastSave = time.Time{}
	toolNameFile.mu.Unlock()
	saveToolNameState()

	raw, err := os.ReadFile(filepath.Join(dir, "tool-names.json"))
	if err != nil {
		t.Fatal(err)
	}
	// A save keeps the expired name; the drop is explicit.
	if !strings.Contains(string(raw), "ttl_old") {
		t.Fatalf("expired name dropped on save: %s", raw)
	}
	if !strings.Contains(string(raw), "ttl_fresh") {
		t.Fatalf("fresh name lost: %s", raw)
	}
}

func TestLiveUnknownNamesSkipsExpired(t *testing.T) {
	setToolNameTTL(t, 1)
	seedUnknownName("lv_old", time.Now().Add(-48*time.Hour))
	seedUnknownName("lv_fresh", time.Now())

	live := LiveUnknownNames(0)
	has := func(name string) bool {
		for _, got := range live {
			if got == name {
				return true
			}
		}
		return false
	}
	if has("lv_old") {
		t.Fatalf("expired name listed: %v", live)
	}
	if !has("lv_fresh") {
		t.Fatalf("fresh name missing: %v", live)
	}
	if len(LiveUnknownNames(1)) != 1 {
		t.Fatal("limit not applied")
	}
}

func TestLiveUnknownNamesWithRetentionOff(t *testing.T) {
	setToolNameTTL(t, 0)
	seedUnknownName("lv_forever", time.Now().Add(-50*24*time.Hour))
	for _, name := range LiveUnknownNames(0) {
		if name == "lv_forever" {
			return
		}
	}
	t.Fatal("name dropped with retention off")
}

func TestToolNamesExpireOnLoad(t *testing.T) {
	setToolNameTTL(t, 1)
	dir := t.TempDir()
	stamp := time.Now().Add(-48 * time.Hour).UnixMilli()
	body := `{"unknown":1,"names":{"ttl_loaded":1},"seen":{"ttl_loaded":` + strconv.FormatInt(stamp, 10) + `}}`
	if err := os.WriteFile(filepath.Join(dir, "tool-names.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	(&Gateway{}).WithStateDir(context.Background(), dir)

	if names := ToolUnknownNames(0); len(names) != 0 {
		t.Fatalf("expired name loaded: %v", names)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "tool-names.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ttl_loaded") {
		t.Fatalf("expired name kept in the file: %s", raw)
	}
}

func TestToolNamesExpiryCanBeDisabled(t *testing.T) {
	setToolNameTTL(t, 0)
	dir := t.TempDir()
	(&Gateway{}).WithStateDir(context.Background(), dir)
	seedUnknownName("ttl_forever", time.Now().Add(-4000*24*time.Hour))

	toolNameFile.mu.Lock()
	toolNameFile.lastSave = time.Time{}
	toolNameFile.mu.Unlock()
	saveToolNameState()

	if names := ToolUnknownNames(0); len(names) == 0 {
		t.Fatal("name dropped with retention off")
	}
}

func TestToolNamesFromAnOldFileStayFresh(t *testing.T) {
	setToolNameTTL(t, 1)
	dir := t.TempDir()
	// No "seen" map at all: the file predates timestamps.
	if err := os.WriteFile(filepath.Join(dir, "tool-names.json"), []byte(`{"unknown":1,"names":{"ttl_nosee":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	(&Gateway{}).WithStateDir(context.Background(), dir)

	if names := ToolUnknownNames(0); len(names) != 1 || names[0] != "ttl_nosee" {
		t.Fatalf("old file dropped: %v", names)
	}
}

func TestMetricsReportToolRetention(t *testing.T) {
	setToolNameTTL(t, 7)
	summary := newMetrics().summary()
	if got := summary["tool_ttl_days"]; got != int64(7) {
		t.Fatalf("retention not reported: %v", got)
	}
	if got := summary["tool_ttl_seconds"]; got != int64(7*86400) {
		t.Fatalf("retention seconds not reported: %v", got)
	}
}

func TestDropExpiredToolNamesKeepsTheFresh(t *testing.T) {
	setToolNameTTL(t, 1)
	dir := t.TempDir()
	body := `{"folded":5,"unknown":9,"names":{"dx_fresh":1},"seen":{"dx_fresh":` +
		strconv.FormatInt(time.Now().UnixMilli(), 10) + `}}`
	path := filepath.Join(dir, "tool-names.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	g := (&Gateway{}).WithStateDir(context.Background(), dir)
	// A name that aged while the gateway was up, not present at load.
	seedUnknownName("dx_old", time.Now().Add(-48*time.Hour))

	if dropped := g.DropExpiredToolNames(); dropped != 1 {
		t.Fatalf("dropped %d, want 1", dropped)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "dx_old") {
		t.Fatalf("expired name kept in the file: %s", raw)
	}
	if !strings.Contains(string(raw), "dx_fresh") {
		t.Fatalf("fresh name lost: %s", raw)
	}
	if !strings.Contains(string(raw), `"unknown":9`) {
		t.Fatalf("counters changed: %s", raw)
	}
	if again := g.DropExpiredToolNames(); again != 0 {
		t.Fatalf("second drop removed %d", again)
	}
}

func TestDropExpiredToolNamesWithRetentionOff(t *testing.T) {
	setToolNameTTL(t, 0)
	g := &Gateway{}
	seedUnknownName("dx_forever", time.Now().Add(-50*24*time.Hour))
	if dropped := g.DropExpiredToolNames(); dropped != 0 {
		t.Fatalf("dropped %d with retention off", dropped)
	}
}

func TestExpiredToolNamesCountsWithoutDropping(t *testing.T) {
	setToolNameTTL(t, 1)
	seedUnknownName("ex_old", time.Now().Add(-48*time.Hour))
	seedUnknownName("ex_fresh", time.Now())

	summary := newMetrics().summary()
	count, ok := summary["tool_expired"].(int)
	if !ok {
		t.Fatalf("count missing: %v", summary["tool_expired"])
	}
	if count < 1 {
		t.Fatalf("expired not counted: %d", count)
	}
	// Counting must not drop: the old name is still listed.
	found := false
	for _, name := range ToolUnknownNames(0) {
		if name == "ex_old" {
			found = true
		}
	}
	if !found {
		t.Fatal("count dropped the name")
	}
}

func TestExpiredToolNamesWithRetentionOff(t *testing.T) {
	setToolNameTTL(t, 0)
	seedUnknownName("ex_forever", time.Now().Add(-50*24*time.Hour))
	if got := expiredToolNames(); got != 0 {
		t.Fatalf("counted %d with retention off", got)
	}
}

func TestToolNameTTLForAcceptsMinutes(t *testing.T) {
	previous := toolNameTTL.Load()
	t.Cleanup(func() { toolNameTTL.Store(previous) })
	(&Gateway{}).WithToolNameTTLFor(2 * time.Minute)

	seedUnknownName("mn_old", time.Now().Add(-3*time.Minute))
	seedUnknownName("mn_fresh", time.Now())

	if got := expiredToolNames(); got < 1 {
		t.Fatalf("minute retention not applied: %d", got)
	}
	for _, name := range LiveUnknownNames(0) {
		if name == "mn_old" {
			t.Fatalf("expired name listed: %v", LiveUnknownNames(0))
		}
	}
}
