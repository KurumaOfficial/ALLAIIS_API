// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/router"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

// webCompletion serves a route whose provider has no documented API.
func (g *Gateway) webCompletion(w http.ResponseWriter, r *http.Request, protocol config.Protocol, target router.Target, payload []byte, stream bool) {
	if protocol != config.Chat {
		writeError(w, protocol, http.StatusBadRequest, "protocol_unsupported",
			"Web-transport providers serve Chat Completions only.")
		return
	}
	credential, err := webprov.ParseCredential(target.Entry.Key)
	if err != nil {
		writeError(w, protocol, http.StatusServiceUnavailable, "credential_invalid",
			"The stored web session is unreadable. Capture it again.")
		return
	}
	if credential.DistinctID == "" {
		credential.DistinctID = randomID()
	}
	request, reason, err := webRequest(payload, target.Route.UpstreamModel, stream)
	if err != nil {
		writeError(w, protocol, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	w.Header().Set(tierHeader, "tier_c3")
	w.Header().Set("X-ALLAIIS-Provider", target.Route.Provider)
	w.Header().Set("X-ALLAIIS-Model", target.Route.ID)
	w.Header().Set("X-ALLAIIS-Emulated-Features", "usage_estimate")

	id := "chatcmpl-" + randomID()
	created := time.Now().Unix()
	sent := false
	emit := func(event webprov.Event) error {
		if event.ServingModel != "" {
			w.Header().Set("X-ALLAIIS-Upstream-Model", event.ServingModel)
		}
		if event.Kind == "content" || (event.Kind == "reasoning" && reason.reasoning) {
			reason.append(event)
		}
		if !stream {
			return nil
		}
		if !sent {
			sent = true
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
		}
		var delta map[string]string
		switch event.Kind {
		case "content":
			delta = map[string]string{"content": event.Content}
		case "reasoning":
			if !reason.reasoning {
				return nil
			}
			delta = map[string]string{"reasoning_content": event.Content}
		default:
			return nil
		}
		writeChunk(w, id, target.Route.ID, created, delta)
		return nil
	}

	started := time.Now()
	err = g.web.Chat(r.Context(), credential, request, emit)
	if errors.Is(err, webprov.ErrUnauthorized) {
		g.web.Forget(credential)
	}
	if err != nil && sent {
		fmt.Fprintf(w, "data: {\"error\":{\"message\":%q,\"type\":\"upstream_error\"}}\n\n", err.Error())
		fmt.Fprint(w, "data: [DONE]\n\n")
		slog.Warn("web provider stream failed", "provider", target.Route.Provider, "error", err)
		return
	}
	if err != nil {
		g.webError(w, protocol, target, err)
		return
	}
	if stream {
		if !sent {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
		}
		reason.finish(w, id, target.Route.ID, created)
		fmt.Fprint(w, "data: [DONE]\n\n")
	} else {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": id, "object": "chat.completion", "created": created, "model": target.Route.ID,
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": reason.text.String()},
			}},
			"usage": map[string]any{
				"prompt_tokens": estimateChars(len(payload)), "completion_tokens": reason.chars(),
				"total_tokens": estimateChars(len(payload)) + reason.chars(), "estimated": true,
			},
		})
	}
	slog.Info("web completion", "provider", target.Route.Provider, "model", target.Route.ID,
		"stream", stream, "chars", reason.chars(), "reasoning", reason.reasoningChars(),
		"duration_ms", time.Since(started).Milliseconds())
}

func (g *Gateway) webError(w http.ResponseWriter, protocol config.Protocol, target router.Target, err error) {
	switch {
	case errors.Is(err, webprov.ErrRateLimited):
		until := time.Now().Add(10 * time.Minute)
		persist, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if setErr := g.router.SetCooldown(persist, target, until); setErr != nil {
			slog.Warn("could not persist provider cooldown", "provider", target.Route.Provider)
		}
		rateLimited(w, protocol, until)
	case errors.Is(err, webprov.ErrUnauthorized):
		writeError(w, protocol, http.StatusBadGateway, "web_session_expired",
			"The web session expired. Re-run the capture command for this account.")
	case errors.Is(err, webprov.ErrCloudflare):
		writeError(w, protocol, http.StatusBadGateway, "cloudflare_challenge",
			"Cloudflare rejected the stored clearance. Re-run the capture command for this account.")
	case errors.Is(err, webprov.ErrContextTooLong()):
		writeError(w, protocol, http.StatusBadRequest, "context_length_exceeded",
			"The prompt exceeds the model context window.")
	default:
		writeError(w, protocol, http.StatusBadGateway, "upstream_error", "The web provider failed: "+err.Error())
	}
}

// webReason keeps the accumulated text so a non-stream reply can be built.
type webReason struct {
	text      strings.Builder
	reasoning bool
	charsN    int
	reasonN   int
}

func (r *webReason) append(event webprov.Event) {
	if event.Kind == "reasoning" {
		r.reasonN += len([]rune(event.Content))
		return
	}
	r.text.WriteString(event.Content)
	r.charsN += len([]rune(event.Content))
}

func (r *webReason) chars() int          { return r.charsN }
func (r *webReason) reasoningChars() int { return r.reasonN }

func (r *webReason) finish(w http.ResponseWriter, id, model string, created int64) {
	finish := map[string]any{}
	data, _ := json.Marshal(map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": finish, "finish_reason": "stop"}},
	})
	fmt.Fprintf(w, "data: %s\n\n", data)
}

func writeChunk(w http.ResponseWriter, id, model string, created int64, delta map[string]string) {
	data, _ := json.Marshal(map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}},
	})
	fmt.Fprintf(w, "data: %s\n\n", data)
}

func estimateChars(n int) int {
	if n <= 0 {
		return 0
	}
	return n/4 + 1
}

func randomID() string {
	var raw [8]byte
	rand.Read(raw[:])
	return fmt.Sprintf("%x", raw[:])
}

// webRequest flattens an OpenAI payload into the web protocol fields.
func webRequest(payload []byte, model string, stream bool) (webprov.Request, *webReason, error) {
	var input struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Temperature *float64 `json:"temperature"`
		TopP        *float64 `json:"top_p"`
		MaxTokens   int      `json:"max_tokens"`
		Reasoning   struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		ReasoningEffort string `json:"reasoning_effort"`
		Tools           []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(payload, &input); err != nil {
		return webprov.Request{}, nil, errors.New("expected a JSON object")
	}
	request := webprov.Request{
		Model: model, Temperature: input.Temperature, TopP: input.TopP, MaxTokens: input.MaxTokens,
	}
	request.Reasoning = input.ReasoningEffort != "" || input.Reasoning.Effort != "" ||
		strings.Contains(strings.ToLower(model), "reasoning")
	for _, message := range input.Messages {
		text := messageText(message.Content)
		if message.Role == "tool" || message.Role == "developer" {
			message.Role = "user"
		}
		request.Messages = append(request.Messages, webprov.Message{Role: message.Role, Content: text})
	}
	return request, &webReason{reasoning: request.Reasoning}, nil
}

// messageText joins text parts and renders simple tool results.
func messageText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var joined strings.Builder
		for _, part := range parts {
			if part.Text != "" {
				joined.WriteString(part.Text)
			}
		}
		return joined.String()
	}
	return string(raw)
}
