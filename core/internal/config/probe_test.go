package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestToolNameTTLDefaultsAndParses(t *testing.T) {
	cfg, err := Load(writeConfig(t, `{"listen":"127.0.0.1:8420","models":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ToolNameTTLDays != DefaultToolNameTTLDays {
		t.Fatalf("default not applied: %d", cfg.ToolNameTTLDays)
	}

	cfg, err = Load(writeConfig(t, `{"listen":"127.0.0.1:8420","models":[],"tool_name_ttl_days":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ToolNameTTLDays != 0 {
		t.Fatalf("explicit zero lost: %d", cfg.ToolNameTTLDays)
	}
}

func TestToolNameTTLRange(t *testing.T) {
	for _, days := range []string{"-1", "3651"} {
		if _, err := Load(writeConfig(t, `{"listen":"127.0.0.1:8420","models":[],"tool_name_ttl_days":`+days+`}`)); err == nil {
			t.Fatalf("%s accepted", days)
		}
	}
}
