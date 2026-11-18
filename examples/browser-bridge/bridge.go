// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const modelID = "browser-echo"

//go:embed worker.html worker.js
var assets embed.FS

type job struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	result   chan string
	claimed  bool
	finished bool
	ctx      context.Context
}

type bridge struct {
	mu      sync.Mutex
	pending *job
	timeout time.Duration
	log     *slog.Logger
}

func newBridge(host, apiKey, workerKey string, timeout time.Duration, log *slog.Logger) http.Handler {
	b := &bridge{timeout: timeout, log: log}
	mux := http.NewServeMux()
	origin := "http://" + host
	worker := requireKey(workerKey, origin)
	api := requireKey(apiKey, "")
	mux.HandleFunc("GET /{$}", asset("worker.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /worker.js", asset("worker.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("POST /worker/claim", worker(b.claim))
	mux.HandleFunc("POST /worker/reply", worker(b.reply))
	mux.HandleFunc("GET /v1/models", api(models))
	mux.HandleFunc("POST /v1/chat/completions", api(b.complete))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; form-action 'none'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Host != host {
			fail(w, http.StatusForbidden, "Open the exact loopback address shown in the terminal.")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func requireKey(key, origin string) func(http.HandlerFunc) http.HandlerFunc {
	expected := sha256.Sum256([]byte(key))
	return func(handler http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if origin != "" && r.Header.Get("Origin") != origin {
				fail(w, http.StatusForbidden, "Worker requests require the bridge page's origin.")
				return
			}
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			digest := sha256.Sum256([]byte(token))
			if !ok || subtle.ConstantTimeCompare(digest[:], expected[:]) != 1 {
				fail(w, http.StatusUnauthorized, "Invalid bearer key.")
				return
			}
			handler(w, r)
		}
	}
}

func asset(name, mediaType string) http.HandlerFunc {
	body, err := assets.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", mediaType)
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
	}
}

func models(w http.ResponseWriter, _ *http.Request) {
	respond(w, http.StatusOK, map[string]any{
		"object": "list", "data": []map[string]any{
			{"id": modelID, "object": "model", "created": 0, "owned_by": "allaiis-example"},
		},
	})
}

func (b *bridge) complete(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	status, requestBytes, contentChars, attempts, nul := 0, 0, 0, 0, 0
	if b.log != nil {
		defer func() {
			b.log.Info("completion", "origin", "browser-worker", "request_bytes", requestBytes,
				"status", status, "model", modelID, "content_chars", contentChars, "reasoning_chars", 0,
				"tools", 0, "calls", 0, "attempts", attempts, "nul_removed", nul,
				"duration_ms", time.Since(start).Milliseconds())
		}()
	}
	var request struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	counter := &countedBody{ReadCloser: r.Body, bytes: &requestBytes}
	r.Body = counter
	if status = readJSON(w, r, &request); status != 0 {
		return
	}
	if request.Model != modelID {
		status = http.StatusNotFound
		fail(w, status, "Model ID must be "+modelID+".")
		return
	}
	if len(request.Messages) == 0 || request.Messages[len(request.Messages)-1].Role != "user" {
		status = http.StatusBadRequest
		fail(w, status, "End the messages array with a user text message.")
		return
	}
	text := request.Messages[len(request.Messages)-1].Content
	nul = strings.Count(text, "\x00")
	text = strings.ReplaceAll(text, "\x00", "")
	if strings.TrimSpace(text) == "" || len(text) > 8<<10 {
		status = http.StatusBadRequest
		fail(w, status, "Send a non-empty text message of at most 8 KiB.")
		return
	}
	j := &job{ID: "chatcmpl-" + rand.Text(), Text: text, result: make(chan string, 1), ctx: r.Context()}
	b.mu.Lock()
	if b.pending != nil {
		b.mu.Unlock()
		status = http.StatusServiceUnavailable
		fail(w, status, "The browser worker already has a request.")
		return
	}
	b.pending = j
	attempts++
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.pending = nil
		b.mu.Unlock()
	}()

	timer := time.NewTimer(b.timeout)
	defer timer.Stop()
	select {
	case <-r.Context().Done():
		return
	case <-timer.C:
		status = http.StatusGatewayTimeout
		fail(w, status, "The browser worker did not reply.")
	case answer := <-j.result:
		if r.Context().Err() == nil {
			status, contentChars = http.StatusOK, utf8.RuneCountInString(answer)
			completion(w, j.ID, j.Text, answer, request.Stream)
		}
	}
}

func (b *bridge) claim(w http.ResponseWriter, _ *http.Request) {
	b.mu.Lock()
	j := b.pending
	if j == nil || j.claimed || j.ctx.Err() != nil {
		b.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	j.claimed = true
	b.mu.Unlock()
	respond(w, http.StatusOK, j)
}

func (b *bridge) reply(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	if readJSON(w, r, &input) != 0 {
		return
	}
	if input.Text == "" || len(input.Text) > (8<<10)+32 {
		fail(w, http.StatusBadRequest, "Invalid worker reply.")
		return
	}
	b.mu.Lock()
	j := b.pending
	if j == nil || j.ID != input.ID || !j.claimed || j.finished || j.ctx.Err() != nil {
		b.mu.Unlock()
		fail(w, http.StatusConflict, "This request is no longer awaiting a worker reply.")
		return
	}
	j.finished = true
	j.result <- input.Text
	b.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func completion(w http.ResponseWriter, id, prompt, answer string, stream bool) {
	result := compatResult{content: answer, usage: estimateUsage(len(prompt), len(answer))}
	writeCompatibility(w, id, modelID, result, stream, nil)
}

type countedBody struct {
	io.ReadCloser
	bytes *int
}

func (b *countedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	*b.bytes += n
	return n, err
}

func readJSON(w http.ResponseWriter, r *http.Request, value any) int {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, "Send application/json.")
		return http.StatusUnsupportedMediaType
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if decoder.Decode(value) != nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
		fail(w, http.StatusBadRequest, "Invalid JSON request.")
		return http.StatusBadRequest
	}
	return 0
}

func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]any{"error": map[string]string{"message": message}})
}

func respond(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}
