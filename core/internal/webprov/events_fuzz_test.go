// SPDX-License-Identifier: GPL-3.0-only

package webprov

import (
	"io"
	"strings"
	"testing"
)

// dribble is an io.Reader that hands out one byte at a time, so the decoder sees
// every possible chunk boundary a network could produce.
type dribble struct{ text string }

func (d *dribble) Read(p []byte) (int, error) {
	if d.text == "" {
		return 0, io.EOF
	}
	p[0] = d.text[0]
	d.text = d.text[1:]
	return 1, nil
}

// FuzzDecodeEvents feeds arbitrary frames in one piece and byte by byte. The
// decoder must terminate and must not panic on any prefix of a frame.
func FuzzDecodeEvents(f *testing.F) {
	seeds := []string{
		"",
		`{"kind":"content","content":"hi"}` + "\n",
		"data: {\"kind\":\"content\",\"content\":\"hi\"}\n\n",
		"event: response.output_text.delta\ndata: {\"delta\":\"x\"}\n\n",
		"data: [DONE]\n\n",
		`{"kind":"content","content":"half`,
		"data: {\"kind\":\"content\"\n\n",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		// The byte-at-a-time reader is quadratic on a huge input; boundaries are
		// what matter, and those show up small.
		if len(body) > 4096 {
			t.Skip()
		}
		for _, reader := range []io.Reader{strings.NewReader(body), &dribble{text: body}} {
			events := 0
			_ = decodeEvents(reader, func(Event) error {
				events++
				if events > len(body)+2 {
					t.Fatalf("runaway events %d for %q", events, body)
				}
				return nil
			})
		}
	})
}
