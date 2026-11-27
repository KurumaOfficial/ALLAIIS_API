// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"strings"
	"testing"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
)

func TestToolCallLimit(t *testing.T) {
	t.Setenv("ALLAIIS_MAX_TOOL_CALLS", "12")
	if got := toolCallLimit(); got != 12 {
		t.Fatalf("override %d", got)
	}
	t.Setenv("ALLAIIS_MAX_TOOL_CALLS", "not-a-number")
	if got := toolCallLimit(); got != defaultToolCallLimit {
		t.Fatalf("fallback %d", got)
	}
	if note := toolHistoryNote(41); !strings.Contains(note, "41 tool calls") {
		t.Fatalf("note %q", note)
	}
}

func TestToolCallsInHistory(t *testing.T) {
	cases := []struct {
		name     string
		protocol config.Protocol
		body     string
		want     int
	}{
		{
			name:     "chat parallel calls",
			protocol: config.Chat,
			body: `{"messages":[{"role":"assistant","tool_calls":[{"id":"a"},{"id":"b"}]},` +
				`{"role":"assistant","tool_calls":[{"id":"c"}]},{"role":"user","content":"go"}]}`,
			want: 3,
		},
		{
			name:     "chat legacy function",
			protocol: config.Chat,
			body:     `{"messages":[{"role":"assistant","function_call":{"name":"read"}}]}`,
			want:     1,
		},
		{
			name:     "messages tool use",
			protocol: config.Messages,
			body: `{"messages":[{"role":"assistant","content":[{"type":"text"},{"type":"tool_use"},{"type":"tool_use"}]},` +
				`{"role":"user","content":"go"}]}`,
			want: 2,
		},
		{
			name:     "responses function calls",
			protocol: config.Responses,
			body:     `{"input":[{"type":"message"},{"type":"function_call"},{"type":"function_call_output"}]}`,
			want:     1,
		},
		{
			name:     "chat empty",
			protocol: config.Chat,
			body:     `{"messages":[{"role":"user","content":"hi"}]}`,
			want:     0,
		},
		{
			name:     "malformed",
			protocol: config.Chat,
			body:     `{`,
			want:     0,
		},
		{
			name:     "responses string input",
			protocol: config.Responses,
			body:     `{"input":"hello"}`,
			want:     0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolCallsInHistory(tc.protocol, []byte(tc.body)); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}
