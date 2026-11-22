// SPDX-License-Identifier: GPL-3.0-only

package router

import (
	"context"
	"encoding/binary"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
)

var (
	ErrModel       = errors.New("model is unknown or restricted")
	ErrCredentials = errors.New("no API key is configured for this model")
	ErrCooldown    = errors.New("provider is temporarily rate limited")
)

type Model struct {
	ID       string          `json:"id"`
	Object   string          `json:"object"`
	Created  int64           `json:"created"`
	Provider string          `json:"owned_by"`
	Protocol config.Protocol `json:"allaiis_protocol,omitempty"`
}

type Target struct {
	Route    config.Route
	Provider config.Provider
	Entry    vault.Entry
}

type ProviderStatus struct {
	Provider    string               `json:"provider"`
	Credentials []string             `json:"credentials"`
	Cooldowns   map[string]time.Time `json:"cooldowns,omitempty"`
	// CooldownUntil is the provider-wide cooldown used by unpinned pools.
	CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
}

// cooldownKey scopes a cooldown to one credential label. An unpinned pool cools
// the whole provider, because any label there can serve the request.
func cooldownKey(provider config.Provider, label string) string {
	if label == "" {
		return provider.BaseURL
	}
	return provider.BaseURL + "\x00" + label
}

type pool struct {
	provider config.Provider
	entries  []vault.Entry
}

func (p pool) credential(label string, session [32]byte) (vault.Entry, bool) {
	if label != "" {
		for _, entry := range p.entries {
			if entry.Label == label {
				return entry, true
			}
		}
	} else if len(p.entries) > 0 {
		position := binary.BigEndian.Uint64(session[:]) % uint64(len(p.entries))
		return p.entries[position], true
	}
	return vault.Entry{}, false
}

type Router struct {
	store   *vault.Store
	routes  map[string]config.Route
	pools   map[string]pool
	mu      sync.Mutex
	blocked map[string]time.Time
}

func New(ctx context.Context, cfg config.Config, store *vault.Store) (*Router, error) {
	r := &Router{
		store: store, routes: make(map[string]config.Route), pools: make(map[string]pool),
		blocked: make(map[string]time.Time),
	}
	for _, route := range cfg.Models {
		r.routes[route.ID] = route
		if _, ok := r.pools[route.Provider]; ok {
			continue
		}
		provider, _ := cfg.Provider(route.Provider)
		entries, err := store.Entries(ctx, route.Provider, provider.BaseURL)
		if err != nil {
			return nil, err
		}
		r.pools[route.Provider] = pool{provider: provider, entries: entries}
	}
	return r, nil
}

func (r *Router) Models(minimum config.Privacy) []Model {
	models := make([]Model, 0, len(r.routes))
	for _, route := range r.routes {
		if route.Disabled {
			continue
		}
		_, available := r.pools[route.Provider].credential(route.CredentialLabel, [32]byte{})
		if route.Privacy.Meets(minimum) && available {
			models = append(models, Model{ID: route.ID, Object: "model", Provider: route.Provider, Protocol: route.Protocol})
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}

func (r *Router) Resolve(ctx context.Context, model string, protocol config.Protocol, minimum config.Privacy, session [32]byte) (Target, time.Time, error) {
	route, ok := r.routes[model]
	if !ok || route.Disabled || route.Protocol != protocol || !route.Privacy.Meets(minimum) {
		return Target{}, time.Time{}, ErrModel
	}
	p := r.pools[route.Provider]
	entry, found := p.credential(route.CredentialLabel, session)
	if !found {
		return Target{}, time.Time{}, ErrCredentials
	}
	target := Target{Route: route, Provider: p.provider, Entry: entry}
	until, err := r.cooldown(ctx, route.Provider, p.provider, route.CredentialLabel)
	if err != nil {
		return Target{}, time.Time{}, err
	}
	if time.Now().Before(until) {
		return target, until, ErrCooldown
	}
	return target, time.Time{}, nil
}

func (r *Router) SetCooldown(ctx context.Context, target Target, until time.Time) error {
	key := cooldownKey(target.Provider, target.Route.CredentialLabel)
	r.mu.Lock()
	if until.After(r.blocked[key]) {
		r.blocked[key] = until
	}
	r.mu.Unlock()
	return r.store.SetCooldown(ctx, target.Route.Provider, key, until)
}

// ClearCooldown lifts a cooldown for one alias target or a whole provider.
func (r *Router) ClearCooldown(ctx context.Context, provider config.Provider, providerID, label string) error {
	key := cooldownKey(provider, label)
	r.mu.Lock()
	delete(r.blocked, key)
	r.mu.Unlock()
	return r.store.ClearCooldown(ctx, providerID, key)
}

// SaveResponse persists a Responses turn list for follow-ups.
func (r *Router) SaveResponse(ctx context.Context, id string, turns []byte, expires time.Time) error {
	return r.store.SaveResponse(ctx, id, turns, expires)
}

// LoadResponse reads a persisted turn list.
func (r *Router) LoadResponse(ctx context.Context, id string) ([]byte, bool) {
	return r.store.LoadResponse(ctx, id)
}

// cooldown returns the latest cooldown for a route, looking at the pinned
// label, the provider-wide row and the in-memory copy.
func (r *Router) cooldown(ctx context.Context, provider string, p config.Provider, label string) (time.Time, error) {
	key := cooldownKey(p, label)
	persisted, err := r.store.Cooldown(ctx, provider, key)
	if err != nil {
		return time.Time{}, err
	}
	r.mu.Lock()
	local := r.blocked[key]
	r.mu.Unlock()
	if local.After(persisted) {
		persisted = local
	}
	if label != "" {
		bare, err := r.store.Cooldown(ctx, provider, p.BaseURL)
		if err != nil {
			return time.Time{}, err
		}
		r.mu.Lock()
		local = r.blocked[p.BaseURL]
		r.mu.Unlock()
		if local.After(bare) {
			bare = local
		}
		if bare.After(persisted) {
			persisted = bare
		}
	}
	return persisted, nil
}

func (r *Router) Status(ctx context.Context) ([]ProviderStatus, error) {
	status := make([]ProviderStatus, 0, len(r.pools))
	for id, p := range r.pools {
		record := ProviderStatus{Provider: id, Credentials: make([]string, 0, len(p.entries))}
		for _, entry := range p.entries {
			record.Credentials = append(record.Credentials, entry.Label)
			until, err := r.cooldown(ctx, id, p.provider, entry.Label)
			if err != nil {
				return nil, err
			}
			if time.Now().Before(until) {
				if record.Cooldowns == nil {
					record.Cooldowns = map[string]time.Time{}
				}
				record.Cooldowns[entry.Label] = until
			}
		}
		until, err := r.cooldown(ctx, id, p.provider, "")
		if err != nil {
			return nil, err
		}
		if time.Now().Before(until) {
			record.CooldownUntil = &until
		}
		status = append(status, record)
	}
	sort.Slice(status, func(i, j int) bool { return status[i].Provider < status[j].Provider })
	return status, nil
}
