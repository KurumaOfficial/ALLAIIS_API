// SPDX-License-Identifier: GPL-3.0-only

// Package admin serves the loopback control panel: keys, aliases, discovery,
// probes, logs and settings. It never leaves 127.0.0.1.
package admin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/gateway"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

//go:embed static
var assets embed.FS

const tokenHeader = "X-ALLAIIS-Admin"

// Options wires the panel to a live gateway instance.
type Options struct {
	Gateway    *gateway.Gateway
	Store      *vault.Store
	ConfigPath string
	DataDir    string
	Passphrase []byte
	Version    string
	// SmokeEvery enables the scheduled probe sweep when non-zero.
	SmokeEvery time.Duration
	// SmokeWebhook receives failures and regressions.
	SmokeWebhook string
	// NoToolProbe leaves the parser probe out of manual and scheduled sweeps.
	NoToolProbe bool
}

type Server struct {
	options  Options
	token    string
	logs     *logHub
	catalogs *catalogCache
	health   *health
	mux      http.Handler
}

func New(options Options) (*Server, error) {
	if options.Gateway == nil || options.Store == nil {
		return nil, errors.New("admin needs a gateway and a vault")
	}
	server := &Server{options: options, token: randomToken(), logs: newLogHub(), catalogs: newCatalogCache(), health: newHealth()}
	server.mux = server.routes()
	return server, nil
}

// Logs exposes the shared log buffer for the slog tee.
func (s *Server) Logs() *logHub { return s.logs }

func randomToken() string {
	raw := make([]byte, 24)
	rand.Read(raw)
	return fmt.Sprintf("%x", raw)
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.page)
	mux.Handle("GET /static/", http.FileServer(http.FS(assets)))
	mux.HandleFunc("GET /api/state", s.guard(s.state))
	mux.HandleFunc("POST /api/keys", s.guard(s.addKey))
	mux.HandleFunc("POST /api/keys/delete", s.guard(s.removeKey))
	mux.HandleFunc("POST /api/keys/rotate", s.guard(s.rotateKey))
	mux.HandleFunc("POST /api/models", s.guard(s.addModel))
	mux.HandleFunc("POST /api/models/delete", s.guard(s.removeModel))
	mux.HandleFunc("POST /api/models/toggle", s.guard(s.toggleModel))
	mux.HandleFunc("POST /api/cooldown/clear", s.guard(s.clearCooldown))
	mux.HandleFunc("POST /api/settings", s.guard(s.saveSettings))
	mux.HandleFunc("POST /api/reload", s.guard(s.reloadConfig))
	mux.HandleFunc("GET /api/discover", s.guard(s.discover))
	mux.HandleFunc("POST /api/probe", s.guard(s.probe))
	mux.HandleFunc("POST /api/probe/tools", s.guard(s.probeTools))
	mux.HandleFunc("POST /api/e2ee/probe", s.guard(s.e2eeProbe))
	mux.HandleFunc("POST /api/smoke", s.guard(s.smoke))
	mux.HandleFunc("GET /api/smoke/history", s.guard(s.smokeHistory))
	mux.HandleFunc("GET /api/logs", s.guard(s.logsPage))
	mux.HandleFunc("POST /api/web/chrome", s.guard(s.openChrome))
	mux.HandleFunc("POST /api/import/opencode", s.guard(s.importOpenCode))
	mux.HandleFunc("POST /api/web/login", s.guard(s.webLogin))
	mux.HandleFunc("POST /api/web/recapture", s.guard(s.recapture))
	mux.HandleFunc("POST /api/web/quota", s.guard(s.webQuota))
	mux.HandleFunc("GET /api/export", s.guard(s.exportConfig))
	mux.HandleFunc("POST /api/doctor", s.guard(s.doctor))
	mux.HandleFunc("POST /api/tools/reset", s.guard(s.toolReset))
	return mux
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(tokenHeader)), []byte(s.token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "admin session token missing or stale; reload the page"})
			return
		}
		next(w, r)
	}
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	body, err := assets.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; "+
		"style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; "+
		"base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	// frame-ancestors covers current browsers; the header covers older ones.
	w.Header().Set("X-Frame-Options", "DENY")
	io.WriteString(w, strings.Replace(string(body), "{{TOKEN}}", s.token, 1))
}

// Listen serves the panel on address and reports the bound URL.
func (s *Server) Listen(ctx context.Context, address string, ready func(url string)) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	// A wildcard listen prints as 127.0.0.1 in the reported URL.
	bound := listener.Addr().String()
	if host, _, err := net.SplitHostPort(bound); err == nil && host == "" {
		bound = net.JoinHostPort("127.0.0.1", portOf(bound))
	}
	server := &http.Server{
		Handler: s, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 120 * time.Second,
		MaxHeaderBytes: 32 << 10,
		BaseContext:    func(net.Listener) context.Context { return ctx },
	}
	url := "http://" + bound + "/"
	if ready != nil {
		ready(url)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(listener) }()
	select {
	case err := <-stopped:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			server.Close()
			return err
		}
		return nil
	}
}

func portOf(address string) string {
	_, port, _ := net.SplitHostPort(address)
	return port
}

// OpenBrowser starts the platform opener without waiting for it.
func OpenBrowser(url string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		command = exec.Command("open", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	if err := command.Start(); err != nil {
		return err
	}
	go command.Wait()
	return nil
}

func (s *Server) reloadFile(ctx context.Context) error { return s.options.Gateway.Reload(ctx) }

func (s *Server) reloadConfig(w http.ResponseWriter, r *http.Request) {
	if err := s.reloadFile(r.Context()); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"reloaded": true})
}

func (s *Server) loadConfig() (config.Config, error) { return config.Load(s.options.ConfigPath) }

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func fail(w http.ResponseWriter, status int, text string) {
	writeJSON(w, status, map[string]string{"error": text})
}

// decode reads a small JSON body into value.
func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		fail(w, http.StatusBadRequest, "could not read the request body")
		return false
	}
	if err := json.Unmarshal(body, value); err != nil {
		fail(w, http.StatusBadRequest, "expected a JSON object")
		return false
	}
	return true
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.snapshot(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) logsPage(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	lines, last := s.logs.since(since)
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines, "last": last})
}

func (s *Server) addKey(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Provider string `json:"provider"`
		Label    string `json:"label"`
		Key      string `json:"key"`
	}
	if !decode(w, r, &input) {
		return
	}
	cfg, err := s.loadConfig()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	provider, ok := cfg.Provider(input.Provider)
	if !ok {
		fail(w, http.StatusBadRequest, "unknown provider")
		return
	}
	if !config.ValidCredentialLabel(input.Label) {
		fail(w, http.StatusBadRequest, "label must start with a letter or digit and use letters, digits, dot, dash or underscore")
		return
	}
	key := strings.TrimSpace(input.Key)
	if key == "" {
		fail(w, http.StatusBadRequest, "the key is empty")
		return
	}
	label := input.Label
	if provider.Web() {
		if !validWebCredential(key) {
			fail(w, http.StatusBadRequest, "a web session must be the JSON captured by `allaiis web login`")
			return
		}
	}
	count, err := s.options.Store.Import(r.Context(), []vault.Entry{{
		Provider: input.Provider, Endpoint: provider.BaseURL, Label: label, Key: key,
	}})
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.catalogs.dropProvider(input.Provider)
	if err := s.options.Gateway.ResetCredential(r.Context(), provider, input.Provider, label, key); err != nil {
		fail(w, http.StatusInternalServerError, "key saved but the state reset failed: "+err.Error())
		return
	}
	if err := s.reloadFile(r.Context()); err != nil {
		fail(w, http.StatusBadRequest, "key saved but reloading failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved": count, "label": label})
}

func validWebCredential(key string) bool {
	var probe map[string]json.RawMessage
	if json.Unmarshal([]byte(key), &probe) != nil {
		return false
	}
	_, hasVenice := probe["venice"]
	_, hasDistinct := probe["distinct_id"]
	return hasVenice || hasDistinct
}

func (s *Server) removeKey(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Provider string `json:"provider"`
		Label    string `json:"label"`
	}
	if !decode(w, r, &input) {
		return
	}
	cfg, err := s.loadConfig()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	provider, ok := cfg.Provider(input.Provider)
	if !ok {
		fail(w, http.StatusBadRequest, "unknown provider")
		return
	}
	removed, err := s.options.Store.Remove(r.Context(), input.Provider, provider.BaseURL, input.Label)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !removed {
		fail(w, http.StatusNotFound, "that label is not stored")
		return
	}
	s.catalogs.dropProvider(input.Provider)
	if err := s.options.Gateway.ResetCredential(r.Context(), provider, input.Provider, input.Label, ""); err != nil {
		fail(w, http.StatusInternalServerError, "key removed but the state reset failed: "+err.Error())
		return
	}
	if err := s.reloadFile(r.Context()); err != nil {
		fail(w, http.StatusBadRequest, "key removed but reloading failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"removed": true})
}

func (s *Server) rotateKey(w http.ResponseWriter, r *http.Request) {
	key, err := s.options.Store.RotateClientKey(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.options.Gateway.SetClientKey(key)
	writeJSON(w, http.StatusOK, map[string]string{"key": key})
}

func (s *Server) addModel(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Provider      string          `json:"provider"`
		UpstreamModel string          `json:"upstream_model"`
		Name          string          `json:"name"`
		Protocol      config.Protocol `json:"protocol"`
		Privacy       config.Privacy  `json:"privacy"`
		Label         string          `json:"label"`
		Reasoning     *bool           `json:"reasoning"`
		MaxContext    int             `json:"max_context"`
		MaxResponse   int             `json:"max_response"`
		MaxImages     int             `json:"max_images"`
		MaxTools      int             `json:"max_tools"`
		WebSearch     bool            `json:"web_search"`
		LargeContext  bool            `json:"large_context"`
		StrictOrder   bool            `json:"strict_order"`
		InactivityMS  int             `json:"inactivity_ms"`
		NoSampling    bool            `json:"no_sampling"`
		Frequency     bool            `json:"frequency_penalty"`
		Presence      bool            `json:"presence_penalty"`
		Replacement   string          `json:"replacement"`
		RoundTrip     bool            `json:"reasoning_round_trip"`
		MaxVideos     int             `json:"max_videos"`
		MinP          bool            `json:"min_p"`
		AnonUserID    *bool           `json:"anon_user_id"`
		Temperature   *float64        `json:"temperature"`
		TopP          *float64        `json:"top_p"`
		E2EE          bool            `json:"e2ee"`
	}
	if !decode(w, r, &input) {
		return
	}
	cfg, err := s.loadConfig()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	provider, ok := cfg.Provider(input.Provider)
	if !ok {
		fail(w, http.StatusBadRequest, "unknown provider")
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = config.DefaultAliasFor(input.Provider, input.UpstreamModel,
			provider.Transport == config.TransportVeniceWeb)
	}
	route := config.Route{
		ID: name, Provider: input.Provider, UpstreamModel: strings.TrimSpace(input.UpstreamModel),
		Protocol: input.Protocol, Privacy: input.Privacy, CredentialLabel: input.Label,
		Reasoning: input.Reasoning, MaxContext: input.MaxContext, MaxResponse: input.MaxResponse,
		MaxImages: input.MaxImages, MaxTools: input.MaxTools, WebSearch: input.WebSearch,
		LargeContext: input.LargeContext, StrictOrder: input.StrictOrder, InactivityMS: input.InactivityMS,
		NoSampling: input.NoSampling, Frequency: input.Frequency, Presence: input.Presence,
		Replacement: input.Replacement, ReasoningRoundTrip: input.RoundTrip, MaxVideos: input.MaxVideos,
		MinP: input.MinP, AnonUserID: input.AnonUserID, E2EE: input.E2EE,
		Temperature: input.Temperature, TopP: input.TopP,
	}
	added, err := config.AddModel(r.Context(), s.options.ConfigPath, route)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.reloadFile(r.Context()); err != nil {
		fail(w, http.StatusBadRequest, "alias saved but reloading failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "id": route.ID})
}

func (s *Server) removeModel(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &input) {
		return
	}
	removed, err := config.RemoveModel(r.Context(), s.options.ConfigPath, input.ID)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.reloadFile(r.Context()); err != nil {
		fail(w, http.StatusBadRequest, "alias removed but reloading failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"removed": removed})
}

func (s *Server) toggleModel(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID       string `json:"id"`
		Disabled bool   `json:"disabled"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := config.SetModelDisabled(r.Context(), s.options.ConfigPath, input.ID, input.Disabled); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.reloadFile(r.Context()); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": input.ID, "disabled": input.Disabled})
}

// clearCooldown lifts a cooldown so a recovered session can answer at once.
func (s *Server) clearCooldown(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Provider string `json:"provider"`
		Label    string `json:"label"`
	}
	if !decode(w, r, &input) {
		return
	}
	cfg, err := s.loadConfig()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	provider, ok := cfg.Provider(input.Provider)
	if !ok {
		fail(w, http.StatusBadRequest, "unknown provider")
		return
	}
	if input.Label != "" && !config.ValidCredentialLabel(input.Label) {
		fail(w, http.StatusBadRequest, "invalid credential label")
		return
	}
	if err := s.options.Gateway.ClearCooldown(r.Context(), provider, input.Provider, input.Label); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cleared": true})
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		MinPrivacy config.Privacy `json:"min_privacy"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !input.MinPrivacy.Valid() {
		fail(w, http.StatusBadRequest, "invalid privacy level")
		return
	}
	if err := config.SetMinPrivacy(r.Context(), s.options.ConfigPath, input.MinPrivacy); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.reloadFile(r.Context()); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"min_privacy": string(input.MinPrivacy)})
}

func (s *Server) exportConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadConfig()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	providers := cfg.Providers
	if providers == nil {
		// Built-in providers need no entry in the file, but an export that
		// answers null is not the object a reader expects.
		providers = map[string]config.Provider{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"listen": cfg.Listen, "min_privacy": cfg.MinPrivacy,
		"providers": providers, "models": cfg.Models,
	})
}

func (s *Server) openChrome(w http.ResponseWriter, r *http.Request) {
	out, err := s.execAllaiis(r, 90*time.Second, "web", "chrome")
	if err != nil {
		fail(w, http.StatusBadGateway, strings.TrimSpace(out+" "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": strings.TrimSpace(out)})
}

// importOpenCode brings the API keys OpenCode already stores into the vault.
func (s *Server) importOpenCode(w http.ResponseWriter, r *http.Request) {
	out, err := s.execAllaiis(r, 2*time.Minute, "import", "--from", "opencode", "--all")
	if err != nil {
		fail(w, http.StatusBadGateway, strings.TrimSpace(out+" "+err.Error()))
		return
	}
	if err := s.reloadFile(r.Context()); err != nil {
		fail(w, http.StatusInternalServerError, "keys imported but reloading failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": strings.TrimSpace(out)})
}

func (s *Server) webLogin(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Provider string `json:"provider"`
		Label    string `json:"label"`
	}
	if !decode(w, r, &input) {
		return
	}
	out, err := s.execAllaiis(r, 5*time.Minute, "web", "login", "--provider", input.Provider, "--label", input.Label)
	if err != nil {
		fail(w, http.StatusBadGateway, strings.TrimSpace(out+" "+err.Error()))
		return
	}
	if err := s.resetLabel(r.Context(), input.Provider, input.Label); err != nil {
		fail(w, http.StatusInternalServerError, "session captured but the state reset failed: "+err.Error())
		return
	}
	if err := s.reloadFile(r.Context()); err != nil {
		fail(w, http.StatusBadRequest, "session captured but reloading failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": strings.TrimSpace(out)})
}

// e2eeProbe attests one model enclave through a stored label. An attestation
// spends no quota, so this works on a spent session.
func (s *Server) e2eeProbe(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Provider string `json:"provider"`
		Label    string `json:"label"`
		Model    string `json:"model"`
	}
	if !decode(w, r, &input) {
		return
	}
	cfg, err := s.loadConfig()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	provider, ok := cfg.Provider(input.Provider)
	if !ok {
		fail(w, http.StatusBadRequest, "unknown provider")
		return
	}
	entries, err := s.options.Store.Entries(r.Context(), input.Provider, provider.BaseURL)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	var credential webprov.Credential
	var parseErr error
	found := false
	for _, entry := range entries {
		if entry.Label == input.Label {
			credential, parseErr = webprov.ParseCredential(entry.Key)
			found = true
			break
		}
	}
	if !found {
		fail(w, http.StatusNotFound, "that label is not stored for this provider")
		return
	}
	if parseErr != nil {
		fail(w, http.StatusBadRequest, "the stored session is unreadable: "+parseErr.Error())
		return
	}
	client := webprov.New(15 * time.Second)
	if strings.HasPrefix(provider.BaseURL, "http://") {
		client = client.WithHTTP(http.DefaultClient)
	}
	probe, err := client.ProbeE2EE(r.Context(), credential, input.Model)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attestation": probe})
}

// resetLabel drops cached session state and cooldowns for a fresh capture.
func (s *Server) resetLabel(ctx context.Context, providerID, label string) error {
	cfg, err := s.loadConfig()
	if err != nil {
		return err
	}
	provider, ok := cfg.Provider(providerID)
	if !ok {
		return nil
	}
	entries, err := s.options.Store.Entries(context.WithoutCancel(ctx), providerID, provider.BaseURL)
	if err != nil {
		return err
	}
	key := ""
	for _, entry := range entries {
		if entry.Label == label {
			key = entry.Key
		}
	}
	s.catalogs.dropProvider(providerID)
	return s.options.Gateway.ResetCredential(context.WithoutCancel(ctx), provider, providerID, label, key)
}

// StartSmoke begins the scheduled sweep.
func (s *Server) StartSmoke(ctx context.Context, every time.Duration, webhook string) {
	s.startSmoke(ctx, every, webhook)
}

func (s *Server) smokeHistory(w http.ResponseWriter, r *http.Request) {
	last, recent, updated, running := s.health.snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"last": last, "recent": recent, "updated": updated, "running": running,
	})
}

// webQuota probes the remaining chat quota of every stored session.
func (s *Server) webQuota(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Provider string `json:"provider"`
	}
	if !decode(w, r, &input) {
		return
	}
	cfg, err := s.loadConfig()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	provider, ok := cfg.Provider(input.Provider)
	if !ok || !provider.Web() {
		fail(w, http.StatusBadRequest, "unknown web provider")
		return
	}
	entries, err := s.options.Store.Entries(r.Context(), input.Provider, provider.BaseURL)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	client := webprov.New(45 * time.Second)
	rows := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		row := map[string]any{"label": entry.Label}
		credential, err := webprov.ParseCredential(entry.Key)
		if err != nil {
			row["error"] = "unreadable session"
			rows = append(rows, row)
			continue
		}
		limits, err := probeQuota(r.Context(), client, credential)
		if err != nil {
			row["error"] = err.Error()
			rows = append(rows, row)
			continue
		}
		row["principal"] = client.Principal(r.Context(), credential)
		if account, ok := limits["account"].(map[string]any); ok {
			row["plan"] = account["userType"]
			row["credits"] = account["availableCredits"]
			if usage, ok := account["bundledCreditsUsage"].(map[string]any); ok {
				row["credit_cap"] = usage["monthlyRefillCredits"]
				row["credit_refill_at"] = usage["nextRefillAt"]
			}
			delete(limits, "account")
		}
		if chat, ok := limits["chat"].(map[string]any); ok {
			row["chat_remaining"] = chat["remaining"]
			row["reset_at"] = chat["resetAt"]
			// A warning belongs in the log too: the panel is not always open.
			floor := cfg.QuotaFloor
			if floor == 0 {
				floor = config.DefaultQuotaFloor
			}
			if remaining, ok := chat["remaining"].(float64); ok && floor > 0 && remaining <= float64(floor) {
				row["low"] = true
				slog.Warn("quota below the floor", "provider", input.Provider, "label", entry.Label,
					"remaining", int(remaining), "floor", floor)
			}
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": rows})
}

// probeQuota is the network seam the panel's quota view uses.
var probeQuota = func(ctx context.Context, client *webprov.Client, cred webprov.Credential) (map[string]any, error) {
	return client.Probe(ctx, cred)
}

// recapture opens the capture browser and refreshes a session in one step.
func (s *Server) recapture(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Provider string `json:"provider"`
		Label    string `json:"label"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !config.ValidCredentialLabel(input.Label) {
		fail(w, http.StatusBadRequest, "give a valid credential label")
		return
	}
	var steps []string
	if !chromeReachable() {
		out, err := s.execAllaiis(r, 90*time.Second, "web", "chrome")
		steps = append(steps, out)
		if err != nil {
			fail(w, http.StatusBadGateway, strings.TrimSpace(strings.Join(steps, "\n")+"\n"+err.Error()))
			return
		}
		deadline := time.Now().Add(30 * time.Second)
		for !chromeReachable() && time.Now().Before(deadline) {
			time.Sleep(300 * time.Millisecond)
		}
		if !chromeReachable() {
			fail(w, http.StatusBadGateway, "the capture browser did not open its DevTools port")
			return
		}
	}
	out, err := s.execAllaiis(r, 5*time.Minute, "web", "login", "--provider", input.Provider, "--label", input.Label)
	steps = append(steps, out)
	if err != nil {
		fail(w, http.StatusBadGateway, strings.TrimSpace(strings.Join(steps, "\n")))
		return
	}
	if err := s.resetLabel(r.Context(), input.Provider, input.Label); err != nil {
		fail(w, http.StatusInternalServerError, "session captured but the state reset failed: "+err.Error())
		return
	}
	if err := s.reloadFile(r.Context()); err != nil {
		fail(w, http.StatusBadRequest, "session captured but reloading failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": strings.TrimSpace(strings.Join(steps, "\n"))})
}

func (s *Server) execAllaiis(r *http.Request, timeout time.Duration, args ...string) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = append(os.Environ(),
		"ALLAIIS_DATA_DIR="+s.options.DataDir,
		"ALLAIIS_VAULT_PASSPHRASE="+string(s.options.Passphrase))
	output, err := command.CombinedOutput()
	text := string(output)
	if len(text) > 2200 {
		text = text[len(text)-2200:]
	}
	if ctx.Err() != nil {
		return text, errors.New("timed out")
	}
	return text, err
}
