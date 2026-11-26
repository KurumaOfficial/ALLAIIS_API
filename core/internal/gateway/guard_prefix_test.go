// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"strings"
	"testing"
)

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
