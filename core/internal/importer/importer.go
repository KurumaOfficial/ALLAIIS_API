// SPDX-License-Identifier: GPL-3.0-only

package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
)

func Providers(cfg config.Config, selected string) ([]string, error) {
	if selected != "" {
		if _, ok := cfg.Provider(selected); !ok {
			return nil, fmt.Errorf("unknown provider %q", selected)
		}
		return []string{selected}, nil
	}

	var ids []string
	for _, model := range cfg.Models {
		ids = append(ids, model.Provider)
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

func Env(cfg config.Config, providerIDs []string) []vault.Entry {
	var entries []vault.Entry
	for _, id := range providerIDs {
		provider, _ := cfg.Provider(id)
		if key := os.Getenv(provider.KeyEnv); key != "" {
			entries = append(entries, vault.Entry{Provider: id, Endpoint: provider.BaseURL, Label: "env", Key: key})
		}
	}
	return entries
}

func OpenCode(cfg config.Config, providerIDs []string, path string) ([]vault.Entry, error) {
	if path == "" {
		dir := os.Getenv("XDG_DATA_HOME")
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			dir = filepath.Join(home, ".local", "share")
		}
		path = filepath.Join(dir, "opencode", "auth.json")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	const maxAuthFile = 1 << 20
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxAuthFile {
		return nil, errors.New("OpenCode credentials must be a regular file of at most 1 MiB")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxAuthFile+1))
	if err != nil {
		return nil, err
	}
	var auth map[string]json.RawMessage
	if len(content) > maxAuthFile || json.Unmarshal(content, &auth) != nil || auth == nil {
		return nil, errors.New("invalid OpenCode credential file")
	}
	builtInAuthIDs := map[string]string{
		"openai": "openai", "deepseek": "deepseek", "zai": "zai", "venice": "venice",
		"qwen": "alibaba", "gemini": "google", "opencode": "opencode", "opencode-go": "opencode-go",
	}
	var entries []vault.Entry
	for _, id := range providerIDs {
		authID, ok := builtInAuthIDs[id]
		if !ok {
			continue
		}
		raw, ok := auth[authID]
		if !ok {
			continue
		}
		var credential struct {
			Type string `json:"type"`
			Key  string `json:"key"`
		}
		if json.Unmarshal(raw, &credential) != nil {
			return nil, fmt.Errorf("invalid OpenCode entry for %q", authID)
		}
		if credential.Type == "api" && credential.Key != "" {
			provider, _ := cfg.Provider(id)
			entries = append(entries, vault.Entry{Provider: id, Endpoint: provider.BaseURL, Label: "opencode", Key: credential.Key})
		}
	}
	return entries, nil
}
