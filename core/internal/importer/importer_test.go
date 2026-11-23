// SPDX-License-Identifier: GPL-3.0-only

package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
)

func fixture() config.Config {
	cfg := config.Default()
	cfg.Models = []config.Route{
		{ID: "a", Provider: "deepseek"},
		{ID: "b", Provider: "deepseek"},
		{ID: "c", Provider: "venice"},
	}
	return cfg
}

func TestProvidersSelectsOrCollects(t *testing.T) {
	cfg := fixture()
	ids, err := Providers(cfg, "")
	if err != nil || strings.Join(ids, ",") != "deepseek,venice" {
		t.Fatalf("collected %v %v", ids, err)
	}
	if _, err := Providers(cfg, "nope"); err == nil {
		t.Fatal("unknown provider accepted")
	}
	ids, err = Providers(cfg, "venice")
	if err != nil || len(ids) != 1 || ids[0] != "venice" {
		t.Fatalf("selected %v %v", ids, err)
	}
}

func TestEnvReadsOnlyConfiguredKeys(t *testing.T) {
	cfg := fixture()
	t.Setenv("DEEPSEEK_API_KEY", "sk-deepseek")
	t.Setenv("VENICE_API_KEY", "sk-venice")
	entries := Env(cfg, []string{"deepseek", "venice"})
	if len(entries) != 2 {
		t.Fatalf("entries %+v", entries)
	}
	for _, entry := range entries {
		if entry.Label != "env" || entry.Key == "" || entry.Endpoint == "" {
			t.Fatalf("entry %+v", entry)
		}
	}
}

func TestOpenCodeReadsBuiltInEntries(t *testing.T) {
	cfg := fixture()
	path := filepath.Join(t.TempDir(), "auth.json")
	body := `{"deepseek":{"type":"api","key":"sk-from-file"},` +
		`"venice":{"type":"oauth","key":"ignored"},` +
		`"alibaba":{"type":"api","key":"sk-qwen"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := OpenCode(cfg, []string{"deepseek", "venice"}, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Provider != "deepseek" || entries[0].Key != "sk-from-file" ||
		entries[0].Label != "opencode" {
		t.Fatalf("entries %+v", entries)
	}
}

// TestOpenCodeMapsQwenToAlibaba pins the name mismatch between our provider id
// and the file's: a reader would otherwise silently import nothing.
func TestOpenCodeMapsQwenToAlibaba(t *testing.T) {
	cfg := fixture()
	cfg.Models = append(cfg.Models, config.Route{ID: "d", Provider: "qwen"})
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"alibaba":{"type":"api","key":"sk-qwen"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := OpenCode(cfg, []string{"qwen"}, path)
	if err != nil || len(entries) != 1 || entries[0].Key != "sk-qwen" {
		t.Fatalf("entries %+v err %v", entries, err)
	}
}

func TestOpenCodeRejectsBadFiles(t *testing.T) {
	cfg := fixture()
	dir := t.TempDir()
	cases := map[string]string{
		"invalid":  `{not json`,
		"list":     `[]`,
		"big":      `{"deepseek":{"type":"api","key":"` + strings.Repeat("x", 1<<20) + `"}}`,
		"not-file": "",
	}
	for name, body := range cases {
		path := filepath.Join(dir, name+".json")
		if name == "not-file" {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenCode(cfg, []string{"deepseek"}, path); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := OpenCode(cfg, []string{"deepseek"}, filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing file accepted")
	}
}
