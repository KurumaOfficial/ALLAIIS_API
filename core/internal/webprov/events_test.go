// SPDX-License-Identifier: GPL-3.0-only

package webprov

import (
	"strings"
	"testing"
)

// TestDecodeEventsKeepsContentWithReasoning covers the transition frame Venice
// sends when the model stops thinking and starts answering: the same frame has
// both the last reasoning token and the first answer token. Keeping only the
// reasoning dropped the first characters of the answer.
func TestDecodeEventsKeepsContentWithReasoning(t *testing.T) {
	stream := strings.Join([]string{
		`{"kind":"content","content":"","reasoning_content":"The"}`,
		`{"kind":"content","content":"","reasoning_content":" user says hi."}`,
		`{"kind":"content","content":"При","reasoning_content":" needed."}`,
		`{"kind":"content","content":"вет! Ч","reasoning_content":""}`,
		`{"kind":"content","content":"ем могу","reasoning_content":""}`,
	}, "\n")
	var got []Event
	if err := decodeEvents(strings.NewReader(stream), func(event Event) error {
		got = append(got, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []Event{
		{Kind: "reasoning", Content: "The"},
		{Kind: "reasoning", Content: " user says hi."},
		{Kind: "reasoning", Content: " needed."},
		{Kind: "content", Content: "При"},
		{Kind: "content", Content: "вет! Ч"},
		{Kind: "content", Content: "ем могу"},
	}
	if len(got) != len(want) {
		t.Fatalf("events %#v", got)
	}
	for i := range want {
		if got[i].Kind != want[i].Kind || got[i].Content != want[i].Content {
			t.Fatalf("event %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}
