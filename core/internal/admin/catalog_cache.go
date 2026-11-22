// SPDX-License-Identifier: GPL-3.0-only

package admin

import (
	"strings"
	"sync"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/catalog"
)

// catalogCache keeps discovery results briefly. Catalog calls cost quota, and a
// model list rarely changes between two clicks.
type catalogCache struct {
	mu      sync.Mutex
	entries map[string]catalogEntry
}

type catalogEntry struct {
	models  []catalog.Model
	expires time.Time
}

const catalogTTL = 10 * time.Minute

func newCatalogCache() *catalogCache { return &catalogCache{entries: map[string]catalogEntry{}} }

func (c *catalogCache) get(key string) ([]catalog.Model, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return append([]catalog.Model{}, entry.models...), true
}

func (c *catalogCache) put(key string, models []catalog.Model) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = catalogEntry{models: append([]catalog.Model{}, models...), expires: time.Now().Add(catalogTTL)}
}

// dropProvider forgets every catalog for one provider, e.g. after a new session.
func (c *catalogCache) dropProvider(provider string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.entries {
		if strings.HasPrefix(key, provider+"\x00") {
			delete(c.entries, key)
		}
	}
}
