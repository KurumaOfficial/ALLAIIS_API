// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"strings"
	"unicode/utf8"
)

// stopCut cuts a reply where the first stop sequence begins. The web endpoint
// ignores the sequences, so the gateway does the cutting itself.
//
// As many runes as the longest sequence holds minus one are kept back, so a
// sequence split across two chunks is still found; without that a stream could
// leak the start of a stop word.
type stopCut struct {
	words []string
	hold  int
	buf   strings.Builder
	done  bool
	// matched is the sequence that ended the reply, so a protocol that names it
	// (Anthropic Messages) can report the caller's own string.
	matched string
}

func newStopCut(words []string) *stopCut {
	if len(words) == 0 {
		return nil
	}
	cut := &stopCut{words: words}
	for _, word := range words {
		if n := utf8.RuneCountInString(word); n > cut.hold {
			cut.hold = n - 1
		}
	}
	return cut
}

// feed adds a chunk and returns what may go out, plus whether the reply ended.
// Once it has ended every later chunk is swallowed.
func (c *stopCut) feed(chunk string) (string, bool) {
	if c.done {
		return "", true
	}
	c.buf.WriteString(chunk)
	text := c.buf.String()
	if at, word := earliestStop(text, c.words); at >= 0 {
		c.done = true
		c.matched = word
		c.buf.Reset()
		return text[:at], true
	}
	runes := []rune(text)
	if len(runes) <= c.hold {
		return "", false
	}
	keep := len(runes) - c.hold
	c.buf.Reset()
	c.buf.WriteString(string(runes[keep:]))
	return string(runes[:keep]), false
}

// flush returns whatever is still held back once the stream is over.
func (c *stopCut) flush() string {
	if c.done {
		return ""
	}
	c.done = true
	out := c.buf.String()
	c.buf.Reset()
	return out
}

// earliestStop is the byte index of the first stop word in text, or -1, plus
// the word itself.
func earliestStop(text string, words []string) (int, string) {
	best, hit := -1, ""
	for _, word := range words {
		at := strings.Index(text, word)
		if at >= 0 && (best < 0 || at < best) {
			best, hit = at, word
		}
	}
	return best, hit
}
