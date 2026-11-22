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

func TestMaxRequestDefaultsAndParses(t *testing.T) {
	cfg, err := Load(writeConfig(t, `{"listen":"127.0.0.1:8420","models":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxRequestBytes() != DefaultMaxRequestMB<<20 {
		t.Fatalf("default not applied: %d", cfg.MaxRequestBytes())
	}

	cfg, err = Load(writeConfig(t, `{"listen":"127.0.0.1:8420","models":[],"max_request_mb":24}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxRequestBytes() != 24<<20 {
		t.Fatalf("configured size lost: %d", cfg.MaxRequestBytes())
	}
}

func TestMaxRequestRange(t *testing.T) {
	for _, mb := range []string{"-1", "129"} {
		if _, err := Load(writeConfig(t, `{"listen":"127.0.0.1:8420","models":[],"max_request_mb":`+mb+`}`)); err == nil {
			t.Fatalf("%s accepted", mb)
		}
	}
}

func TestQuotaFloorRange(t *testing.T) {
	if _, err := Load(writeConfig(t, `{"listen":"127.0.0.1:8420","models":[],"quota_floor":-1}`)); err != nil {
		t.Fatalf("-1 rejected: %v", err)
	}
	for _, floor := range []string{"-2", "1001"} {
		if _, err := Load(writeConfig(t, `{"listen":"127.0.0.1:8420","models":[],"quota_floor":`+floor+`}`)); err == nil {
			t.Fatalf("%s accepted", floor)
		}
	}
}
