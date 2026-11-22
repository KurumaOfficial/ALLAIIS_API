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

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
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
	if err := showToolNames(&out, filepath.Join(dir, "tool-names.json"), dir, "test", 0, "config", false); err != nil {
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
	dir := t.TempDir()
	if err := showToolNames(&out, filepath.Join(dir, "tool-names.json"), dir, "test", 0, "config", false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "No tool name record") {
		t.Fatalf("unexpected output: %q", text)
	}
	// Even with no record the path is the answer: it shows which profile was read.
	if !strings.Contains(text, filepath.Join(dir, "tool-names.json")) {
		t.Fatalf("path missing: %q", text)
	}
}

func TestToolsShowRejectsABrokenRecord(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, "{nope")
	var out bytes.Buffer
	if err := showToolNames(&out, filepath.Join(dir, "tool-names.json"), dir, "test", 0, "config", false); err == nil {
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
	err := resetToolNames(&out, filepath.Join(dir, "tool-names.json"), dir, "test", 0, "config", false)
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
	// A reset writes to a path from the environment, so it names the target.
	if !strings.Contains(out.String(), "File: "+filepath.Join(dir, "tool-names.json")) {
		t.Fatalf("target missing: %q", out.String())
	}
}

func TestToolsResetWithoutARecord(t *testing.T) {
	var out bytes.Buffer
	if err := resetToolNames(&out, filepath.Join(t.TempDir(), "tool-names.json"), "", "test", 0, "config", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No record") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestAgeRendering(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	day := 24 * time.Hour
	cases := []struct {
		stamp int64
		ttl   time.Duration
		want  string
	}{
		{0, 30 * day, "unknown"},
		{now.UnixMilli() - 30_000, 30 * day, "just now"},
		{now.UnixMilli() - 5*60_000, 30 * day, "5m"},
		{now.UnixMilli() - 3*3_600_000, 30 * day, "3h"},
		{now.UnixMilli() - 9*24*3_600_000, 30 * day, "9d"},
		{now.UnixMilli() - 40*24*3_600_000, 30 * day, expiredLabel},
		{now.UnixMilli() - 40*24*3_600_000, 0, "40d"},
		{now.UnixMilli() - 90_000, 60 * time.Second, expiredLabel},
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
	if err := showToolNames(&out, filepath.Join(dir, "tool-names.json"), dir, "test", 30*24*time.Hour, "config", false); err != nil {
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
	if err := showToolNames(&out, filepath.Join(dir, "tool-names.json"), dir, "test", 0, "config", false); err != nil {
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
	if err := resetToolNames(&out, path, dir, "test", 30*24*time.Hour, "config", true); err != nil {
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
	if err := resetToolNames(&out, path, dir, "test", 30*24*time.Hour, "config", true); err != nil {
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
	if err := resetToolNames(&out, path, dir, "test", 0, "config", true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "retention is off") {
		t.Fatalf("unexpected output: %q", out.String())
	}
	// Off without a way back reads as breakage.
	if !strings.Contains(out.String(), TTLEnv+"=30d") {
		t.Fatalf("override hint missing: %q", out.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file removed: %v", err)
	}
}

func TestRetentionTTLFromEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"listen":"127.0.0.1:8420","models":[],"tool_name_ttl_days":30}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(TTLEnv, "45s")
	if got := RetentionTTL(path); got != 45*time.Second {
		t.Fatalf("override ignored: %v", got)
	}
	t.Setenv(TTLEnv, "nonsense")
	if got := RetentionTTL(path); got != 30*24*time.Hour {
		t.Fatalf("bad override not ignored: %v", got)
	}
	t.Setenv(TTLEnv, "0s")
	if got := RetentionTTL(path); got != 0 {
		t.Fatalf("zero override lost: %v", got)
	}
}

func TestRetentionTTLPrefersTheEnvForARunningGateway(t *testing.T) {
	cfg := config.Default()
	cfg.ToolNameTTLDays = 30
	t.Setenv(TTLEnv, "2m")
	if got := retentionTTL(cfg, "unused"); got != 2*time.Minute {
		t.Fatalf("override ignored: %v", got)
	}
	os.Unsetenv(TTLEnv)
	if got := retentionTTL(cfg, "unused"); got != 30*24*time.Hour {
		t.Fatalf("config value lost: %v", got)
	}
}

func TestToolsShowStatesTheRetention(t *testing.T) {
	dir := t.TempDir()
	body := `{"unknown":1,"names":{"x":1},"seen":{"x":` + strconv.FormatInt(time.Now().UnixMilli(), 10) + `}}`
	if err := os.WriteFile(filepath.Join(dir, "tool-names.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := showToolNames(&out, filepath.Join(dir, "tool-names.json"), dir, "ALLAIIS_DATA_DIR", 5*time.Minute, "config", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Retention: 5m from config") {
		t.Fatalf("retention missing: %q", out.String())
	}
	if !strings.Contains(out.String(), "File: ") {
		t.Fatalf("file path missing: %q", out.String())
	}
	if !strings.Contains(out.String(), "(from ALLAIIS_DATA_DIR)") {
		t.Fatalf("data dir origin missing: %q", out.String())
	}
}

func TestRetentionText(t *testing.T) {
	cases := map[time.Duration]string{
		0:                   "off (names are kept forever)",
		90 * time.Second:    "1m from config",
		5 * time.Minute:     "5m from config",
		3 * time.Hour:       "3h from config",
		30 * 24 * time.Hour: "30d from config",
	}
	for ttl, want := range cases {
		if got := retentionText(ttl, "config"); got != want {
			t.Fatalf("retentionText(%v) = %q, want %q", ttl, got, want)
		}
	}
}

func TestToolsShowExplainsAnOffRetention(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, `{"unknown":1,"names":{"kept":1}}`)
	var out bytes.Buffer
	if err := showToolNames(&out, filepath.Join(dir, "tool-names.json"), dir, "test", 0, "config", false); err != nil {
		t.Fatal(err)
	}
	// The view is read-only, but an off retention must not look like breakage.
	if !strings.Contains(out.String(), TTLEnv+"=30d") {
		t.Fatalf("hint missing: %q", out.String())
	}
}
