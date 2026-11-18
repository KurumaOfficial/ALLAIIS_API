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
	ID       string `json:"id"`
	Object   string `json:"object"`
	Created  int64  `json:"created"`
	Provider string `json:"owned_by"`
}

type Target struct {
	Route    config.Route
	Provider config.Provider
	Entry    vault.Entry
}

type ProviderStatus struct {
	Provider      string     `json:"provider"`
	Credentials   []string   `json:"credentials"`
	CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
}

type pool struct {
	provider config.Provider
	entries  []vault.Entry
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
		if route.Privacy.Meets(minimum) && len(r.pools[route.Provider].entries) > 0 {
			models = append(models, Model{ID: route.ID, Object: "model", Provider: route.Provider})
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}

func (r *Router) Resolve(ctx context.Context, model string, minimum config.Privacy, session [32]byte) (Target, time.Time, error) {
	route, ok := r.routes[model]
	if !ok || !route.Privacy.Meets(minimum) {
		return Target{}, time.Time{}, ErrModel
	}
	p := r.pools[route.Provider]
	if len(p.entries) == 0 {
		return Target{}, time.Time{}, ErrCredentials
	}
	until, err := r.cooldown(ctx, route.Provider, p.provider.BaseURL)
	if err != nil {
		return Target{}, time.Time{}, err
	}
	if time.Now().Before(until) {
		return Target{}, until, ErrCooldown
	}
	position := binary.BigEndian.Uint64(session[:]) % uint64(len(p.entries))
	return Target{Route: route, Provider: p.provider, Entry: p.entries[position]}, time.Time{}, nil
}

func (r *Router) SetCooldown(ctx context.Context, target Target, until time.Time) error {
	r.mu.Lock()
	if until.After(r.blocked[target.Route.Provider]) {
		r.blocked[target.Route.Provider] = until
	}
	r.mu.Unlock()
	return r.store.SetCooldown(ctx, target.Route.Provider, target.Provider.BaseURL, until)
}

func (r *Router) cooldown(ctx context.Context, provider, endpoint string) (time.Time, error) {
	persisted, err := r.store.Cooldown(ctx, provider, endpoint)
	if err != nil {
		return time.Time{}, err
	}
	r.mu.Lock()
	local := r.blocked[provider]
	r.mu.Unlock()
	if local.After(persisted) {
		return local, nil
	}
	return persisted, nil
}

func (r *Router) Status(ctx context.Context) ([]ProviderStatus, error) {
	status := make([]ProviderStatus, 0, len(r.pools))
	for id, p := range r.pools {
		record := ProviderStatus{Provider: id, Credentials: make([]string, 0, len(p.entries))}
		for _, entry := range p.entries {
			record.Credentials = append(record.Credentials, entry.Label)
		}
		until, err := r.cooldown(ctx, id, p.provider.BaseURL)
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
