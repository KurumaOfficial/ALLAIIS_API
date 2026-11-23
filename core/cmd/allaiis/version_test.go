// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"strings"
	"testing"
)

// TestVersionFlag pins the thing a script or a bug report reaches for first.
func TestVersionFlag(t *testing.T) {
	for _, flag := range []string{"version", "-v", "--version"} {
		out := capture(t, func() {
			if err := run(t.Context(), []string{flag}); err != nil {
				t.Fatalf("%s: %v", flag, err)
			}
		})
		if !strings.Contains(out, "allaiis "+version) {
			t.Fatalf("%s printed %q", flag, out)
		}
	}
}
