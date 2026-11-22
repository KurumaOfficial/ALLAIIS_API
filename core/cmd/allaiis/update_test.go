// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWantedSumFindsTheFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("aaa  allaiis_windows_amd64.exe\nbbb  allaiis_linux_amd64\n"))
	}))
	defer server.Close()

	sum, err := wantedSum(context.Background(), server.Client(), server.URL, "allaiis_linux_amd64")
	if err != nil || sum != "bbb" {
		t.Fatalf("%q %v", sum, err)
	}
	if _, err := wantedSum(context.Background(), server.Client(), server.URL, "absent"); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestDownloadWritesTheBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("binary"))
	}))
	defer server.Close()

	target := filepath.Join(t.TempDir(), "fresh.exe")
	if err := download(context.Background(), server.Client(), server.URL, target); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "binary" {
		t.Fatalf("%q %v", raw, err)
	}
	// Windows has no execute bit; elsewhere the owner bit must be set.
	if info, _ := os.Stat(target); runtime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("not executable: %v", info.Mode())
	}
}

func TestSwapBinaryReplacesAndKeepsTheOld(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "allaiis.exe")
	fresh := executable + ".new"
	if err := os.WriteFile(executable, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fresh, []byte("new"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := swapBinary(executable, fresh); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(executable)
	if err != nil || string(raw) != "new" {
		t.Fatalf("%q %v", raw, err)
	}
	if old, err := os.ReadFile(executable + ".old"); err != nil || string(old) != "old" {
		t.Fatalf("old binary lost: %q %v", old, err)
	}
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Fatal("staging file left")
	}
}

func TestUpdateRejectsUnknownArguments(t *testing.T) {
	if err := runUpdate(context.Background(), []string{"--nope"}); err == nil ||
		!strings.Contains(err.Error(), "unknown") {
		t.Fatalf("accepted: %v", err)
	}
}
