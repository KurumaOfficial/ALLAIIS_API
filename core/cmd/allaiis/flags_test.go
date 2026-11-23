// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"errors"
	"flag"
	"io"
	"testing"
)

// The gui command falls through into the smoke flags, so a flag registered in
// both places panics only when the command line is parsed. Parsing --help
// reaches exactly that code and stops before the vault.
func TestCommandsAcceptTheirFlags(t *testing.T) {
	t.Setenv("ALLAIIS_DATA_DIR", t.TempDir())
	previous := flagOutput
	flagOutput = io.Discard
	t.Cleanup(func() { flagOutput = previous })
	cases := [][]string{
		{"gui", "--no-tools", "--help"},
		{"gui", "--smoke-every", "15m", "--smoke-webhook", "http://127.0.0.1:1/x", "--help"},
		{"smoke", "--no-tools", "--json", "--help"},
		{"up", "--log-file", "x.log", "--log-max-mb", "8", "--help"},
		{"doctor", "--help"},
		{"doctor", "--offline", "--help"},
		{"web", "e2ee", "--model", "e2ee-x", "--label", "pro", "--help"},
		{"smoke", "--allow", "model_not_found", "--help"},
		{"up", "--help"},
	}
	for _, args := range cases {
		if err := run(t.Context(), args); !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("%v: %v", args, err)
		}
	}
}
