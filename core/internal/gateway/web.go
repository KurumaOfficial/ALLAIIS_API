// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"bytes"
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

var errTooManyImages = errors.New("too many attachments for this model")

var webDebug = os.Getenv("ALLAIIS_WEB_DEBUG") != ""

// webShape renders a web reply in one inbound protocol.
type webShape interface {
	reasoning(string)
	text(string)
	calls([]toolCall)
	end(reason string, usage map[string]any)
	reply(content, reasoning string, calls []toolCall, usage map[string]any, finish string)
}

// webCompletion serves a route whose provider has no documented API.
func (g *Gateway) webCompletion(w http.ResponseWriter, r *http.Request, protocol config.Protocol, target router.Target, payload []byte, stream bool) (chars, callsOut int) {
	credential, err := webprov.ParseCredential(target.Entry.Key)
	if err != nil {
		writeError(w, protocol, http.StatusServiceUnavailable, "credential_invalid",
			"The stored web session is unreadable. Capture it again.")
		return 0, 0
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
			request.MaxTokens = defaultResponse
		}
	case config.Responses:
		request, reason, tools, err = responsesRequest(payload, target.Route.UpstreamModel)
	default:

		return
	}
	if err != nil {
		writeError(w, protocol, http.StatusBadRequest, "invalid_request", err.Error())
		return 0, 0
	}
	if request.Parent != "" {
		if raw, ok := g.table().routes.LoadResponse(r.Context(), request.Parent); ok {
			var previous []webprov.Message
			if json.Unmarshal(raw, &previous) == nil && len(previous) > 0 {
				reason.turns = append(previous, reason.turns...)
			}
		}
		request.Messages = reason.messages(reason.system)
	}
	if target.Route.ReasoningRoundTrip {
		g.attachReasoning(target.Route.ID, reason.turns)
		request.Messages = reason.messages(reason.system)
	}
	if err := applyCapabilities(&request, target.Route); err != nil {
		writeError(w, protocol, http.StatusBadRequest, "invalid_request", err.Error())
		return 0, 0
	}
	// Capabilities decide whether thinking is requested and surfaced.
	reason.reasoning = request.Reasoning
	if limit := toolLimit(target.Route); len(tools) > limit {
		writeError(w, protocol, http.StatusBadRequest, "too_many_tools",
			fmt.Sprintf("This model accepts at most %d tool(s).", limit))
		return 0, 0
	}
	if target.Route.WebSearch {
		request.WebSearch = true
	}
	if limit := imageLimit(target.Route); request.ImageCount() > limit {
		writeError(w, protocol, http.StatusBadRequest, "too_many_attachments",
			fmt.Sprintf("This model accepts at most %d attachment(s).", limit))
		return 0, 0
	}
	if target.Route.MaxVideos > 0 && request.VideoCount() > target.Route.MaxVideos {
		writeError(w, protocol, http.StatusBadRequest, "too_many_attachments",
			fmt.Sprintf("This model accepts at most %d video(s).", target.Route.MaxVideos))
		return 0, 0
	}
	envelope := ""
	if len(tools) > 0 {
		envelope = toolEnvelope(reason.system, tools)
		request.Messages = reason.messages(envelope)
	}
	if target.Route.StrictOrder {
		request.Messages = mergeSameRole(request.Messages)
	}
	if err := checkContext(reason.system, request.Messages, tools, target.Route); err != nil {
		writeError(w, protocol, http.StatusBadRequest, "context_length_exceeded", err.Error())
		return 0, 0
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
	// An argument-less call is held back while nothing has reached the client,
	// so a repair turn can replace it instead of a client rejection.
	var heldCalls []toolCall

	guard := &toolGuard{probe: r.Header.Get(probeHeader) != ""}
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
			reason.reasoningText.WriteString(event.Content)
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
					if webDebug {
						slog.Info("guard error", "err", err.Error(), "buf", guard.buf.String())
					}
					return errToolRepair
				}
				text = safe
				if len(parsed) > 0 {
					reason.callsChars(parsed)
					if !sent && len(calls) == 0 && strings.TrimSpace(text) == "" && guard.incompleteCalls(parsed) {
						heldCalls = append(heldCalls, parsed...)
						return nil
					}
					calls = append(calls, parsed...)
					guard.logMissing(parsed)
					if strings.TrimSpace(text) == "" {
						text = ""
					}
					if stream {
						startStream()
						shape.calls(parsed)
					}
				}
			}
			if text == "" {
				return nil
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
			reason.reasoningText.Reset()
			guard.reset()
			firstSeen, held = false, ""
			continue
		}
		// A 200 with no content at all is worth one more try, like a bad block;
		// so is a call that forgot its arguments while the client still saw
		// nothing.
		empty := reason.text.Len() == 0 && len(calls) == 0 && len(heldCalls) == 0 && (held == "" || held == "processing")
		incomplete := len(heldCalls) > 0 && !sent && reason.text.Len() == 0
		if err == nil && (empty || incomplete) && attempts < 2 && !sent {
			reason.reasoningText.Reset()
			guard.reset()
			heldCalls = nil
			firstSeen, held = false, ""
			continue
		}
		break
	}
	if len(heldCalls) > 0 {
		// No repair was possible or left; deliver what came.
		guard.logMissing(heldCalls)
		calls = append(calls, heldCalls...)
		if stream {
			startStream()
			shape.calls(heldCalls)
		}
		heldCalls = nil
	}
	if held != "" {
		// A lone "processing" status is not an answer.
		if held != "processing" {
			reason.append(held)
			if stream {
				startStream()
				shape.text(held)
			}
		}
		held = ""
	}
	if len(tools) > 0 {
		raw := guard.buf.String()
		if tail, parsed, parseErr := guard.Flush(); parseErr == nil {
			if len(parsed) > 0 && strings.TrimSpace(tail) == "" {
				tail = ""
			}
			if tail != "" {
				reason.append(tail)
				if stream {
					startStream()
					shape.text(tail)
				}
			}
			calls = append(calls, parsed...)
			if len(parsed) > 0 {
				reason.callsChars(parsed)
			}
			if len(parsed) > 0 && stream {
				startStream()
				shape.calls(parsed)
			}
		} else if tail := strings.TrimSpace(trimPartialClose(strings.TrimPrefix(raw, toolOpen), toolClose)); tail != "" {
			reason.append(tail)
			if stream {
				startStream()
				shape.text(tail)
			}
		}
	}
	// Thinking is output too, even when the route does not surface it.
	completion := reason.chars() + len([]rune(reason.reasoningText.String()))
	if len(calls) > 0 {
		finish = "tool_calls"
	} else if request.MaxTokens > 0 && completion >= request.MaxTokens*3 {
		// Venice sends no finish reason; a reply near the cap was cut.
		finish = "length"
	}
	if target.Route.ReasoningRoundTrip {
		// A client replays the assistant turn either as bare prose or as the
		// rendered call block, so the thinking is stored under both shapes.
		thinking := reason.reasoningText.String()
		assistant := reason.text.String()
		if len(calls) > 0 {
			g.reasoning.save(target.Route.ID, renderCallList(assistant, calls), thinking)
		}
		if strings.TrimSpace(assistant) != "" {
			g.reasoning.save(target.Route.ID, assistant, thinking)
		}
	}
	if err != nil && sent {
		writeStreamError(w, protocol, err)
		slog.Warn("web provider stream failed", "request_id", requestID(r),
			"provider", target.Route.Provider, "error", err)
		return reason.chars(), len(calls)
	}
	if err != nil {
		g.webError(w, protocol, target, err)
		return reason.chars(), len(calls)
	}
	promptTokens := estimateChars(len(payload))
	usage := map[string]any{
		"prompt_tokens": promptTokens, "completion_tokens": completion,
		"total_tokens": promptTokens + completion, "estimated": true,
	}
	if stream {
		startStream()
		report := usage
		if protocol == config.Chat && !reason.includeUsage {
			report = nil
		}
		shape.end(finish, report)
	} else {
		shape.reply(reason.text.String(), reason.reasoningText.String(), calls, usage, finish)
	}
	if protocol == config.Responses {
		reply := reason.text.String()
		if len(calls) > 0 {
			reply = renderCallList(reply, calls)
		}
		turns := append(append([]webprov.Message{}, reason.turns...), webprov.Message{Role: "assistant", Content: reply})
		if raw, err := json.Marshal(turns); err == nil {
			persist, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			if saveErr := g.table().routes.SaveResponse(persist, id, raw, time.Now().Add(time.Hour)); saveErr != nil {
				slog.Warn("could not persist response turns", "error", saveErr)
			}
			cancel()
		}
	}
	slog.Info("web completion", "request_id", requestID(r),
		"provider", target.Route.Provider, "model", target.Route.ID,
		"protocol", protocol, "stream", stream, "chars", reason.chars(), "calls", len(calls),
		"attempts", attempts, "duration_ms", time.Since(started).Milliseconds())
	return reason.chars(), len(calls)
}

const (
	defaultResponse   = 8192
	defaultImageLimit = 10
	defaultToolLimit  = 64
	defaultInactivity = 2 * time.Minute
	maxInactivity     = 10 * time.Minute
)

// inactivity clamps the model's suggested stream watchdog.
func inactivity(route config.Route) time.Duration {
	window := defaultInactivity
	if route.InactivityMS > 0 {
		window = time.Duration(route.InactivityMS) * time.Millisecond
	}
	if window < time.Second {
		window = time.Second
	}
	if window > maxInactivity {
		window = maxInactivity
	}
	return window
}

// mergeSameRole joins consecutive turns of one role for strict models.
func mergeSameRole(messages []webprov.Message) []webprov.Message {
	if len(messages) < 2 {
		return messages
	}
	merged := make([]webprov.Message, 0, len(messages))
	merged = append(merged, messages[0])
	for _, message := range messages[1:] {
		last := &merged[len(merged)-1]
		if message.Role == last.Role && message.Role != "system" {
			last.Content = strings.Join([]string{last.Content, message.Content}, "\n\n")
			last.Images = append(last.Images, message.Images...)
			continue
		}
		merged = append(merged, message)
	}
	return merged
}

func imageLimit(route config.Route) int {
	if route.MaxImages > 0 {
		return route.MaxImages
	}
	return defaultImageLimit
}

func toolLimit(route config.Route) int {
	if route.MaxTools > 0 {
		return route.MaxTools
	}
	return defaultToolLimit
}

// applyCapabilities shapes the request with the discovered model limits.
func applyCapabilities(request *webprov.Request, route config.Route) error {
	request.LargeContext = route.LargeContext
	request.Inactivity = inactivity(route)
	if route.Reasoning != nil {
		request.Reasoning = *route.Reasoning
	}
	if route.Temperature != nil && request.Temperature == nil {
		value := *route.Temperature
		request.Temperature = &value
	}
	if route.NoSampling {
		request.Temperature, request.TopP = nil, nil
	} else if route.TopP != nil && request.TopP == nil {
		value := *route.TopP
		request.TopP = &value
	}
	if !route.Frequency {
		request.FrequencyPenalty = nil
	}
	if !route.Presence {
		request.PresencePenalty = nil
	}
	if !route.MinP {
		request.MinP = nil
	}
	request.IncludeAnonUserID = route.AnonUserID
	if request.MaxTokens <= 0 {
		request.MaxTokens = defaultResponse
	}
	if route.MaxResponse > 0 && request.MaxTokens > route.MaxResponse {
		request.MaxTokens = route.MaxResponse
	}
	if route.MaxContext > 0 && request.MaxTokens > route.MaxContext {
		request.MaxTokens = route.MaxContext
	}
	return nil
}

// attachReasoning adds stored thinking to replayed assistant turns.
func (g *Gateway) attachReasoning(alias string, turns []webprov.Message) {
	for index := range turns {
		if turns[index].Role == "assistant" && turns[index].Reasoning == "" {
			turns[index].Reasoning = g.reasoning.load(alias, turns[index].Content)
			if webDebug {
				slog.Info("reasoning replay", "alias", alias,
					"hit", turns[index].Reasoning != "", "chars", len([]rune(turns[index].Content)))
			}
		}
	}
}

// checkContext rejects a prompt that cannot fit before spending a request.
func checkContext(system string, messages []webprov.Message, tools []toolSpec, route config.Route) error {
	if route.MaxContext <= 0 {
		return nil
	}
	size := len(system)
	for _, message := range messages {
		size += len(message.Content)
	}
	for _, tool := range tools {
		size += len(tool.Function.Description) + len(tool.Function.Parameters)
	}
	if estimateChars(size) > route.MaxContext {
		return fmt.Errorf("The prompt is about %d tokens; the model accepts %d.",
			estimateChars(size), route.MaxContext)
	}
	return nil
}

func replyID(protocol config.Protocol) string {
	switch protocol {
	case config.Messages:
		return "msg_" + randomID()
	case config.Responses:
		return "resp_" + randomID()
	default:
		return "chatcmpl-" + randomID()
	}
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
		until := time.Now().Add(maxRateCooldown)
		if reset, ok := webprov.RateLimitReset(err); ok && reset.Before(until) {
			until = reset
		}
		persist, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if setErr := g.table().routes.SetCooldown(persist, target, until); setErr != nil {
			slog.Warn("could not persist provider cooldown", "provider", target.Route.Provider)
		}
		rateLimited(w, protocol, until)
	case errors.Is(err, webprov.ErrSignInRequired):
		writeError(w, protocol, http.StatusBadRequest, "sign_in_required",
			"This model needs a signed-in web session. Log in through `allaiis web chrome` and capture it again, or press Recapture in the control panel.")
	case errors.Is(err, webprov.ErrUnauthorized):
		writeError(w, protocol, http.StatusBadGateway, "web_session_expired",
			"The web session expired. Re-run the capture command for this account.")
	case errors.Is(err, webprov.ErrCloudflare):
		writeError(w, protocol, http.StatusBadGateway, "cloudflare_challenge",
			"Cloudflare rejected the stored clearance. Recapture this account with `allaiis web login` or the panel's Recapture button.")
	case errors.Is(err, webprov.ErrInsufficientBalance()):
		writeError(w, protocol, http.StatusPaymentRequired, "insufficient_balance",
			"This session has no messages left. Check the reset time with `allaiis web quota` or use another label.")
	case errors.Is(err, webprov.ErrStreamStalled()):
		writeError(w, protocol, http.StatusGatewayTimeout, "upstream_timeout",
			"The provider stopped sending for too long. Retry, or raise the alias inactivity window.")
	case errors.Is(err, webprov.ErrContextTooLong()):
		writeError(w, protocol, http.StatusBadRequest, "context_length_exceeded",
			"The prompt exceeds the model context window.")
	case errors.Is(err, webprov.ErrUnknownModel()):
		message := "The web app does not offer this model for the captured session."
		if target.Route.Replacement != "" {
			message += " Try " + target.Route.Replacement + "."
		}
		writeError(w, protocol, http.StatusNotFound, "model_not_found", message)
	default:
		writeError(w, protocol, http.StatusBadGateway, "upstream_error", "The web provider failed: "+err.Error())
	}
}

// webReason accumulates the reply and keeps the request history for a repair.
type webReason struct {
	text           strings.Builder
	reasoningText  strings.Builder
	system         string
	turns          []webprov.Message
	reasoning      bool
	includeUsage   bool
	charsN         int
	reasoningChars int
}

func (r *webReason) append(text string) {
	r.text.WriteString(text)
	r.charsN += len([]rune(text))
}

// callsChars counts tool JSON towards the output estimate.
func (r *webReason) callsChars(calls []toolCall) {
	for _, call := range calls {
		r.charsN += len([]rune(call.Function.Name)) + len([]rune(call.Function.Arguments))
	}
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
		ReasoningEffort  string   `json:"reasoning_effort"`
		FrequencyPenalty *float64 `json:"frequency_penalty"`
		MinP             *float64 `json:"min_p"`
		PresencePenalty  *float64 `json:"presence_penalty"`
		StreamOptions    struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
		Tools      []toolSpec `json:"tools"`
		ToolChoice any        `json:"tool_choice"`
	}
	if err := json.Unmarshal(payload, &input); err != nil {
		return webprov.Request{}, nil, nil, errors.New("expected a JSON object")
	}
	request := webprov.Request{
		RequestID: randomID(), Model: model,
		Temperature: input.Temperature, TopP: input.TopP, MaxTokens: input.MaxTokens,
		FrequencyPenalty: input.FrequencyPenalty, PresencePenalty: input.PresencePenalty, MinP: input.MinP,
	}
	request.ReasoningEffort = input.ReasoningEffort
	if request.ReasoningEffort == "" {
		request.ReasoningEffort = input.Reasoning.Effort
	}
	request.Reasoning = request.ReasoningEffort != "" ||
		strings.Contains(strings.ToLower(model), "reasoning")
	reason := &webReason{reasoning: request.Reasoning, includeUsage: input.StreamOptions.IncludeUsage}
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
			if strings.TrimSpace(text) == "" {
				text = renderToolCalls("", message.ToolCalls)
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
		name, err := json.Marshal(call.Function.Name)
		if err != nil {
			name = []byte(`""`)
		}
		fmt.Fprintf(&block, `{"name":%s,"arguments":%s}`, name,
			compactJSON(call.Function.Arguments))
	}
	block.WriteString(`]}` + toolClose)
	return block.String()
}

// compactJSON drops insignificant spaces so the replayed text matches the
// canonical form a client rebuilds from the same calls, which keeps the stored
// thinking findable.
func compactJSON(raw string) string {
	var out bytes.Buffer
	if err := json.Compact(&out, []byte(raw)); err != nil {
		return raw
	}
	return out.String()
}
