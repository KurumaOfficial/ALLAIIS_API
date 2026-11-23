// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"strings"
	"testing"
)

// TestPlanLineNamesTheCreditBucket pins the line printed right after a capture:
// a zero credit balance must be visible then, not on the first paid call.
func TestPlanLineNamesTheCreditBucket(t *testing.T) {
	line := planLine(map[string]any{
		"chat": map[string]any{"remaining": float64(9685)},
		"account": map[string]any{
			"userType": "PRO",
			"bundledCreditsUsage": map[string]any{
				"availableCredits": float64(0), "monthlyRefillCredits": float64(100),
			},
		},
	})
	for _, want := range []string{"PRO", "9685", "0/100", "402"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q lacks %q", line, want)
		}
	}
	if line := planLine(map[string]any{"chat": map[string]any{"remaining": float64(4)}}); !strings.Contains(line, "4") {
		t.Fatalf("anonymous line %q", line)
	}
}
