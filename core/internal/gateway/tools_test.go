// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
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
