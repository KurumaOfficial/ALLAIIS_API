// SPDX-License-Identifier: GPL-3.0-only

package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/gofrs/flock"
)

func AddModel(ctx context.Context, path string, route Route) (bool, error) {
	added := false
	err := edit(ctx, path, func(cfg *Config) error {
		if route.Privacy == "" {
			route.Privacy = Consumer
		}
		if route.Protocol == "chat" {
			route.Protocol = Chat
		}
		for _, existing := range cfg.Models {
			if existing.ID == route.ID {
				if existing == route {
					return nil
				}
				return fmt.Errorf("model alias %q already points to a different route", route.ID)
			}
		}
		cfg.Models = append(cfg.Models, route)
		added = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return added, nil
}

// RemoveModel deletes an alias and reports whether it existed.
func RemoveModel(ctx context.Context, path, id string) (bool, error) {
	removed := false
	err := edit(ctx, path, func(cfg *Config) error {
		kept := cfg.Models[:0]
		for _, route := range cfg.Models {
			if route.ID == id {
				removed = true
				continue
			}
			kept = append(kept, route)
		}
		cfg.Models = kept
		if !removed {
			return fmt.Errorf("model alias %q is not configured", id)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return removed, nil
}

// UpdateProvider registers or replaces a custom provider.
func UpdateProvider(ctx context.Context, path, id string, provider Provider) error {
	return edit(ctx, path, func(cfg *Config) error {
		if cfg.Providers == nil {
			cfg.Providers = map[string]Provider{}
		}
		cfg.Providers[id] = provider
		return nil
	})
}

// SetModelDisabled mutes or unmutes an alias without deleting it.
func SetModelDisabled(ctx context.Context, path, id string, disabled bool) error {
	return edit(ctx, path, func(cfg *Config) error {
		for index := range cfg.Models {
			if cfg.Models[index].ID == id {
				cfg.Models[index].Disabled = disabled
				return nil
			}
		}
		return fmt.Errorf("model alias %q is not configured", id)
	})
}

// SetMinPrivacy updates the gateway-wide privacy floor.
func SetMinPrivacy(ctx context.Context, path string, level Privacy) error {
	return edit(ctx, path, func(cfg *Config) error {
		cfg.MinPrivacy = level
		return nil
	})
}

func edit(ctx context.Context, path string, change func(*Config) error) error {
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	lock := flock.New(path+".lock", flock.SetPermissions(0600))
	defer lock.Close()
	lockContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockContext, 50*time.Millisecond)
	if err != nil {
		return fmt.Errorf("lock configuration: %w", err)
	}
	if !locked {
		return errors.New("configuration is being edited by another process")
	}

	cfg, err := Load(path)
	if err != nil {
		return err
	}
	if err := change(&cfg); err != nil {
		return err
	}
	if err := cfg.validate(); err != nil {
		return err
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if len(body)+1 > maxConfigBytes {
		return errors.New("edited configuration would exceed 1 MiB")
	}
	return replaceConfig(ctx, path, append(body, '\n'))
}

func replaceConfig(ctx context.Context, path string, body []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}

	// Windows can refuse replacement while another process has the file open.
	const windowsSharingViolation syscall.Errno = 32
	retryUntil := time.Now().Add(time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := os.Rename(file.Name(), path)
		if runtime.GOOS != "windows" || time.Now().After(retryUntil) ||
			(!errors.Is(err, os.ErrPermission) && !errors.Is(err, windowsSharingViolation)) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
