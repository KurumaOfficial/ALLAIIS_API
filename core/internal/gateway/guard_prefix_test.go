// SPDX-License-Identifier: GPL-3.0-only

package gateway

import "testing"

func TestGuardKeepsPlainPrefix(t *testing.T) {
	guard := guardFor("read_file")
	text, calls, err := guard.Write("Привет!")
	if err != nil || text != "Привет!" || len(calls) != 0 {
		t.Fatalf("text=%q calls=%v err=%v", text, calls, err)
	}
}
