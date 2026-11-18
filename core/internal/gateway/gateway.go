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
	"strconv"
	"strings"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/router"
)

const (
	tierHeader = "X-ALLAIIS-Transport-Tier"
	maxRequest = 8 << 20
	maxReply   = 16 << 20
)

type Gateway struct {
	router    *router.Router
	client    *http.Client
	keyDigest [32]byte
	minimum   config.Privacy
}

func New(routes *router.Router, clientKey string, minimum config.Privacy) http.Handler {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 90 * time.Second
	g := &Gateway{
		router:    routes,
		keyDigest: sha256.Sum256([]byte(clientKey)),
		minimum:   minimum,
		client: &http.Client{
			Transport: transport,
			Timeout:   10 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/models", g.authorize(g.models))
	mux.HandleFunc("POST /v1/chat/completions", g.authorize(g.chat))
	return mux
}

func (g *Gateway) authorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		headers := r.Header.Values("Authorization")
		if len(headers) == 1 {
			scheme, secret, found := strings.Cut(headers[0], " ")
			digest := sha256.Sum256([]byte(secret))
			if found && strings.EqualFold(scheme, "Bearer") && subtle.ConstantTimeCompare(digest[:], g.keyDigest[:]) == 1 {
				next(w, r)
				return
			}
		}
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "A valid ALLAIIS bearer key is required.")
	}
}

func (g *Gateway) privacy(r *http.Request) (config.Privacy, bool) {
	values := r.Header.Values("X-ALLAIIS-Min-Privacy")
	if len(values) == 0 {
		return g.minimum, true
	}
	requested := config.Privacy(values[0])
	if len(values) != 1 || !requested.Valid() {
		return "", false
	}
	if requested.Meets(g.minimum) {
		return requested, true
	}
	return g.minimum, true
}

func (g *Gateway) models(w http.ResponseWriter, r *http.Request) {
	minimum, ok := g.privacy(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_privacy", "Invalid X-ALLAIIS-Min-Privacy value.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": g.router.Models(minimum)})
}

func (g *Gateway) chat(w http.ResponseWriter, r *http.Request) {
	minimum, ok := g.privacy(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_privacy", "Invalid X-ALLAIIS-Min-Privacy value.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequest))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "The maximum request size is 8 MiB.")
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, "invalid_request", "Could not read the request body.")
		return
	}

	fields, model, stream, prefix, err := parseChat(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	sessionHeader := r.Header.Get("X-ALLAIIS-Session")
	if sessionHeader == "" {
		sessionHeader = r.Header.Get("X-OpenCode-Session")
	}
	if len(sessionHeader) > 256 {
		writeError(w, http.StatusBadRequest, "invalid_session", "The session header must be at most 256 bytes.")
		return
	}
	session := g.sessionID(sessionHeader, prefix)
	target, until, err := g.router.Resolve(r.Context(), model, minimum, session)
	if target.Route.ID != "" {
		w.Header().Set(tierHeader, "tier_a")
		w.Header().Set("X-ALLAIIS-Provider", target.Route.Provider)
		w.Header().Set("X-ALLAIIS-Model", target.Route.ID)
	}
	switch {
	case errors.Is(err, router.ErrModel):
		writeError(w, http.StatusNotFound, "model_not_found", "The requested model is unavailable under the current policy.")
		return
	case errors.Is(err, router.ErrCredentials):
		writeError(w, http.StatusServiceUnavailable, "key_unconfigured", "No provider key is configured for this model.")
		return
	case errors.Is(err, router.ErrCooldown):
		rateLimited(w, until)
		return
	case err != nil:
		writeError(w, http.StatusServiceUnavailable, "routing_unavailable", "Could not read the routing state.")
		return
	}

	fields["model"], _ = json.Marshal(target.Route.UpstreamModel)
	forward, err := json.Marshal(fields)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Could not encode the chat request.")
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.Provider.BaseURL+"/chat/completions", bytes.NewReader(forward))
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_unavailable", "Could not construct the provider request.")
		return
	}
	request.Header.Set("Authorization", "Bearer "+target.Entry.Key)
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
			writeError(w, http.StatusBadGateway, "upstream_unavailable", "Could not connect to the provider or complete the request in time.")
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
		copyJSON(w, response.Body)
		return
	}
	writeError(w, http.StatusBadGateway, "invalid_upstream_response", "The provider returned an unexpected response format.")
}

func parseChat(body []byte) (map[string]json.RawMessage, string, bool, []byte, error) {
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
	return fields, model, stream, prefix, err
}

func (g *Gateway) sessionID(hint string, prefix []byte) [32]byte {
	mac := hmac.New(sha256.New, g.keyDigest[:])
	if hint != "" {
		mac.Write([]byte("session/v1\x00" + hint))
	} else {
		mac.Write([]byte("prefix/v1\x00"))
		mac.Write(prefix)
	}
	return [32]byte(mac.Sum(nil))
}

func (g *Gateway) upstreamError(w http.ResponseWriter, ctx context.Context, target router.Target, response *http.Response) {
	status := response.StatusCode
	switch status {
	case http.StatusTooManyRequests:
		until := retryTime(response.Header.Get("Retry-After"), time.Now())
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := g.router.SetCooldown(persist, target, until); err != nil {
			slog.Warn("could not persist provider cooldown", "provider", target.Route.Provider)
		}
		rateLimited(w, until)
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		writeError(w, status, "upstream_rejected_request", "The provider rejected this model or its request parameters.")
	case http.StatusUnauthorized, http.StatusForbidden:
		writeError(w, http.StatusBadGateway, "upstream_access_denied", "The provider refused access. Check the API key and model permissions.")
	default:
		writeError(w, http.StatusBadGateway, "upstream_error", "The provider returned an unexpected status.")
	}
}

func retryTime(value string, now time.Time) time.Time {
	if seconds, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32); err == nil {
		return now.Add(time.Duration(max(1, seconds)) * time.Second)
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return date
	}
	return now.Add(30 * time.Second)
}

func rateLimited(w http.ResponseWriter, until time.Time) {
	seconds := (time.Until(until) + time.Second - 1) / time.Second
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(int64(seconds), 10))
	writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded", "The provider rate-limited this key pool. Retry after the stated delay.")
}

func copyJSON(w http.ResponseWriter, body io.Reader) {
	result, err := io.ReadAll(io.LimitReader(body, maxReply+1))
	if err != nil || len(result) > maxReply || !json.Valid(result) {
		writeError(w, http.StatusBadGateway, "invalid_upstream_response", "The provider returned an invalid or oversized JSON response.")
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

func writeError(w http.ResponseWriter, status int, code, message string) {
	kind := "invalid_request_error"
	if status >= 500 {
		kind = "api_error"
	} else if status == http.StatusTooManyRequests {
		kind = "rate_limit_error"
	}
	writeJSON(w, status, map[string]any{"error": map[string]string{
		"message": message, "type": kind, "code": code,
	}})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}
