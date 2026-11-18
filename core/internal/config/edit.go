// SPDX-License-Identifier: GPL-3.0-only

package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

func AddModel(ctx context.Context, path string, route Route) (bool, error) {
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false, err
	}
	lock := flock.New(path+".lock", flock.SetPermissions(0600))
	defer lock.Close()
	lockContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockContext, 50*time.Millisecond)
	if err != nil {
		return false, fmt.Errorf("lock configuration: %w", err)
	}
	if !locked {
		return false, errors.New("configuration is being edited by another process")
	}

	cfg, err := Load(path)
	if err != nil {
		return false, err
	}
	if route.Privacy == "" {
		route.Privacy = Consumer
	}
	for _, existing := range cfg.Models {
		if existing.ID == route.ID {
			if existing == route {
				return false, nil
			}
			return false, fmt.Errorf("model alias %q already points to a different route", route.ID)
		}
	}
	cfg.Models = append(cfg.Models, route)
	if err := cfg.validate(); err != nil {
		return false, err
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return false, err
	}
	if len(body)+1 > maxConfigBytes {
		return false, errors.New("edited configuration would exceed 1 MiB")
	}
	if err := replaceConfig(path, append(body, '\n')); err != nil {
		return false, err
	}
	return true, nil
}

func replaceConfig(path string, body []byte) error {
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
	return os.Rename(file.Name(), path)
}
