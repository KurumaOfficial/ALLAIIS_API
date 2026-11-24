// SPDX-License-Identifier: GPL-3.0-only

package gateway

import "testing"

func TestStopCutRemembersTheMatchedWord(t *testing.T) {
	cut := newStopCut([]string{"\nUser:", "STOP"})
	if _, ended := cut.feed("answer STOP tail"); !ended || cut.matched != "STOP" {
		t.Fatalf("ended %v matched %q", ended, cut.matched)
	}
}

func TestStopCutCutsAtTheFirstWord(t *testing.T) {
	cut := newStopCut([]string{"\nUser:", "STOP"})
	out, ended := cut.feed("Hello there\nUser: again")
	if out != "Hello there" || !ended {
		t.Fatalf("got %q ended=%v", out, ended)
	}
	if rest, ended := cut.feed(" later"); rest != "" || !ended {
		t.Fatalf("content after the cut leaked: %q", rest)
	}
}

// TestStopCutFindsASplitWord pins the hold-back: a stop word arriving in two
// chunks must still cut, not stream through.
func TestStopCutFindsASplitWord(t *testing.T) {
	cut := newStopCut([]string{"END"})
	var out string
	for _, chunk := range []string{"one tw", "o thr", "ee EN", "D and more"} {
		piece, ended := cut.feed(chunk)
		out += piece
		if ended {
			break
		}
	}
	out += cut.flush()
	if out != "one two three " {
		t.Fatalf("got %q", out)
	}
}

func TestStopCutFlushesTheHeldTail(t *testing.T) {
	cut := newStopCut([]string{"END"})
	out, ended := cut.feed("short")
	if out != "sho" || ended {
		t.Fatalf("got %q ended=%v", out, ended)
	}
	if tail := cut.flush(); out+tail != "short" {
		t.Fatalf("tail lost: %q", out+tail)
	}
}

func TestStopCutKeepsTheLongestMatch(t *testing.T) {
	cut := newStopCut([]string{"a", "abc"})
	out, _ := cut.feed("xabc")
	if out != "x" {
		t.Fatalf("got %q", out)
	}
}
