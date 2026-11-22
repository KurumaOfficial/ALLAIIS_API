// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeRecord(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "tool-names.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestToolsShowPrintsCountsAndOrder(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, `{"folded":2,"suffixed":1,"unknown":3,"names":{"bash":2,"grep":1}}`)

	var out bytes.Buffer
	if err := showToolNames(&out, filepath.Join(dir, "tool-names.json"), 0, false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Repaired names: 3", "Unknown names: 3", "bash", "grep"} {
		if !strings.Contains(text, want) {
			t.Fatalf("%q missing in %q", want, text)
		}
	}
	if strings.Index(text, "bash") > strings.Index(text, "grep") {
		t.Fatalf("names not ordered by frequency: %q", text)
	}
}

func TestToolsShowWithoutARecord(t *testing.T) {
	var out bytes.Buffer
	if err := showToolNames(&out, filepath.Join(t.TempDir(), "tool-names.json"), 0, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No tool name record") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestToolsShowRejectsABrokenRecord(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, "{nope")
	var out bytes.Buffer
	if err := showToolNames(&out, filepath.Join(dir, "tool-names.json"), 0, false); err == nil {
		t.Fatal("broken record accepted")
	}
}

func TestToolsReset(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, `{"unknown":1,"names":{"bash":1}}`)
	if err := os.WriteFile(filepath.Join(dir, "tool-names.json.tmp"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := resetToolNames(&out, filepath.Join(dir, "tool-names.json"), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tool-names.json", "tool-names.json.tmp"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s kept", name)
		}
	}
	if !strings.Contains(out.String(), "cleared") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestToolsResetWithoutARecord(t *testing.T) {
	var out bytes.Buffer
	if err := resetToolNames(&out, filepath.Join(t.TempDir(), "tool-names.json"), 0, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No record") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestAgeRendering(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	cases := []struct {
		stamp int64
		ttl   int
		want  string
	}{
		{0, 30, "unknown"},
		{now.UnixMilli() - 30_000, 30, "just now"},
		{now.UnixMilli() - 5*60_000, 30, "5m"},
		{now.UnixMilli() - 3*3_600_000, 30, "3h"},
		{now.UnixMilli() - 9*24*3_600_000, 30, "9d"},
		{now.UnixMilli() - 40*24*3_600_000, 30, expiredLabel},
		{now.UnixMilli() - 40*24*3_600_000, 0, "40d"},
	}
	for _, item := range cases {
		if got := age(now, item.stamp, item.ttl); got != item.want {
			t.Fatalf("age(%d, %d) = %q, want %q", item.stamp, item.ttl, got, item.want)
		}
	}
}

func TestToolsShowMarksExpiredNames(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-40 * 24 * time.Hour).UnixMilli()
	body := `{"unknown":1,"names":{"stale":1},"seen":{"stale":` + strconv.FormatInt(old, 10) + `}}`
	if err := os.WriteFile(filepath.Join(dir, "tool-names.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := showToolNames(&out, filepath.Join(dir, "tool-names.json"), 30, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), expiredLabel) || !strings.Contains(out.String(), "1 expired") {
		t.Fatalf("expired name not marked: %q", out.String())
	}
}

func TestToolsShowPrintsAge(t *testing.T) {
	dir := t.TempDir()
	stamp := time.Now().Add(-5 * time.Hour).UnixMilli()
	body := `{"unknown":1,"names":{"aged":1},"seen":{"aged":` + strconv.FormatInt(stamp, 10) + `}}`
	if err := os.WriteFile(filepath.Join(dir, "tool-names.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := showToolNames(&out, filepath.Join(dir, "tool-names.json"), 0, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "LAST SEEN") || !strings.Contains(out.String(), "5h") {
		t.Fatalf("age not printed: %q", out.String())
	}
}

func TestToolsResetDropsOnlyExpired(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-40 * 24 * time.Hour).UnixMilli()
	fresh := time.Now().UnixMilli()
	body := `{"unknown":2,"names":{"ghost":1,"kept":1},"seen":{"ghost":` + strconv.FormatInt(old, 10) +
		`,"kept":` + strconv.FormatInt(fresh, 10) + `}}`
	path := filepath.Join(dir, "tool-names.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := resetToolNames(&out, path, 30, true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ghost") {
		t.Fatalf("expired name kept: %s", raw)
	}
	if !strings.Contains(string(raw), "kept") {
		t.Fatalf("fresh name lost: %s", raw)
	}
	if !strings.Contains(out.String(), "Dropped 1") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestToolsResetExpiredWithoutExpiredNames(t *testing.T) {
	dir := t.TempDir()
	body := `{"unknown":1,"names":{"kept":1},"seen":{"kept":` + strconv.FormatInt(time.Now().UnixMilli(), 10) + `}}`
	path := filepath.Join(dir, "tool-names.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := resetToolNames(&out, path, 30, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No expired names") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestToolsResetExpiredWithRetentionOff(t *testing.T) {
	dir := t.TempDir()
	body := `{"unknown":1,"names":{"kept":1},"seen":{"kept":1}}`
	path := filepath.Join(dir, "tool-names.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := resetToolNames(&out, path, 0, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Retention is off") {
		t.Fatalf("unexpected output: %q", out.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file removed: %v", err)
	}
}
