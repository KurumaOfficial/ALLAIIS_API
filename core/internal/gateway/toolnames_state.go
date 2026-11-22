// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// toolNameState is the persisted half of the tool counters: how many names were
// repaired, how many stayed unknown and which ones. It exists so the doctor
// warning survives a restart instead of forgetting what the last run learned.
type toolNameState struct {
	Folded   int64          `json:"folded"`
	Suffixed int64          `json:"suffixed"`
	Unknown  int64          `json:"unknown"`
	Names    map[string]int `json:"names"`
}

var toolNameFile struct {
	mu       sync.Mutex
	path     string
	lastSave time.Time
	pending  *time.Timer
}

// toolNameSaveEvery throttles disk writes; the file is only read at startup.
const toolNameSaveEvery = 3 * time.Second

// WithStateDir loads and remembers the tool name state under dir, and watches
// the file so a CLI reset or edit takes effect without a restart.
func (g *Gateway) WithStateDir(ctx context.Context, dir string) *Gateway {
	if dir == "" {
		return g
	}
	path := filepath.Join(dir, "tool-names.json")
	toolNameFile.mu.Lock()
	toolNameFile.path = path
	toolNameFile.mu.Unlock()
	loadToolNameState(path)
	go watchToolNameFile(ctx)
	return g
}

// toolNameWatchEvery is how often the file is checked for an outside change.
const toolNameWatchEvery = 2 * time.Second

// watchToolNameFile keeps memory in step with the file. Deletion is treated
// like a reset; another change is loaded and becomes the new baseline.
func watchToolNameFile(ctx context.Context) {
	ticker := time.NewTicker(toolNameWatchEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		toolNameFile.mu.Lock()
		path, last := toolNameFile.path, toolNameFile.lastSave
		toolNameFile.mu.Unlock()
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			// Only a vanished file counts, and only once the last write is old
			// enough that it cannot be a save in flight.
			if since := time.Since(last); os.IsNotExist(err) && since >= toolNameSaveEvery && hasToolNameState() {
				clearToolNameState()
			}
			continue
		}
		if !info.ModTime().After(last) {
			continue
		}
		loadToolNameState(path)
		toolNameFile.mu.Lock()
		toolNameFile.lastSave = info.ModTime()
		toolNameFile.mu.Unlock()
	}
}

// hasToolNameState reports whether anything is worth clearing.
func hasToolNameState() bool {
	if toolCounters.fold.Load()+toolCounters.suffix.Load()+toolCounters.unknown.Load() > 0 {
		return true
	}
	unknownNames.mu.Lock()
	defer unknownNames.mu.Unlock()
	return len(unknownNames.count) > 0
}

// clearToolNameState zeroes the counters and names but leaves the file alone.
func clearToolNameState() {
	toolCounters.fold.Store(0)
	toolCounters.suffix.Store(0)
	toolCounters.unknown.Store(0)
	unknownNames.mu.Lock()
	unknownNames.count = map[string]int{}
	unknownNames.mu.Unlock()
}

// loadToolNameState applies a saved file, keeping only the most frequent names.
func loadToolNameState(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var saved toolNameState
	if json.Unmarshal(raw, &saved) != nil {
		return
	}
	toolCounters.fold.Store(saved.Folded)
	toolCounters.suffix.Store(saved.Suffixed)
	toolCounters.unknown.Store(saved.Unknown)

	unknownNames.mu.Lock()
	unknownNames.count = map[string]int{}
	for name, count := range saved.Names {
		if count > 0 {
			unknownNames.count[name] = count
		}
	}
	trimUnknownNames(maxUnknownNames)
	unknownNames.mu.Unlock()
}

// ResetToolNameState forgets the counters, the names and the saved file, so the
// doctor starts from scratch.
func (g *Gateway) ResetToolNameState() {
	clearToolNameState()

	toolNameFile.mu.Lock()
	defer toolNameFile.mu.Unlock()
	if toolNameFile.pending != nil {
		toolNameFile.pending.Stop()
		toolNameFile.pending = nil
	}
	toolNameFile.lastSave = time.Time{}
	if toolNameFile.path != "" {
		os.Remove(toolNameFile.path)
	}
}

// trimUnknownNames drops the least frequent entries down to limit. The caller
// holds the lock.
func trimUnknownNames(limit int) {
	for len(unknownNames.count) > limit {
		lowest, count := "", 0
		for name, seen := range unknownNames.count {
			if lowest == "" || seen < count {
				lowest, count = name, seen
			}
		}
		delete(unknownNames.count, lowest)
	}
}

// saveToolNameState writes the counters and names, throttled, and schedules one
// trailing write when it has to wait, so the newest names are not lost. The
// write goes through a temporary file so a crash cannot leave half a JSON
// document behind.
func saveToolNameState() {
	toolNameFile.mu.Lock()
	defer toolNameFile.mu.Unlock()
	if toolNameFile.path == "" {
		return
	}
	if wait := toolNameSaveEvery - time.Since(toolNameFile.lastSave); wait > 0 {
		if toolNameFile.pending == nil {
			toolNameFile.pending = time.AfterFunc(wait, func() {
				toolNameFile.mu.Lock()
				toolNameFile.pending = nil
				toolNameFile.mu.Unlock()
				saveToolNameState()
			})
		}
		return
	}

	unknownNames.mu.Lock()
	names := make(map[string]int, len(unknownNames.count))
	for name, count := range unknownNames.count {
		names[name] = count
	}
	unknownNames.mu.Unlock()

	state := toolNameState{
		Folded:   toolCounters.fold.Load(),
		Suffixed: toolCounters.suffix.Load(),
		Unknown:  toolCounters.unknown.Load(),
		Names:    names,
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return
	}
	temp := toolNameFile.path + ".tmp"
	if os.WriteFile(temp, raw, 0o600) != nil {
		return
	}
	if os.Rename(temp, toolNameFile.path) != nil {
		os.Remove(temp)
		return
	}
	toolNameFile.lastSave = time.Now()
}
