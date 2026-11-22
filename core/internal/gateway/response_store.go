// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"sync"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

// responseStore keeps recent Responses turn lists so a follow-up that only sends
// previous_response_id can be replayed onto a stateless web transport.
type responseStore struct {
	mu      sync.Mutex
	entries map[string]storedTurns
	order   []string
}

type storedTurns struct {
	turns   []webprov.Message
	expires time.Time
}

const (
	responseStoreSize  = 64
	responseStoreTTL   = time.Hour
	responseStoreTurns = 60
)

func newResponseStore() *responseStore {
	return &responseStore{entries: map[string]storedTurns{}}
}

func (s *responseStore) save(id string, turns []webprov.Message) {
	if s == nil || id == "" {
		return
	}
	clip := turns
	if len(clip) > responseStoreTurns {
		clip = clip[len(clip)-responseStoreTurns:]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.entries[id]; !exists {
		s.order = append(s.order, id)
	}
	s.entries[id] = storedTurns{turns: append([]webprov.Message{}, clip...), expires: time.Now().Add(responseStoreTTL)}
	for len(s.order) > responseStoreSize {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.entries, oldest)
	}
}

func (s *responseStore) load(id string) ([]webprov.Message, bool) {
	if s == nil || id == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[id]
	if !ok || time.Now().After(entry.expires) {
		delete(s.entries, id)
		return nil, false
	}
	return append([]webprov.Message{}, entry.turns...), true
}
