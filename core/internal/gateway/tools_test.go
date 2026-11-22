// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	if _, _, err := guard.Write("<<<ALLAIIS_TOOL_CALL>>>{\"calls\":[{\"name\":\"other\",\"arguments\":{}}]}<<<END_TOOL_CALL>>>"); err == nil {
		t.Fatal("unknown tool accepted")
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
	if _, _, err := guard.Write(`<invoke name="other"><parameter name="x">1</parameter></invoke>`); err == nil {
		t.Fatal("unknown tool accepted")
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
