// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/router"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

var errToolRepair = errors.New("tool block needs a repair attempt")

var webDebug = os.Getenv("ALLAIIS_WEB_DEBUG") != ""

// webShape renders a web reply in one inbound protocol.
type webShape interface {
	reasoning(string)
	text(string)
	calls([]toolCall)
	end(reason string)
	reply(content string, calls []toolCall, usage map[string]any)
}

// webCompletion serves a route whose provider has no documented API.
func (g *Gateway) webCompletion(w http.ResponseWriter, r *http.Request, protocol config.Protocol, target router.Target, payload []byte, stream bool) {
	credential, err := webprov.ParseCredential(target.Entry.Key)
	if err != nil {
		writeError(w, protocol, http.StatusServiceUnavailable, "credential_invalid",
			"The stored web session is unreadable. Capture it again.")
		return
	}
	if credential.DistinctID == "" {
		credential.DistinctID = randomID()
	}
	var request webprov.Request
	var reason *webReason
	var tools []toolSpec
	switch protocol {
	case config.Chat:
		request, reason, tools, err = webRequest(payload, target.Route.UpstreamModel)
	case config.Messages:
		request, reason, tools, err = messagesRequest(payload, target.Route.UpstreamModel)
		if err == nil && stream && request.MaxTokens <= 0 {
			request.MaxTokens = 4096
		}
	default:
		writeError(w, protocol, http.StatusBadRequest, "protocol_unsupported",
			"Web-transport providers serve Chat Completions and Anthropic Messages.")
		return
	}
	if err != nil {
		writeError(w, protocol, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	envelope := ""
	if len(tools) > 0 {
		envelope = toolEnvelope(reason.system, tools)
		request.Messages = reason.messages(envelope)
	}
	emulated := "usage_estimate"
	if len(tools) > 0 {
		emulated = "tools,usage_estimate"
	}
	w.Header().Set(tierHeader, "tier_c3")
	w.Header().Set("X-ALLAIIS-Provider", target.Route.Provider)
	w.Header().Set("X-ALLAIIS-Model", target.Route.ID)
	w.Header().Set("X-ALLAIIS-Emulated-Features", emulated)

	id := replyID(protocol)
	created := time.Now().Unix()
	sent := false
	finish := "stop"
	var calls []toolCall

	guard := &toolGuard{}
	guard.add(tools)

	shape := newShape(protocol, w, id, target.Route.ID, created)

	var startStream func()
	firstSeen, held := false, ""
	emit := func(event webprov.Event) error {
		if webDebug {
			slog.Info("web event", "kind", event.Kind, "len", len(event.Content))
		}
		if event.ServingModel != "" {
			w.Header().Set("X-ALLAIIS-Upstream-Model", event.ServingModel)
		}
		switch event.Kind {
		case "reasoning":
			if !reason.reasoning {
				return nil
			}
			reason.reasoningChars += len([]rune(event.Content))
			if stream {
				startStream()
				shape.reasoning(event.Content)
			}
			return nil
		case "content":
			text := event.Content
			if len(tools) > 0 {
				safe, parsed, err := guard.Write(event.Content)
				if err != nil {
					return errToolRepair
				}
				text = safe
				if len(parsed) > 0 {
					calls = append(calls, parsed...)
					if stream {
						startStream()
						shape.calls(parsed)
					}
				}
			}
			// Venice opens the stream with a literal "processing" status chunk.
			if !firstSeen {
				firstSeen = true
				held = text
				return nil
			}
			if held != "" {
				if held == "processing" {
					text = strings.TrimPrefix(text, " ")
				} else {
					text = held + text
				}
				held = ""
			}
			if text == "" {
				return nil
			}
			reason.append(text)
			if stream {
				startStream()
				shape.text(text)
			}
			return nil
		}
		return nil
	}
	startStream = func() {
		if sent || !stream {
			return
		}
		sent = true
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
	}

	started := time.Now()
	attempts := 0
	for {
		attempts++
		ctx, cancel := context.WithCancel(r.Context())
		err = g.web.Chat(ctx, credential, request, emit)
		cancel()
		if errors.Is(err, webprov.ErrUnauthorized) {
			g.web.Forget(credential)
		}
		if errors.Is(err, errToolRepair) && attempts < 2 && !sent {
			request.Messages = repairMessages(reason.messages(envelope), reason.text.String())
			reason.text.Reset()
			continue
		}
		break
	}
	if held != "" {
		reason.append(held)
		if stream {
			startStream()
			shape.text(held)
		}
		held = ""
	}
	if len(tools) > 0 {
		if tail, parsed, parseErr := guard.Flush(); parseErr == nil {
			if tail != "" {
				reason.append(tail)
				if stream {
					startStream()
					shape.text(tail)
				}
			}
			calls = append(calls, parsed...)
			if len(parsed) > 0 && stream {
				startStream()
				shape.calls(parsed)
			}
		}
	}
	if len(calls) > 0 {
		finish = "tool_calls"
	}
	if err != nil && sent {
		writeStreamError(w, protocol, err)
		slog.Warn("web provider stream failed", "provider", target.Route.Provider, "error", err)
		return
	}
	if err != nil {
		g.webError(w, protocol, target, err)
		return
	}
	promptTokens := estimateChars(len(payload))
	usage := map[string]any{
		"prompt_tokens": promptTokens, "completion_tokens": reason.chars(),
		"total_tokens": promptTokens + reason.chars(), "estimated": true,
	}
	if stream {
		startStream()
		shape.end(finish)
	} else {
		shape.reply(reason.text.String(), calls, usage)
	}
	slog.Info("web completion", "provider", target.Route.Provider, "model", target.Route.ID,
		"protocol", protocol, "stream", stream, "chars", reason.chars(), "calls", len(calls),
		"attempts", attempts, "duration_ms", time.Since(started).Milliseconds())
}

func replyID(protocol config.Protocol) string {
	if protocol == config.Messages {
		return "msg_" + randomID()
	}
	return "chatcmpl-" + randomID()
}

// writeStreamError reports a mid-stream failure in the inbound protocol.
func writeStreamError(w http.ResponseWriter, protocol config.Protocol, err error) {
	switch protocol {
	case config.Messages:
		payload, _ := json.Marshal(map[string]any{
			"type": "error", "error": map[string]string{"type": "api_error", "message": err.Error()},
		})
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", payload)
	default:
		fmt.Fprintf(w, "data: {\"error\":{\"message\":%q,\"type\":\"upstream_error\"}}\n\n", err.Error())
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
}

func (g *Gateway) webError(w http.ResponseWriter, protocol config.Protocol, target router.Target, err error) {
	switch {
	case errors.Is(err, webprov.ErrRateLimited):
		until := time.Now().Add(10 * time.Minute)
		if reset, ok := webprov.RateLimitReset(err); ok {
			until = reset
		}
		persist, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if setErr := g.router.SetCooldown(persist, target, until); setErr != nil {
			slog.Warn("could not persist provider cooldown", "provider", target.Route.Provider)
		}
		rateLimited(w, protocol, until)
	case errors.Is(err, webprov.ErrSignInRequired):
		writeError(w, protocol, http.StatusBadRequest, "sign_in_required",
			"This model needs a signed-in web session. Log in through `allaiis web chrome` and capture it again.")
	case errors.Is(err, webprov.ErrUnauthorized):
		writeError(w, protocol, http.StatusBadGateway, "web_session_expired",
			"The web session expired. Re-run the capture command for this account.")
	case errors.Is(err, webprov.ErrCloudflare):
		writeError(w, protocol, http.StatusBadGateway, "cloudflare_challenge",
			"Cloudflare rejected the stored clearance. Re-run the capture command for this account.")
	case errors.Is(err, webprov.ErrContextTooLong()):
		writeError(w, protocol, http.StatusBadRequest, "context_length_exceeded",
			"The prompt exceeds the model context window.")
	case errors.Is(err, webprov.ErrUnknownModel()):
		writeError(w, protocol, http.StatusNotFound, "model_not_found",
			"The web app does not offer this model for the captured session.")
	default:
		writeError(w, protocol, http.StatusBadGateway, "upstream_error", "The web provider failed: "+err.Error())
	}
}

// webReason accumulates the reply and keeps the request history for a repair.
type webReason struct {
	text           strings.Builder
	system         string
	turns          []webprov.Message
	reasoning      bool
	charsN         int
	reasoningChars int
}

func (r *webReason) append(text string) {
	r.text.WriteString(text)
	r.charsN += len([]rune(text))
}

func (r *webReason) chars() int { return r.charsN }

// messages rebuilds the turn list with the current system instruction.
func (r *webReason) messages(system string) []webprov.Message {
	built := append([]webprov.Message{}, r.turns...)
	if system == "" {
		return built
	}
	if len(built) > 0 && built[0].Role == "system" {
		built[0].Content = system
		return built
	}
	return append([]webprov.Message{{Role: "system", Content: system}}, built...)
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
func webRequest(payload []byte, model string) (webprov.Request, *webReason, []toolSpec, error) {
	var input struct {
		Messages []struct {
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			ToolCalls json.RawMessage `json:"tool_calls"`
		} `json:"messages"`
		Temperature *float64 `json:"temperature"`
		TopP        *float64 `json:"top_p"`
		MaxTokens   int      `json:"max_tokens"`
		Reasoning   struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		ReasoningEffort string     `json:"reasoning_effort"`
		Tools           []toolSpec `json:"tools"`
		ToolChoice      any        `json:"tool_choice"`
	}
	if err := json.Unmarshal(payload, &input); err != nil {
		return webprov.Request{}, nil, nil, errors.New("expected a JSON object")
	}
	request := webprov.Request{
		RequestID: randomID(), Model: model,
		Temperature: input.Temperature, TopP: input.TopP, MaxTokens: input.MaxTokens,
	}
	request.Reasoning = input.ReasoningEffort != "" || input.Reasoning.Effort != "" ||
		strings.Contains(strings.ToLower(model), "reasoning")
	reason := &webReason{reasoning: request.Reasoning}
	for _, message := range input.Messages {
		text, images := messageParts(message.Content)
		switch message.Role {
		case "system", "developer":
			if reason.system == "" {
				reason.system = text
			} else {
				reason.system += "\n\n" + text
			}
		case "tool":
			reason.turns = append(reason.turns, webprov.Message{Role: "user", Content: text, Images: images})
		case "assistant":
			if text == "" {
				text = renderToolCalls(text, message.ToolCalls)
			}
			if text == "" {
				text = "(empty)"
			}
			reason.turns = append(reason.turns, webprov.Message{Role: "assistant", Content: text})
		default:
			reason.turns = append(reason.turns, webprov.Message{Role: "user", Content: text, Images: images})
		}
	}
	tools := input.Tools
	if choice, ok := input.ToolChoice.(string); ok && choice == "none" {
		tools = nil
	}
	if len(reason.turns) == 0 {
		reason.turns = append(reason.turns, webprov.Message{Role: "user", Content: ""})
	}
	request.Messages = reason.messages(reason.system)
	return request, reason, tools, nil
}

// messageParts joins text parts and decodes inline data-URL images.
func messageParts(raw json.RawMessage) (string, []webprov.Image) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
		File *struct {
			Name string `json:"filename"`
			Data string `json:"file_data"`
		} `json:"file"`
		ToolResult json.RawMessage `json:"content"`
		URL        string          `json:"url"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return string(raw), nil
	}
	var joined strings.Builder
	var images []webprov.Image
	for _, part := range parts {
		if part.Text != "" {
			joined.WriteString(part.Text)
		}
		url := part.URL
		if part.ImageURL != nil {
			url = part.ImageURL.URL
		}
		if image, ok := decodeDataURL(url); ok {
			images = append(images, image)
		} else if image, ok := fetchImage(url); ok {
			images = append(images, image)
		}
		if strings.HasPrefix(part.Type, "tool_result") && part.ToolResult != nil {
			result, _ := messageParts(part.ToolResult)
			if result != "" {
				joined.WriteString("\n" + result)
			}
		}
		if part.File != nil {
			if file, ok := decodeDataURL(part.File.Data); ok {
				file.Name = part.File.Name
				images = append(images, file)
			}
		}
	}
	return joined.String(), images
}

// fetchImage downloads an http(s) image and returns its bytes.
func fetchImage(raw string) (webprov.Image, bool) {
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return webprov.Image{}, false
	}
	request, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		return webprov.Image{}, false
	}
	request.Header.Set("User-Agent", "ALLAIIS/0.1")
	response, err := imageClient.Do(request)
	if err != nil {
		return webprov.Image{}, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return webprov.Image{}, false
	}
	mime := strings.Split(response.Header.Get("Content-Type"), ";")[0]
	if !strings.HasPrefix(mime, "image/") {
		return webprov.Image{}, false
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxRequest))
	if err != nil || len(data) == 0 {
		return webprov.Image{}, false
	}
	return webprov.Image{Data: data, MIME: mime}, true
}

var imageClient = &http.Client{Timeout: 20 * time.Second}

// decodeDataURL turns a `data:image/...;base64,...` URL into bytes.
func decodeDataURL(raw string) (webprov.Image, bool) {
	if !strings.HasPrefix(raw, "data:") || len(raw) > maxRequest {
		return webprov.Image{}, false
	}
	header, payload, found := strings.Cut(strings.TrimPrefix(raw, "data:"), ",")
	if !found || !strings.HasSuffix(header, ";base64") {
		return webprov.Image{}, false
	}
	mime := strings.TrimSuffix(header, ";base64")
	if mime == "" {
		return webprov.Image{}, false
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return webprov.Image{}, false
	}
	return webprov.Image{Data: data, MIME: mime}, true
}

func messageToolCalls(raw json.RawMessage) []toolCall {
	var list []toolCall
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var holder struct {
		ToolCalls []toolCall `json:"tool_calls"`
	}
	json.Unmarshal(raw, &holder)
	return holder.ToolCalls
}

// renderToolCalls replays an earlier assistant call as text the web model can read.
func renderToolCalls(text string, raw json.RawMessage) string {
	return renderCallList(text, messageToolCalls(raw))
}

func renderCallList(text string, calls []toolCall) string {
	if len(calls) == 0 {
		return text
	}
	var block strings.Builder
	block.WriteString(text)
	block.WriteString("\n" + toolOpen + `{"calls":[`)
	for i, call := range calls {
		if i > 0 {
			block.WriteString(",")
		}
		fmt.Fprintf(&block, `{"name":%q,"arguments":%s}`, call.Function.Name, call.Function.Arguments)
	}
	block.WriteString(`]}` + toolClose)
	return block.String()
}
