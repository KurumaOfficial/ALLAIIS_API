// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/router"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

const (
	tierHeader       = "X-ALLAIIS-Transport-Tier"
	probeHeader      = "X-ALLAIIS-Probe"
	maxRequest       = 8 << 20
	maxReply         = 16 << 20
	anthropicVersion = "2026-10-21"
)

// table is one immutable routing snapshot. Reload swaps whole tables so a
// request never observes a half-applied configuration.
type table struct {
	routes *router.Router
	config config.Config
}

type Gateway struct {
	live      atomic.Pointer[table]
	client    *http.Client
	web       *webprov.Client
	key       atomic.Pointer[[32]byte]
	minimum   atomic.Value
	metrics   *metrics
	reasoning *reasoningStore
	reload    *reloadState
	mux       http.Handler
}

// reloadState carries what a hot reload needs to reread the configuration.
type reloadState struct {
	path  string
	store *vault.Store
	stamp atomic.Pointer[string]
	mtime atomic.Int64
	size  atomic.Int64
}

func New(routes *router.Router, clientKey string, minimum config.Privacy) *Gateway {
	return NewWithWeb(routes, clientKey, minimum, webprov.New(10*time.Minute))
}

// NewWithWeb builds a gateway with an explicit web-provider client.
func NewWithWeb(routes *router.Router, clientKey string, minimum config.Privacy, web *webprov.Client) *Gateway {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 90 * time.Second
	g := &Gateway{
		web:       web,
		metrics:   newMetrics(),
		reasoning: newReasoningStore(),
		client: &http.Client{
			Transport: transport,
			Timeout:   10 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	g.live.Store(&table{routes: routes, config: config.Default()})
	g.SetClientKey(clientKey)
	g.minimum.Store(minimum)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/models", g.authorize(config.Chat, g.models))
	mux.HandleFunc("POST /v1/chat/completions", g.completion(config.Chat, "/chat/completions"))
	mux.HandleFunc("POST /v1/responses", g.completion(config.Responses, "/responses"))
	mux.HandleFunc("POST /v1/messages", g.completion(config.Messages, "/messages"))
	mux.HandleFunc("GET /metrics", g.metricsPage)
	mux.HandleFunc("GET /tool-names", g.toolNamesPage)
	g.mux = g.instrument(mux)
	return g
}

// instrument stamps a request ID on every response, keeps cache and CORS
// headers uniform, and turns a panic into a JSON 500 when nothing was written.
func (g *Gateway) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := inboundRequestID(r.Header.Get("X-Request-Id"))
		if id == "" {
			id = "req_" + randomID()
		}
		w.Header().Set("X-Request-Id", id)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Expose-Headers", "X-Request-Id, X-ALLAIIS-Transport-Tier, X-ALLAIIS-Provider, X-ALLAIIS-Model, X-ALLAIIS-Upstream-Model, X-ALLAIIS-Emulated-Features")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Api-Key, Anthropic-Version, Anthropic-Beta, X-ALLAIIS-Session, X-ALLAIIS-Min-Privacy, X-Request-Id")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		tracker := &writeTracker{ResponseWriter: w}
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			if recovered != http.ErrAbortHandler {
				slog.Error("handler panic", "request_id", id, "path", r.URL.Path, "panic", recovered)
			}
			if !tracker.wrote {
				writeError(w, config.Chat, http.StatusInternalServerError, "internal_error",
					"The gateway hit an internal error. Quote the request id when reporting it.")
			}
		}()
		next.ServeHTTP(tracker, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// writeTracker remembers whether a body or status was already written.
type writeTracker struct {
	http.ResponseWriter
	wrote bool
}

func (t *writeTracker) WriteHeader(status int) {
	t.wrote = true
	t.ResponseWriter.WriteHeader(status)
}

func (t *writeTracker) Write(body []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(body)
}

func (t *writeTracker) Unwrap() http.ResponseWriter { return t.ResponseWriter }

type requestIDKey struct{}

func requestID(r *http.Request) string {
	id, _ := r.Context().Value(requestIDKey{}).(string)
	return id
}

// inboundRequestID keeps a client-supplied trace ID when it is safe.
func inboundRequestID(raw string) string {
	if raw == "" || len(raw) > 64 {
		return ""
	}
	for index := range len(raw) {
		ch := raw[index]
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' ||
			ch == '-' || ch == '_' || ch == '.' {
			continue
		}
		return ""
	}
	return raw
}

// ResetCredential drops cached session state and any cooldown for a label after
// its credential was replaced or removed.
func (g *Gateway) ResetCredential(ctx context.Context, provider config.Provider, providerID, label, rawKey string) error {
	if rawKey != "" {
		if credential, err := webprov.ParseCredential(rawKey); err == nil {
			g.web.Forget(credential)
		}
	}
	return g.ClearCooldown(ctx, provider, providerID, label)
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) { g.mux.ServeHTTP(w, r) }

func (g *Gateway) table() *table { return g.live.Load() }

func (g *Gateway) metricsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write([]byte(g.metrics.prometheus(len(g.table().routes.Models(config.Public)))))
}

// ClearCooldown lifts a stored and in-memory cooldown.
func (g *Gateway) ClearCooldown(ctx context.Context, provider config.Provider, providerID, label string) error {
	return g.table().routes.ClearCooldown(ctx, provider, providerID, label)
}

// Models lists every configured alias, without the privacy filter.
func (g *Gateway) Models() []router.Model { return g.table().routes.Models(config.Public) }

// Minimum reports the configured privacy floor.
func (g *Gateway) Minimum() config.Privacy { return g.minimum.Load().(config.Privacy) }

// ValidKey reports whether key is the current client key.
func (g *Gateway) ValidKey(key string) bool { return g.validKey(key) }

// SetClientKey installs the bearer key clients must present.
func (g *Gateway) SetClientKey(key string) {
	digest := sha256.Sum256([]byte(key))
	g.key.Store(&digest)
}

// WithReload enables hot reload for a configuration file and vault.
func (g *Gateway) WithReload(path string, store *vault.Store) *Gateway {
	g.reload = &reloadState{path: path, store: store}
	return g
}

// Reload rereads the configuration and swaps in a new routing table. On error
// the previous table stays live.
func (g *Gateway) Reload(ctx context.Context) error {
	if g.reload == nil {
		return errors.New("hot reload is not configured")
	}
	cfg, err := config.Load(g.reload.path)
	if err != nil {
		return err
	}
	routes, err := router.New(ctx, cfg, g.reload.store)
	if err != nil {
		return err
	}
	g.live.Store(&table{routes: routes, config: cfg})
	g.minimum.Store(cfg.MinPrivacy)
	stamp, _ := g.reload.store.CredentialStamp(ctx)
	if stamp != "" {
		g.reload.stamp.Store(&stamp)
	}
	// Baseline the file metadata too, so the watcher does not reload again for
	// the write we just handled.
	if info, err := os.Stat(g.reload.path); err == nil {
		g.reload.mtime.Store(info.ModTime().UnixNano())
		g.reload.size.Store(info.Size())
	}
	slog.Info("configuration reloaded", "models", len(cfg.Models))
	return nil
}

// Watch polls the configuration and vault and reloads when either changes.
func (g *Gateway) Watch(ctx context.Context) {
	if g.reload == nil {
		return
	}
	stamp, _ := g.reload.store.CredentialStamp(ctx)
	g.reload.stamp.Store(&stamp)
	if info, err := os.Stat(g.reload.path); err == nil {
		g.reload.mtime.Store(info.ModTime().UnixNano())
		g.reload.size.Store(info.Size())
	}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		info, err := os.Stat(g.reload.path)
		g.web.Sweep()
		changed := err == nil && (info.ModTime().UnixNano() != g.reload.mtime.Load() || info.Size() != g.reload.size.Load())
		if current, err := g.reload.store.CredentialStamp(ctx); err == nil {
			if last := g.reload.stamp.Load(); last == nil || *last != current {
				changed = true
			}
		}
		if !changed {
			continue
		}
		if err := g.Reload(ctx); err != nil {
			slog.Warn("configuration reload failed, keeping the previous routes", "error", err)
			// Remember the broken file so the warning repeats only after the next
			// edit instead of every tick.
			if info, statErr := os.Stat(g.reload.path); statErr == nil {
				g.reload.mtime.Store(info.ModTime().UnixNano())
				g.reload.size.Store(info.Size())
			}
			continue
		}
	}
}

func (g *Gateway) authorize(protocol config.Protocol, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if g.validKey(clientCredential(r, protocol)) {
			next(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, protocol, http.StatusUnauthorized, "invalid_api_key", "A valid ALLAIIS key is required.")
	}
}

func clientCredential(r *http.Request, protocol config.Protocol) string {
	auth := r.Header.Values("Authorization")
	if keys := r.Header.Values("X-Api-Key"); protocol == config.Messages && len(keys) > 0 {
		if len(keys) == 1 && len(auth) == 0 {
			return keys[0]
		}
		return ""
	}
	if len(auth) == 1 {
		scheme, secret, found := strings.Cut(auth[0], " ")
		if found && strings.EqualFold(scheme, "Bearer") {
			return secret
		}
	}
	return ""
}

func (g *Gateway) validKey(key string) bool {
	digest := sha256.Sum256([]byte(key))
	expected := g.key.Load()
	return key != "" && expected != nil && subtle.ConstantTimeCompare(digest[:], expected[:]) == 1
}

func (g *Gateway) privacy(r *http.Request) (config.Privacy, bool) {
	values := r.Header.Values("X-ALLAIIS-Min-Privacy")
	minimum := g.minimum.Load().(config.Privacy)
	if len(values) == 0 {
		return minimum, true
	}
	requested := config.Privacy(values[0])
	if len(values) != 1 || !requested.Valid() {
		return "", false
	}
	if requested.Meets(minimum) {
		return requested, true
	}
	return minimum, true
}

func (g *Gateway) models(w http.ResponseWriter, r *http.Request) {
	minimum, ok := g.privacy(r)
	if !ok {
		writeError(w, config.Chat, http.StatusBadRequest, "invalid_privacy", "Invalid X-ALLAIIS-Min-Privacy value.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": g.table().routes.Models(minimum)})
}

func (g *Gateway) completion(protocol config.Protocol, path string) http.HandlerFunc {
	return g.authorize(protocol, func(w http.ResponseWriter, r *http.Request) {
		g.forwardCompletion(w, r, protocol, path)
	})
}

func (g *Gateway) forwardCompletion(w http.ResponseWriter, r *http.Request, protocol config.Protocol, path string) {
	start := time.Now()
	recorder := &statusWriter{ResponseWriter: w}
	w = recorder
	model, provider, stream, chars, calls := "", "", false, 0, 0
	defer func() {
		g.recordCompletion(recorder, start, protocol, stream, func() (string, string) { return model, provider }, chars, calls)
	}()
	minimum, ok := g.privacy(r)
	if !ok {
		writeError(w, protocol, http.StatusBadRequest, "invalid_privacy", "Invalid X-ALLAIIS-Min-Privacy value.")
		return
	}
	var version string
	if protocol == config.Messages {
		var err error
		version, err = messagesVersion(r.Header.Values("Anthropic-Version"))
		if err != nil {
			writeError(w, protocol, http.StatusBadRequest, "invalid_version", err.Error())
			return
		}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequest))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeError(w, protocol, http.StatusRequestEntityTooLarge, "request_too_large", "The maximum request size is 8 MiB.")
		return
	case err != nil:
		writeError(w, protocol, http.StatusBadRequest, "invalid_request", "Could not read the request body.")
		return
	}

	fields, parsedModel, parsedStream, prefix, err := parseCompletion(body, protocol)
	model, stream = parsedModel, parsedStream
	if err != nil {
		writeError(w, protocol, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	sessionHeader := r.Header.Get("X-ALLAIIS-Session")
	if sessionHeader == "" {
		sessionHeader = r.Header.Get("X-OpenCode-Session")
	}
	if len(sessionHeader) > 256 {
		writeError(w, protocol, http.StatusBadRequest, "invalid_session", "The session header must be at most 256 bytes.")
		return
	}
	session := g.sessionID(sessionHeader, prefix)
	target, until, err := g.table().routes.Resolve(r.Context(), model, protocol, minimum, session)
	provider = target.Route.Provider
	if target.Route.ID != "" {
		w.Header().Set(tierHeader, "tier_a")
		w.Header().Set("X-ALLAIIS-Provider", target.Route.Provider)
		w.Header().Set("X-ALLAIIS-Model", target.Route.ID)
	}
	switch {
	case errors.Is(err, router.ErrModel):
		writeError(w, protocol, http.StatusNotFound, "model_not_found", "The requested model is unavailable for this endpoint under the current policy.")
		return
	case errors.Is(err, router.ErrCredentials):
		writeError(w, protocol, http.StatusServiceUnavailable, "key_unconfigured", "No provider key is configured for this model.")
		return
	case errors.Is(err, router.ErrCooldown):
		rateLimited(w, protocol, until)
		return
	case err != nil:
		writeError(w, protocol, http.StatusServiceUnavailable, "routing_unavailable", "Could not read the routing state.")
		return
	}

	if target.Provider.Web() {
		chars, calls = g.webCompletion(w, r, protocol, target, body, stream)
		return
	}
	fields["model"], _ = json.Marshal(target.Route.UpstreamModel)
	forward, err := json.Marshal(fields)
	if err != nil {
		writeError(w, protocol, http.StatusBadRequest, "invalid_request", "Could not encode the model request.")
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.Provider.BaseURL+path, bytes.NewReader(forward))
	if err != nil {
		writeError(w, protocol, http.StatusBadGateway, "upstream_unavailable", "Could not construct the provider request.")
		return
	}
	if protocol == config.Messages {
		request.Header.Set("X-Api-Key", target.Entry.Key)
		request.Header.Set("Anthropic-Version", version)
		for _, beta := range r.Header.Values("Anthropic-Beta") {
			request.Header.Add("Anthropic-Beta", beta)
		}
	} else {
		request.Header.Set("Authorization", "Bearer "+target.Entry.Key)
	}
	request.Header.Set("User-Agent", "ALLAIIS/0.1")
	request.Header.Set("Content-Type", "application/json")
	if stream {
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	if target.Provider.SendSession() {
		request.Header.Set("X-OpenCode-Session", "ses_"+hex.EncodeToString(session[:16]))
	}

	response, err := g.client.Do(request)
	if err != nil {
		if r.Context().Err() == nil {
			slog.Warn("provider request failed", "provider", target.Route.Provider)
			writeError(w, protocol, http.StatusBadGateway, "upstream_unavailable", "Could not connect to the provider or complete the request in time.")
		}
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		g.upstreamError(w, r.Context(), target, response)
		return
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if stream && mediaType == "text/event-stream" {
		streamSSE(w, response.Body)
		return
	}
	if !stream && mediaType == "application/json" {
		copyJSON(w, protocol, response.Body)
		return
	}
	writeError(w, protocol, http.StatusBadGateway, "invalid_upstream_response", "The provider returned an unexpected response format.")
}

func messagesVersion(values []string) (string, error) {
	if len(values) == 0 {
		return anthropicVersion, nil
	}
	if len(values) == 1 {
		if _, err := time.Parse(time.DateOnly, values[0]); err == nil {
			return values[0], nil
		}
	}
	return "", errors.New("Anthropic-Version must be a single date in YYYY-MM-DD format.")
}

func parseCompletion(body []byte, protocol config.Protocol) (map[string]json.RawMessage, string, bool, []byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, "", false, nil, errors.New("expected a JSON object")
	}
	var model string
	if json.Unmarshal(fields["model"], &model) != nil || model == "" {
		return nil, "", false, nil, errors.New("model must be a non-empty string")
	}
	var stream bool
	if raw, ok := fields["stream"]; ok && json.Unmarshal(raw, &stream) != nil {
		return nil, "", false, nil, errors.New("stream must be a boolean")
	}
	if protocol == config.Responses {
		if bytes.Equal(fields["background"], []byte("true")) {
			return nil, "", false, nil, errors.New("background generation is unavailable through this endpoint")
		}
		return fields, model, stream, nil, nil
	}
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(fields["messages"], &messages) != nil || len(messages) == 0 {
		return nil, "", false, nil, errors.New("messages must be a non-empty array of message objects")
	}
	prefixLength := 0
	for i, message := range messages {
		if message.Role == "" {
			return nil, "", false, nil, errors.New("every message must have a non-empty role")
		}
		if message.Role == "user" && prefixLength == 0 {
			prefixLength = i + 1
		}
	}
	if prefixLength == 0 {
		prefixLength = len(messages)
	}
	prefix, err := json.Marshal(messages[:prefixLength])
	if protocol == config.Messages {
		prefix = append(append(bytes.Clone(fields["system"]), '\x00'), prefix...)
	}
	return fields, model, stream, prefix, err
}

func (g *Gateway) sessionID(hint string, prefix []byte) [32]byte {
	mac := hmac.New(sha256.New, g.key.Load()[:])
	if hint != "" {
		mac.Write([]byte("session/v1\x00" + hint))
	} else {
		mac.Write([]byte("prefix/v1\x00"))
		mac.Write(prefix)
	}
	return [32]byte(mac.Sum(nil))
}

func (g *Gateway) upstreamError(w http.ResponseWriter, ctx context.Context, target router.Target, response *http.Response) {
	protocol := target.Route.Protocol
	status := response.StatusCode
	switch status {
	case http.StatusTooManyRequests:
		until := retryTime(response.Header.Get("Retry-After"), time.Now())
		if ceiling := time.Now().Add(maxRateCooldown); until.After(ceiling) {
			until = ceiling
		}
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := g.table().routes.SetCooldown(persist, target, until); err != nil {
			slog.Warn("could not persist provider cooldown", "provider", target.Route.Provider)
		}
		rateLimited(w, protocol, until)
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		writeError(w, protocol, status, "upstream_rejected_request", "The provider rejected this model or its request parameters.")
	case http.StatusRequestEntityTooLarge:
		writeError(w, protocol, status, "request_too_large", "The model request exceeds the provider's size limit.")
	case http.StatusUnauthorized, http.StatusForbidden:
		writeError(w, protocol, http.StatusBadGateway, "upstream_access_denied", "The provider refused access. Check the API key and model permissions.")
	default:
		writeError(w, protocol, http.StatusBadGateway, "upstream_error", "The provider returned an unexpected status.")
	}
}

// maxRateCooldown stops a provider's long reset from parking a pool for hours.
// A burst window usually clears well before the published quota reset, so a
// capped retry costs nothing while a 17 hour sleep costs the whole session.
const maxRateCooldown = 10 * time.Minute

func retryTime(value string, now time.Time) time.Time {
	if seconds, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32); err == nil {
		return now.Add(time.Duration(max(1, seconds)) * time.Second)
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return date
	}
	return now.Add(30 * time.Second)
}

func rateLimited(w http.ResponseWriter, protocol config.Protocol, until time.Time) {
	seconds := (time.Until(until) + time.Second - 1) / time.Second
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(int64(seconds), 10))
	writeError(w, protocol, http.StatusTooManyRequests, "rate_limit_exceeded", "The provider rate-limited this key pool. Retry after the stated delay.")
}

func copyJSON(w http.ResponseWriter, protocol config.Protocol, body io.Reader) {
	result, err := io.ReadAll(io.LimitReader(body, maxReply+1))
	if err != nil || len(result) > maxReply || !json.Valid(result) {
		writeError(w, protocol, http.StatusBadGateway, "invalid_upstream_response", "The provider returned an invalid or oversized JSON response.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(result)
}

func streamSSE(w http.ResponseWriter, body io.Reader) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	ctrl := http.NewResponseController(w)
	defer ctrl.SetWriteDeadline(time.Time{})
	ctrl.SetWriteDeadline(time.Now().Add(15 * time.Second))
	w.WriteHeader(http.StatusOK)
	if err := ctrl.Flush(); err != nil {
		return
	}
	ctrl.SetWriteDeadline(time.Time{})
	buf := make([]byte, 32<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			ctrl.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			if err := ctrl.Flush(); err != nil {
				return
			}
			ctrl.SetWriteDeadline(time.Time{})
		}
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			panic(http.ErrAbortHandler)
		}
	}
}

func writeError(w http.ResponseWriter, protocol config.Protocol, status int, code, message string) {
	kind := "invalid_request_error"
	if status >= 500 {
		kind = "api_error"
	} else if status == http.StatusTooManyRequests {
		kind = "rate_limit_error"
	} else if status == http.StatusPaymentRequired {
		kind = "insufficient_quota"
	}
	if protocol == config.Messages {
		switch status {
		case http.StatusUnauthorized:
			kind = "authentication_error"
		case http.StatusNotFound:
			kind = "not_found_error"
		case http.StatusRequestEntityTooLarge:
			kind = "request_too_large"
		}
		writeJSON(w, status, map[string]any{
			"type": "error", "error": map[string]string{"type": kind, "message": message},
		})
		return
	}
	payload := map[string]string{"message": message, "type": kind, "code": code}
	if id := w.Header().Get("X-Request-Id"); id != "" {
		payload["request_id"] = id
	}
	writeJSON(w, status, map[string]any{"error": payload})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}
