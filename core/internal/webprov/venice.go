// SPDX-License-Identifier: GPL-3.0-only

// Package webprov executes provider routes against browser web endpoints that
// have no documented API. The Venice transport speaks the web app's own
// inference protocol: Cloudflare clearance cookies, a Clerk session token and a
// signed client attestation are replayed around a newline-delimited event
// stream.
package webprov

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

const (
	// DefaultUserAgent matches the Chromium build the captured fingerprint came from.
	DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/158.0.0.0 Safari/537.36"

	veniceApp        = "https://venice.ai"
	veniceOuterface  = "https://outerface.venice.ai/api"
	veniceInterface  = "interface@20261121.222723+15e98df"
	veniceMiddleface = "0.1.1271"
	clerkBase        = "https://clerk.venice.ai"
)

var (
	ErrCloudflare     = errors.New("cloudflare clearance rejected; capture cookies again")
	ErrUnauthorized   = errors.New("venice session expired")
	ErrRateLimited    = errors.New("venice web rate limit reached")
	ErrSignInRequired = errors.New("this model needs a signed-in session")
)

// rateLimitError carries the upstream reset time when one was published.
type rateLimitError struct {
	until time.Time
}

func (e rateLimitError) Error() string        { return ErrRateLimited.Error() }
func (e rateLimitError) Is(target error) bool { return target == ErrRateLimited }

// RateLimitReset reports the upstream reset instant for a rate-limit error.
func RateLimitReset(err error) (time.Time, bool) {
	var limited rateLimitError
	if errors.As(err, &limited) && !limited.until.IsZero() {
		return limited.until, true
	}
	return time.Time{}, false
}

// RateLimitUntil extracts the published reset instant, if any.
func RateLimitUntil(response *http.Response, now time.Time) time.Time {
	value := strings.TrimSpace(response.Header.Get("x-ratelimit-resets"))
	millis, err := strconv.ParseInt(value, 10, 64)
	if err != nil || millis <= 0 {
		return time.Time{}
	}
	until := time.UnixMilli(millis)
	if !until.After(now) {
		return time.Time{}
	}
	return until
}

// Cookie is one browser cookie bound to a host.
type Cookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Credential is a captured Venice browser session.
type Credential struct {
	UserAgent  string   `json:"user_agent"`
	DistinctID string   `json:"distinct_id"`
	Venice     []Cookie `json:"venice,omitempty"`
	Clerk      []Cookie `json:"clerk,omitempty"`
}

// ParseCredential decodes the compact JSON form stored in the vault.
func ParseCredential(raw string) (Credential, error) {
	var cred Credential
	if err := json.Unmarshal([]byte(raw), &cred); err != nil {
		return Credential{}, fmt.Errorf("invalid venice-web credential: %w", err)
	}
	if cred.UserAgent == "" {
		cred.UserAgent = DefaultUserAgent
	}
	return cred, nil
}

// Encode returns the compact JSON form. Cookies with empty values are dropped.
func (c Credential) Encode() string {
	cred := Credential{UserAgent: c.UserAgent, DistinctID: c.DistinctID}
	for _, cookie := range c.Venice {
		if cookie.Value != "" {
			cred.Venice = append(cred.Venice, cookie)
		}
	}
	for _, cookie := range c.Clerk {
		if cookie.Value != "" {
			cred.Clerk = append(cred.Clerk, cookie)
		}
	}
	data, _ := json.Marshal(cred)
	return string(data)
}

func (c Credential) cookie(name string) string {
	for _, cookie := range c.Venice {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

func (c Credential) clerkCookie(name string) string {
	for _, cookie := range c.Clerk {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

// Message is one turn sent upstream, optionally with images.
type Message struct {
	Role    string  `json:"role"`
	Content string  `json:"content"`
	Images  []Image `json:"-"`
}

// Request is a provider-neutral chat request.
type Request struct {
	RequestID   string
	Model       string
	Messages    []Message
	Reasoning   bool
	Temperature *float64
	TopP        *float64
	MaxTokens   int
}

// Event is one decoded upstream event. Kind is "content", "reasoning" or "meta".
type Event struct {
	Kind         string
	Content      string
	ServingModel string
}

// Client talks to the Venice web inference API.
type Client struct {
	http      *http.Client
	base      string
	clerk     string
	now       func() time.Time
	mu        sync.Mutex
	tokens    map[string]cached        // session JWTs by credential hash
	attesting map[string][]cached      // client attestation token pool by user key
	locks     map[string]chan struct{} // one generation per credential
}

type cached struct {
	value   string
	expires time.Time
}

// New builds a client with a Chrome TLS fingerprint and HTTP/2.
func New(timeout time.Duration) *Client {
	transport := &http2.Transport{
		DialTLSContext: func(_ context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			raw, err := net.DialTimeout(network, addr, 15*time.Second)
			if err != nil {
				return nil, err
			}
			host, _, _ := net.SplitHostPort(addr)
			conn := utls.UClient(raw, &utls.Config{
				ServerName: host, NextProtos: []string{"h2", "http/1.1"},
			}, utls.HelloChrome_Auto)
			if err := conn.Handshake(); err != nil {
				return nil, err
			}
			return conn, nil
		},
	}
	return &Client{
		http:      &http.Client{Transport: transport, Timeout: timeout},
		base:      veniceOuterface,
		clerk:     clerkBase,
		now:       time.Now,
		tokens:    map[string]cached{},
		attesting: map[string][]cached{},
		locks:     map[string]chan struct{}{},
	}
}

// WithBase points the client at another host, used by tests.
func (c *Client) WithBase(base string) *Client {
	c.base = base
	c.clerk = base
	return c
}

// WithHTTP swaps the transport, used by tests against loopback servers.
func (c *Client) WithHTTP(client *http.Client) *Client {
	c.http = client
	return c
}

// Chat streams one completion, calling emit for every decoded event.
func (c *Client) Chat(ctx context.Context, cred Credential, req Request, emit func(Event) error) error {
	if len(req.Messages) == 0 {
		return errors.New("no messages to send")
	}
	if req.Images() > 0 {
		return c.ChatImages(ctx, cred, req, emit)
	}
	// One generation per account: the web app rejects parallel turns.
	release, err := c.lock(ctx, cred)
	if err != nil {
		return err
	}
	defer release()

	userID, err := c.user(ctx, cred)
	if err != nil {
		return err
	}
	body, err := json.Marshal(venicePayload(cred, req, userID))
	if err != nil {
		return err
	}
	for attempt := 1; ; attempt++ {
		attestation, err := c.attestation(ctx, cred, userID)
		if err != nil {
			return err
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/inference/chat", strings.NewReader(string(body)))
		if err != nil {
			return err
		}
		c.headers(httpReq, cred, attestation)
		httpReq.Header.Set("Accept", "text/event-stream")
		if cred.DistinctID != "" {
			httpReq.Header.Set("x-venice-distinct-id", cred.DistinctID)
		}
		if token := c.sessionToken(cred, userID); token != "" {
			httpReq.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := c.http.Do(httpReq)
		if err != nil {
			if attempt == 1 && ctx.Err() == nil {
				c.invalidateAttestation(userID)
				continue
			}
			return err
		}
		if response.StatusCode == http.StatusOK {
			delivered := false
			streamErr := decodeEvents(response.Body, func(event Event) error {
				delivered = true
				return emit(event)
			})
			response.Body.Close()
			// A stream that dies before the first event is worth one retry.
			if streamErr != nil && attempt == 1 && ctx.Err() == nil && !delivered {
				continue
			}
			return streamErr
		}
		status := response.StatusCode
		if status >= 500 && attempt == 1 {
			io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			response.Body.Close()
			continue
		}
		if status == http.StatusUnauthorized && userID == "anonymous" {
			io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			response.Body.Close()
			return ErrSignInRequired
		}
		// A spent or raced attestation token also answers 403. Mint a new one
		// once before blaming the clearance.
		if status == http.StatusForbidden && attempt == 1 {
			io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			response.Body.Close()
			c.invalidateAttestation(userID)
			continue
		}
		defer response.Body.Close()
		return c.statusError(response)
	}
}

// invalidateAttestation drops cached attestation tokens for a user.
func (c *Client) invalidateAttestation(userID string) {
	c.mu.Lock()
	delete(c.attesting, userID)
	c.mu.Unlock()
}

// lock serialises generations for one credential.
func (c *Client) lock(ctx context.Context, cred Credential) (func(), error) {
	key := credentialKey(cred)
	c.mu.Lock()
	guard, ok := c.locks[key]
	if !ok {
		guard = make(chan struct{}, 1)
		c.locks[key] = guard
	}
	c.mu.Unlock()
	select {
	case guard <- struct{}{}:
		return func() { <-guard }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Images reports how many attachments the request carries.
func (r Request) Images() int {
	total := 0
	for _, message := range r.Messages {
		total += len(message.Images)
	}
	return total
}

func venicePayload(cred Credential, req Request, userID string) map[string]any {
	prompt := make([]Message, len(req.Messages))
	copy(prompt, req.Messages)
	payload := map[string]any{
		"modelId":                   req.Model,
		"prompt":                    prompt,
		"reasoning":                 req.Reasoning,
		"temperature":               0.7,
		"topP":                      0.9,
		"requestId":                 requestID(cred, req),
		"userId":                    userID,
		"simpleMode":                false,
		"includeVeniceSystemPrompt": false,
		"webEnabled":                false,
		"webScrapeEnabled":          false,
		"xSearchEnabled":            false,
		"conversationType":          "text",
		"enableLargeContextChat":    false,
		"isCharacter":               false,
		"clientProcessingTime":      0,
		"stripThinkingResponse":     true,
		"includeExternalWebResults": false,
		"includeVeniceReasoning":    req.Reasoning,
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		payload["topP"] = *req.TopP
	}
	if req.MaxTokens > 0 {
		payload["maxTokens"] = req.MaxTokens
	}
	return payload
}

func requestID(cred Credential, req Request) string {
	sum := sha256.Sum256([]byte(cred.DistinctID + "\x00" + req.Model + "\x00" + fmt.Sprint(len(req.Messages))))
	return hex.EncodeToString(sum[:])[:7]
}

func decodeEvents(body io.Reader, emit func(Event) error) error {
	decoder := json.NewDecoder(body)
	for {
		var frame struct {
			Kind             string `json:"kind"`
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ServingModel     string `json:"servingModelId"`
			Error            any    `json:"error"`
			Type             string `json:"type"`
			CompletionID     string `json:"completion_id"`
		}
		if err := decoder.Decode(&frame); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if frame.Kind == "" && frame.Content == "" && frame.ReasoningContent == "" {
			continue
		}
		event := Event{Kind: frame.Kind, ServingModel: frame.ServingModel}
		switch {
		case frame.Content != "":
			event.Kind, event.Content = "content", frame.Content
		case frame.ReasoningContent != "":
			event.Kind, event.Content = "reasoning", frame.ReasoningContent
		case frame.Kind == "meta" || frame.Kind == "done":
			event.Kind = "meta"
		default:
			event.Kind = frame.Kind
		}
		if err := emit(event); err != nil {
			return err
		}
	}
}

func (c *Client) statusError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	text := string(body)
	switch {
	case response.StatusCode == http.StatusTooManyRequests:
		return rateLimitError{until: RateLimitUntil(response, time.Now())}
	case response.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("%w: %s", ErrUnauthorized, firstLine(text))
	case response.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: %s", ErrCloudflare, firstLine(text))
	case strings.Contains(text, "userPromptTooLong"):
		return errContextTooLong
	case (response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusNotFound) &&
		strings.Contains(strings.ToLower(text), "model"):
		return fmt.Errorf("%w: %s", errUnknownModel, firstLine(text))
	default:
		return fmt.Errorf("venice returned HTTP %d: %s", response.StatusCode, firstLine(text))
	}
}

var (
	errContextTooLong = errors.New("prompt exceeds the model context")
	errUnknownModel   = errors.New("the web app does not offer this model")
)

// ErrContextTooLong reports that the model context window was exceeded.
func ErrContextTooLong() error { return errContextTooLong }

// ErrUnknownModel reports that the model ID is not offered to this session.
func ErrUnknownModel() error { return errUnknownModel }

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}

func (c *Client) headers(req *http.Request, cred Credential, attestation string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", cred.UserAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Origin", veniceApp)
	req.Header.Set("Referer", veniceApp+"/")
	req.Header.Set("x-venice-version", veniceInterface)
	req.Header.Set("x-venice-middleface-version", veniceMiddleface)
	req.Header.Set("x-venice-request-timestamp-ms", strconv.FormatInt(c.now().UnixMilli(), 10))
	req.Header.Set("x-venice-locale", "en")
	req.Header.Set("x-venice-client-attestation", attestation)
	if clearance := cred.cookie("cf_clearance"); clearance != "" {
		req.AddCookie(&http.Cookie{Name: "cf_clearance", Value: clearance})
	}
}

// user resolves the account subject from the Clerk session token. A guest
// `__client` cookie has no session, so minting failure falls back to anonymous.
func (c *Client) user(ctx context.Context, cred Credential) (string, error) {
	if cred.clerkCookie("__client") == "" {
		return "anonymous", nil
	}
	token, err := c.clerkToken(ctx, cred)
	if err != nil {
		return "anonymous", nil
	}
	if sub := jwtSubject(token); sub != "" {
		return sub, nil
	}
	return "anonymous", nil
}

func (c *Client) sessionToken(cred Credential, userID string) string {
	if userID == "anonymous" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.tokens[credentialKey(cred)]
	if entry.value != "" && c.now().Before(entry.expires) {
		return entry.value
	}
	return ""
}

func (c *Client) clientAttestation(ctx context.Context, cred Credential) ([]cached, error) {
	body := `{"count":5,"env":{"nativeIntegrityOk":true,"reactStateHash":"69669e0b72f10994f29c772e09e4ac115e4976bef4d692695f3cec4b95bb930b","sentryVersion":"10.50.0","unexpectedGlobalsCount":677,"webdriver":false}}`
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/app/user/client_attestation/mint", strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.headers(request, cred, "")
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, c.statusError(response)
	}
	var minted struct {
		ExpiresAt int64 `json:"expiresAt"`
		Tokens    []struct {
			Token string `json:"token"`
		} `json:"tokens"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&minted); err != nil {
		return nil, err
	}
	if len(minted.Tokens) == 0 {
		return nil, errors.New("attestation response carried no token")
	}
	tokens := make([]cached, 0, len(minted.Tokens))
	for _, minted := range minted.Tokens {
		expiry := c.now().Add(4 * time.Minute)
		if exp := jwtExpiry(minted.Token); !exp.IsZero() {
			expiry = exp
		}
		tokens = append(tokens, cached{value: minted.Token, expires: expiry})
	}
	return tokens, nil
}

// attestation pops a pooled attestation token. Minted tokens are single-use.
func (c *Client) attestation(ctx context.Context, cred Credential, userID string) (string, error) {
	key := userID
	c.mu.Lock()
	pool := c.attesting[key]
	for len(pool) > 0 {
		entry := pool[0]
		pool = pool[1:]
		if c.now().Before(entry.expires) {
			c.attesting[key] = pool
			c.mu.Unlock()
			return entry.value, nil
		}
	}
	c.attesting[key] = pool
	c.mu.Unlock()
	tokens, err := c.clientAttestation(ctx, cred)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.attesting[key] = append(c.attesting[key], tokens[1:]...)
	c.mu.Unlock()
	return tokens[0].value, nil
}

// clerkToken mints a fresh session JWT from the durable Clerk client cookie.
func (c *Client) clerkToken(ctx context.Context, cred Credential) (string, error) {
	key := credentialKey(cred)
	c.mu.Lock()
	entry, ok := c.tokens[key]
	c.mu.Unlock()
	if ok && c.now().Before(entry.expires) {
		return entry.value, nil
	}
	cookie := "__client=" + cred.clerkCookie("__client")
	if bm := cred.clerkCookie("__cf_bm"); bm != "" {
		cookie += "; __cf_bm=" + bm
	}
	call := func(method, path string) ([]byte, error) {
		request, err := http.NewRequestWithContext(ctx, method, c.clerk+path, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Cookie", cookie)
		request.Header.Set("User-Agent", cred.UserAgent)
		request.Header.Set("Origin", veniceApp)
		request.Header.Set("Referer", veniceApp+"/")
		request.Header.Set("Accept", "application/json")
		response, err := c.http.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("clerk %s returned HTTP %d", path, response.StatusCode)
		}
		return body, nil
	}
	list, err := call(http.MethodGet, "/v1/client")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	var client struct {
		Response struct {
			Sessions []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"sessions"`
		} `json:"response"`
	}
	if err := json.Unmarshal(list, &client); err != nil {
		return "", err
	}
	sessionID := ""
	for _, session := range client.Response.Sessions {
		if session.Status == "active" {
			sessionID = session.ID
			break
		}
	}
	if sessionID == "" {
		return "", fmt.Errorf("%w: no active Clerk session", ErrUnauthorized)
	}
	minted, err := call(http.MethodPost, "/v1/client/sessions/"+sessionID+"/tokens")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	var token struct {
		JWT string `json:"jwt"`
	}
	if err := json.Unmarshal(minted, &token); err != nil || token.JWT == "" {
		return "", fmt.Errorf("%w: Clerk did not return a session token", ErrUnauthorized)
	}
	expiry := c.now().Add(50 * time.Second)
	if exp := jwtExpiry(token.JWT); !exp.IsZero() {
		expiry = exp
	}
	c.mu.Lock()
	c.tokens[key] = cached{value: token.JWT, expires: expiry}
	c.mu.Unlock()
	return token.JWT, nil
}

// Forget drops cached tokens so the next call re-mints them.
func (c *Client) Forget(cred Credential) {
	c.mu.Lock()
	delete(c.tokens, credentialKey(cred))
	c.mu.Unlock()
}

func credentialKey(cred Credential) string {
	sum := sha256.Sum256([]byte(cred.clerkCookie("__client") + "\x00" + cred.DistinctID))
	return hex.EncodeToString(sum[:8])
}

func jwtExpiry(token string) time.Time {
	payload := jwtPayload(token)
	if exp, ok := payload["exp"].(float64); ok && exp > 0 {
		return time.Unix(int64(exp), 0).Add(-10 * time.Second)
	}
	return time.Time{}
}

func jwtSubject(token string) string {
	if sub, ok := jwtPayload(token)["sub"].(string); ok {
		return sub
	}
	return ""
}

func jwtPayload(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		return nil
	}
	return payload
}

// Principal reports the session subject, or "anonymous" for a guest cookie.
func (c *Client) Principal(ctx context.Context, cred Credential) string {
	user, err := c.user(ctx, cred)
	if err != nil {
		return "anonymous"
	}
	return user
}

// Probe lists the remaining chat quota without spending a message.
func (c *Client) Probe(ctx context.Context, cred Credential) (map[string]any, error) {
	userID, err := c.user(ctx, cred)
	if err != nil {
		return nil, err
	}
	attestation, err := c.attestation(ctx, cred, userID)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/inference/rate-limits", nil)
	if err != nil {
		return nil, err
	}
	c.headers(request, cred, attestation)
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, c.statusError(response)
	}
	var limits map[string]any
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&limits); err != nil {
		return nil, err
	}
	return limits, nil
}
