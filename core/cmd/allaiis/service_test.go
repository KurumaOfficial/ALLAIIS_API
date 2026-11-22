// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestServiceInstallWritesALoginScript(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the startup folder is Windows-only")
	}
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)
	t.Setenv("ALLAIIS_VAULT_PASSPHRASE", "passphrase-for-the-task")
	dir := t.TempDir()

	if err := runService(t.Context(), dir, []string{"install"}); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", serviceName+".cmd")
	raw, err := os.ReadFile(launcher)
	if err != nil {
		t.Fatalf("launcher: %v", err)
	}
	if !strings.Contains(string(raw), filepath.Join(dir, "service.cmd")) {
		t.Fatalf("launcher does not call the script: %q", raw)
	}
	script, err := os.ReadFile(filepath.Join(dir, "service.cmd"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ALLAIIS_VAULT_PASSPHRASE=passphrase-for-the-task", " gui ", "--no-open", "--log-file", ":run", "goto run"} {
		if !strings.Contains(string(script), want) {
			t.Fatalf("%q missing in %q", want, script)
		}
	}

	if err := runService(t.Context(), dir, []string{"remove"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(launcher); !os.IsNotExist(err) {
		t.Fatal("launcher left behind")
	}
}
