// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// toolNameState is the persisted half of the tool counters: how many names were
// repaired, how many stayed unknown and which ones. It exists so the doctor
// warning survives a restart instead of forgetting what the last run learned.
type toolNameState struct {
	Folded   int64            `json:"folded"`
	Suffixed int64            `json:"suffixed"`
	Unknown  int64            `json:"unknown"`
	Names    map[string]int   `json:"names"`
	Seen     map[string]int64 `json:"seen,omitempty"`
}

// toolNameTTL is the retention in seconds; zero keeps names forever. It is in
// seconds so a demo can use minutes. A name the model stopped inventing should
// not be blamed a year later.
var toolNameTTL atomic.Int64

// WithToolNameTTL sets the retention in days for unknown names.
func (g *Gateway) WithToolNameTTL(days int) *Gateway {
	toolNameTTL.Store(int64(days) * 86400)
	return g
}

// WithToolNameTTLFor sets the retention from a duration, rounded down to whole
// seconds. Non-positive durations keep names forever.
func (g *Gateway) WithToolNameTTLFor(ttl time.Duration) *Gateway {
	if ttl <= 0 {
		toolNameTTL.Store(0)
		return g
	}
	toolNameTTL.Store(int64(ttl / time.Second))
	return g
}

// retention is the configured TTL as a duration, zero when names are kept.
func retention() time.Duration {
	return time.Duration(toolNameTTL.Load()) * time.Second
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

// expiredToolNames counts the names past retention without dropping them, for the
// panel button that drops them.
func expiredToolNames() int {
	ttl := retention()
	if ttl <= 0 {
		return 0
	}
	cutoff := time.Now().Add(-ttl)
	unknownNames.mu.Lock()
	defer unknownNames.mu.Unlock()
	count := 0
	for _, seen := range unknownNames.seen {
		if seen.Before(cutoff) {
			count++
		}
	}
	return count
}

// dropExpiredNames forgets names older than the retention. The caller holds the
// lock.
func dropExpiredNames(now time.Time) int {
	ttl := retention()
	if ttl <= 0 || len(unknownNames.seen) == 0 {
		return 0
	}
	cutoff := now.Add(-ttl)
	dropped := 0
	for name, seen := range unknownNames.seen {
		if seen.Before(cutoff) {
			delete(unknownNames.seen, name)
			delete(unknownNames.count, name)
			dropped++
		}
	}
	return dropped
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
	unknownNames.seen = map[string]time.Time{}
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

	now := time.Now()
	unknownNames.mu.Lock()
	unknownNames.count = map[string]int{}
	unknownNames.seen = map[string]time.Time{}
	for name, count := range saved.Names {
		if count <= 0 {
			continue
		}
		unknownNames.count[name] = count
		// A file from before the timestamps existed is treated as fresh.
		if stamp, ok := saved.Seen[name]; ok {
			unknownNames.seen[name] = time.UnixMilli(stamp)
		} else {
			unknownNames.seen[name] = now
		}
	}
	dropped := dropExpiredNames(now)
	trimUnknownNames(maxUnknownNames)
	unknownNames.mu.Unlock()
	if dropped > 0 {
		// Write straight away so the file stops listing expired names, which is
		// what `allaiis tools show` reads.
		toolNameFile.mu.Lock()
		toolNameFile.lastSave = time.Time{}
		toolNameFile.mu.Unlock()
		saveToolNameState()
	}
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

// DropExpiredToolNames forgets only the names past retention and reports how
// many went. It touches neither the counters nor the fresh names.
func (g *Gateway) DropExpiredToolNames() int {
	unknownNames.mu.Lock()
	dropped := dropExpiredNames(time.Now())
	unknownNames.mu.Unlock()
	if dropped == 0 {
		return 0
	}
	toolNameFile.mu.Lock()
	toolNameFile.lastSave = time.Time{}
	toolNameFile.mu.Unlock()
	saveToolNameState()
	return dropped
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

	// Names past retention are kept here and only removed on load or by the
	// explicit drop, so the panel button has something to act on.
	unknownNames.mu.Lock()
	names := make(map[string]int, len(unknownNames.count))
	seen := make(map[string]int64, len(unknownNames.count))
	for name, count := range unknownNames.count {
		names[name] = count
		seen[name] = unknownNames.seen[name].UnixMilli()
	}
	unknownNames.mu.Unlock()

	state := toolNameState{
		Folded:   toolCounters.fold.Load(),
		Suffixed: toolCounters.suffix.Load(),
		Unknown:  toolCounters.unknown.Load(),
		Names:    names,
		Seen:     seen,
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
