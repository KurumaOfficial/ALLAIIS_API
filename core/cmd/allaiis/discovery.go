// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/catalog"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

type catalogResult struct {
	Provider   string          `json:"provider"`
	Status     string          `json:"status"`
	Scope      string          `json:"scope,omitempty"`
	Credential string          `json:"credential,omitempty"`
	Error      string          `json:"error,omitempty"`
	Models     []catalog.Model `json:"models"`
}

func discoverModels(ctx context.Context, path, id, match, label string, withKeys bool) error {
	cfg, err := config.Load(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg = config.Default()
	} else if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if id != "" {
		provider, ok := cfg.Provider(id)
		if !ok {
			return fmt.Errorf("unknown provider %q", id)
		}
		withKeys = withKeys || label != "" || !provider.CatalogPublic
	}
	if label != "" && !config.ValidCredentialLabel(label) {
		return errors.New("invalid credential label")
	}

	var store *vault.Store
	if withKeys {
		passphrase, err := readPassphrase()
		if err != nil {
			return err
		}
		store, err = vault.Open(ctx, filepath.Join(filepath.Dir(path), "vault.sqlite"), passphrase)
		clear(passphrase)
		if err != nil {
			return fmt.Errorf("open vault (initialize it and add provider keys): %w", err)
		}
		defer store.Close()
	}
	if id != "" {
		provider, _ := cfg.Provider(id)
		result := discoverProvider(ctx, store, id, provider, match, label)
		if result.Status != "ok" {
			return fmt.Errorf("discover %s models: %s", id, result.Error)
		}
		return writeJSON(os.Stdout, result)
	}

	ids := cfg.ProviderIDs()
	results := make([]catalogResult, len(ids))
	workers := min(4, len(ids))
	var group sync.WaitGroup
	for worker := range workers {
		group.Go(func() {
			for i := worker; i < len(ids); i += workers {
				provider, _ := cfg.Provider(ids[i])
				results[i] = discoverProvider(ctx, store, ids[i], provider, match, "")
			}
		})
	}
	group.Wait()
	if err := writeJSON(os.Stdout, map[string]any{"providers": results}); err != nil {
		return err
	}
	return ctx.Err()
}

func discoverProvider(ctx context.Context, store *vault.Store, id string, provider config.Provider, match, label string) catalogResult {
	result := catalogResult{Provider: id}
	entry, status, err := catalogCredential(ctx, store, id, provider, label)
	if err != nil {
		result.Status, result.Error = status, err.Error()
		return result
	}
	if entry.Key != "" {
		result.Scope, result.Credential = "credential", entry.Label
	} else {
		result.Scope = "public"
	}
	if provider.Transport == config.TransportVeniceWeb {
		cred, err := webprov.ParseCredential(entry.Key)
		if err != nil {
			result.Status, result.Error = "error", err.Error()
			return result
		}
		models, err := webprov.New(60*time.Second).Catalog(ctx, cred)
		if err != nil {
			result.Status, result.Error = "error", err.Error()
			return result
		}
		for _, model := range models {
			if match != "" && !strings.Contains(strings.ToLower(model.ID), strings.ToLower(match)) &&
				!strings.Contains(strings.ToLower(model.Name), strings.ToLower(match)) {
				continue
			}
			result.Models = append(result.Models, catalog.Model{ID: model.ID, Name: model.Name, Type: model.Kind})
		}
		result.Status = "ok"
		return result
	}
	models, err := catalog.List(ctx, provider.CatalogURL(), provider.CatalogFormat, entry.Key, match)
	if err != nil {
		result.Status, result.Error = "error", err.Error()
		return result
	}
	result.Status, result.Models = "ok", models
	return result
}

func catalogCredential(ctx context.Context, store *vault.Store, id string, provider config.Provider, label string) (vault.Entry, string, error) {
	if store != nil {
		entries, err := store.Entries(ctx, id, provider.BaseURL)
		if err != nil {
			return vault.Entry{}, "error", errors.New("could not read stored provider credentials")
		}
		if label != "" {
			for _, entry := range entries {
				if entry.Label == label {
					return entry, "", nil
				}
			}
			return vault.Entry{}, "needs_key", errors.New("the requested credential label was not found")
		}
		if len(entries) == 1 {
			return entries[0], "", nil
		}
		if len(entries) > 1 {
			return vault.Entry{}, "needs_label", errors.New("select the provider and a credential with --label")
		}
	}
	if provider.CatalogPublic {
		return vault.Entry{}, "", nil
	}
	return vault.Entry{}, "needs_key", errors.New("add a provider key and use --with-keys")
}
