// SPDX-License-Identifier: GPL-3.0-only

package gateway

import "testing"

func TestStripToolNoise(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"normal prose", "normal prose"},
		{"before\n|DSML| calls>\n", "before\n"},
		{"before\n｜DSML｜ calls>\n", "before\n"},
		{"before\n<|DSML| function_calls>\n", "before\n"},
		{"before\n</|DSML| calls>\n", "before\n"},
		{"before\n calls>\n", "before\n"},
		{"before\nfunction_calls>\n", "before\n"},
		{"the function calls> are cheap\n", "the function calls> are cheap\n"},
	}
	for _, tc := range cases {
		if got := stripToolNoise(tc.in); got != tc.want {
			t.Errorf("stripToolNoise(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
