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
// TestAllowedResultTakesCodeOrStatus pins the flag a person types: 402 is the
// status, insufficient_balance the code, and both should excuse the probe.
func TestAllowedResultTakesCodeOrStatus(t *testing.T) {
	allowed := map[string]bool{"insufficient_balance": true, "404": true}
	for _, tc := range []struct {
		result smokeResult
		want   bool
	}{
		{smokeResult{Code: "insufficient_balance", Status: 402}, true},
		{smokeResult{Code: "model_not_found", Status: 404}, true},
		{smokeResult{Code: "upstream_error", Status: 502}, false},
		{smokeResult{Status: 404}, true},
	} {
		if got := allowedResult(allowed, tc.result); got != tc.want {
			t.Errorf("%+v: got %v want %v", tc.result, got, tc.want)
		}
	}
}

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
