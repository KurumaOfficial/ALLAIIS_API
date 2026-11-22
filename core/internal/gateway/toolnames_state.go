// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
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

// WithStateDir loads and remembers the tool name state under dir.
func (g *Gateway) WithStateDir(dir string) *Gateway {
	if dir == "" {
		return g
	}
	path := filepath.Join(dir, "tool-names.json")
	toolNameFile.mu.Lock()
	toolNameFile.path = path
	toolNameFile.mu.Unlock()
	loadToolNameState(path)
	return g
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
