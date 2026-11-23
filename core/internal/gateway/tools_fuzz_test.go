// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"strings"
	"testing"
)

// FuzzToolGuard feeds arbitrary prose at arbitrary splits. The marker must never
// reach the client and no input may panic the parser; a fuzz run is cheap
// insurance for the most intricate code in the transport.
func FuzzToolGuard(f *testing.F) {
	seeds := []string{
		"plain prose",
		toolOpen + `{"calls":[{"name":"read_file","arguments":{"path":"a"}}]}` + toolClose,
		toolOpen + `{"calls":[{"name":"read_file","arguments":` + toolClose,
		"partial " + toolOpen,
		`<|DSML|invoke name="read_file"><|DSML|parameter name="path" string="true">a</|DSML|parameter></|DSML|invoke>`,
		`<tool_call>{"name":"read_file","arguments":{}}</tool_call>`,
		strings.Repeat("prose ", 50) + toolOpen + `{"calls":[]}` + toolClose + " tail",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, chunk string) {
		guard := &toolGuard{}
		var out strings.Builder
		feed := func(text string) {
			text, _, _ = guard.Write(text)
			out.WriteString(text)
		}
		// Whole chunk, then byte by byte, which is the split that broke parsers
		// before.
		feed(chunk)
		out.Reset()
		guard = &toolGuard{}
		for index := 0; index < len(chunk); index++ {
			feed(chunk[index : index+1])
		}
		if strings.Contains(out.String(), toolOpen) || strings.Contains(out.String(), toolClose) {
			t.Fatalf("marker leaked for %q: %q", chunk, out.String())
		}
	})
}
