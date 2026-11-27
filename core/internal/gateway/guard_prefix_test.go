// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"strings"
	"testing"
)

func TestGuardParsesMalformedOpeningMarker(t *testing.T) {
	guard := guardFor("grep")
	block := `<</ALLAIIS_TOOL_CALL>>>{"calls":[{"name":"grep","arguments":{"path":"a","pattern":"b"}}]}`
	text, calls, err := guard.Write(block)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) == 0 {
		text, calls, err = guard.Flush()
		if err != nil {
			t.Fatal(err)
		}
	}
	if text != "" || len(calls) != 1 || calls[0].Function.Name != "grep" {
		t.Fatalf("text=%q calls=%#v", text, calls)
	}
}

func TestGuardStripsTrailingParameterTag(t *testing.T) {
	guard := guardFor("browse_console")
	text, calls, err := guard.Write(`<call name="browse_console"><parameter name="action">get</parameter>` + "\n" + `</parameter></call>`)
	if err != nil || text != "" || len(calls) != 1 {
		t.Fatalf("text=%q calls=%#v err=%v", text, calls, err)
	}
	if calls[0].Function.Arguments != `{"action":"get"}` {
		t.Fatalf("args=%s", calls[0].Function.Arguments)
	}
}

func TestGuardAcceptsCallTagWithAttributes(t *testing.T) {
	guard := guardFor("browse_eval")
	text, calls, err := guard.Write(`<call name="browse_eval" tab="1820148515"><parameter name="expression">1+1</parameter></call>`)
	if err != nil || text != "" || len(calls) != 1 {
		t.Fatalf("text=%q calls=%#v err=%v", text, calls, err)
	}
	if calls[0].Function.Name != "browse_eval" ||
		calls[0].Function.Arguments != `{"expression":"1+1","tab":1820148515}` {
		t.Fatalf("call=%#v", calls[0])
	}
}

func TestToolEnvelopeExplainsBrowseTabNumbers(t *testing.T) {
	tools := []toolSpec{}
	shape := toolSpec{}
	shape.Type = "function"
	shape.Function.Name = "browse_snapshot"
	shape.Function.Description = "Snapshot"
	shape.Function.Parameters = []byte(`{"type":"object","properties":{"tab":{"type":["integer","string"]}}}`)
	tools = append(tools, shape)
	envelope := toolEnvelope("system", tools)
	if !strings.Contains(envelope, "pass `tab` as a JSON number") {
		t.Fatalf("missing browse tab note: %s", envelope)
	}
	if !strings.Contains(envelope, "A rejected argument is not a missing tool") {
		t.Fatalf("missing tool-error guidance: %s", envelope)
	}
}

func TestNormalizeToolArgumentsMakesNumericBrowseTabANumber(t *testing.T) {
	call := toolCall{}
	call.Function.Name = "browse_snapshot"
	call.Function.Arguments = `{"tab":"1820148504","maxItems":60}`
	got := normalizeToolArguments(call.Function.Name, call.Function.Arguments)
	if got != `{"maxItems":60,"tab":1820148504}` {
		t.Fatalf("got %s", got)
	}
	if slug := normalizeToolArguments("browse_snapshot", `{"tab":"work-mail"}`); slug != `{"tab":"work-mail"}` {
		t.Fatalf("slug changed: %s", slug)
	}
	if other := normalizeToolArguments("read_file", `{"tab":"12"}`); other != `{"tab":"12"}` {
		t.Fatalf("non-browse call changed: %s", other)
	}
}

func TestNormalizeToolArgumentsLeavesMalformedInputAlone(t *testing.T) {
	if got := normalizeToolArguments("browse_snapshot", `{"tab":`); got != `{"tab":` {
		t.Fatalf("got %s", got)
	}
}

func TestGuardKeepsPlainPrefix(t *testing.T) {
	guard := guardFor("read_file")
	text, calls, err := guard.Write("Привет!")
	if err != nil || text != "Привет!" || len(calls) != 0 {
		t.Fatalf("text=%q calls=%v err=%v", text, calls, err)
	}
}
