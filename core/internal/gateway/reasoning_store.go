// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// reasoningStore keeps the thinking of the last replies so a follow-up that
// replays the assistant text can send it back to models that require the
// encrypted reasoning round trip.
type reasoningStore struct {
	mu      sync.Mutex
	entries map[string]reasoningEntry
}

type reasoningEntry struct {
	text    string
	expires time.Time
}

const (
	reasoningEntries = 512
	reasoningTTL     = 2 * time.Hour
)

func newReasoningStore() *reasoningStore {
	return &reasoningStore{entries: map[string]reasoningEntry{}}
}

// reasoningKey ignores surrounding whitespace, which a client trims differently
// from the stream and would otherwise miss the stored entry.
func reasoningKey(alias, assistantText string) string {
	sum := sha256.Sum256([]byte(alias + "\x00" + strings.TrimSpace(assistantText)))
	return hex.EncodeToString(sum[:16])
}

func (s *reasoningStore) save(alias, assistantText, reasoning string) {
	if s == nil || reasoning == "" || assistantText == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for key, entry := range s.entries {
		if now.After(entry.expires) {
			delete(s.entries, key)
		}
	}
	for len(s.entries) >= reasoningEntries {
		for key := range s.entries {
			delete(s.entries, key)
			break
		}
	}
	s.entries[reasoningKey(alias, assistantText)] = reasoningEntry{
		text: reasoning, expires: time.Now().Add(reasoningTTL),
	}
}

// size reports the number of live entries.
func (s *reasoningStore) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func (s *reasoningStore) load(alias, assistantText string) string {
	if s == nil || assistantText == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := reasoningKey(alias, assistantText)
	entry, ok := s.entries[key]
	if !ok {
		return ""
	}
	if time.Now().After(entry.expires) {
		delete(s.entries, key)
		return ""
	}
	return entry.text
}
