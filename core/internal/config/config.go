// SPDX-License-Identifier: GPL-3.0-only

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const DefaultListen = "127.0.0.1:8420"

type Privacy string

const (
	Public   Privacy = "public_research"
	Consumer Privacy = "consumer_cloud"
	ZDR      Privacy = "zdr"
	E2EE     Privacy = "e2ee"
)

var privacyRank = map[Privacy]int{Public: 1, Consumer: 2, ZDR: 3, E2EE: 4}

func (p Privacy) Valid() bool { return privacyRank[p] > 0 }

func (p Privacy) Meets(minimum Privacy) bool {
	return privacyRank[p] >= privacyRank[minimum]
}

type Protocol string

const (
	Chat      Protocol = ""
	Responses Protocol = "responses"
	Messages  Protocol = "messages"
)

type Provider struct {
	BaseURL       string        `json:"base_url"`
	KeyEnv        string        `json:"key_env,omitempty"`
	CatalogFormat CatalogFormat `json:"catalog_format,omitempty"`
	CatalogPublic bool          `json:"catalog_public,omitempty"`
	Transport     Transport     `json:"transport,omitempty"`
	session       bool
	catalogURL    string
	customizable  bool
	native        Protocol
}

func (p Provider) SendSession() bool { return p.session }

// Transport selects how a provider route is executed.
type Transport string

const (
	// TransportAPI forwards the request body to an OpenAI-shaped JSON endpoint.
	TransportAPI Transport = ""
	// TransportVeniceWeb drives the Venice web inference API with browser cookies.
	TransportVeniceWeb Transport = "venice_web"
)

// Web reports whether the provider is served by a browser web transport.
func (p Provider) Web() bool { return p.Transport != TransportAPI }

type CatalogFormat string

const (
	OpenAICatalog    CatalogFormat = ""
	ClaudeCatalog    CatalogFormat = "anthropic"
	GeminiCatalog    CatalogFormat = "gemini"
	DashScopeCatalog CatalogFormat = "dashscope"
)

func (p Provider) CatalogURL() string {
	if p.catalogURL != "" {
		return p.catalogURL
	}
	if p.CatalogFormat == DashScopeCatalog {
		return strings.TrimSuffix(p.BaseURL, "/compatible-mode/v1") + "/api/v1/models"
	}
	return p.BaseURL + "/models"
}

var defaults = map[string]Provider{
	"openai":   {BaseURL: "https://api.openai.com/v1", KeyEnv: "OPENAI_API_KEY", native: Responses},
	"deepseek": {BaseURL: "https://api.deepseek.com", KeyEnv: "DEEPSEEK_API_KEY"},
	"zai":      {BaseURL: "https://api.z.ai/api/paas/v4", KeyEnv: "ZAI_API_KEY"},
	"anthropic": {
		BaseURL: "https://api.anthropic.com/v1", KeyEnv: "ANTHROPIC_API_KEY",
		CatalogFormat: ClaudeCatalog, customizable: true, native: Messages,
	},
	"arena-ai": {BaseURL: "https://api.arena.ai/v1", KeyEnv: "ARENA_API_KEY", customizable: true},
	"venice": {
		BaseURL: "https://api.venice.ai/api/v1", KeyEnv: "VENICE_API_KEY", CatalogPublic: true,
		catalogURL: "https://api.venice.ai/api/v1/models?type=all",
	},
	"venice-web": {
		BaseURL: "https://outerface.venice.ai/api", KeyEnv: "VENICE_WEB_COOKIE",
		Transport: TransportVeniceWeb, catalogURL: "https://outerface.venice.ai/api/app/models?matureFilter=true&onlySafeVenice=true",
	},
	"qwen": {
		BaseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", KeyEnv: "DASHSCOPE_API_KEY",
		CatalogFormat: DashScopeCatalog,
	},
	"gemini": {
		BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", KeyEnv: "GEMINI_API_KEY",
		CatalogFormat: GeminiCatalog, catalogURL: "https://generativelanguage.googleapis.com/v1beta/models",
	},
	"opencode": {
		BaseURL: "https://opencode.ai/inference/openai/v1", KeyEnv: "OPENCODE_API_KEY",
		session: true, CatalogPublic: true, catalogURL: "https://opencode.ai/inference/v1/models",
	},
	"opencode-go": {
		BaseURL: "https://opencode.ai/inference/go/openai/v1", KeyEnv: "OPENCODE_API_KEY",
		session: true, CatalogPublic: true, catalogURL: "https://opencode.ai/inference/go/v1/models",
	},
}

var (
	providerID      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)
	modelID         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,159}$`)
	envName         = regexp.MustCompile(`^[A-Z_][A-Z_0-9]*$`)
	credentialLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`)
)

const maxConfigBytes = 1 << 20

type Route struct {
	ID              string   `json:"id"`
	Provider        string   `json:"provider"`
	UpstreamModel   string   `json:"upstream_model"`
	Privacy         Privacy  `json:"privacy,omitempty"`
	CredentialLabel string   `json:"credential_label,omitempty"`
	Protocol        Protocol `json:"protocol,omitempty"`
	// Disabled keeps the alias configured but out of `/v1/models` and routing.
	Disabled bool `json:"disabled,omitempty"`
	// Capabilities come from catalog discovery and shape the upstream request.
	Reasoning          *bool    `json:"reasoning,omitempty"`
	MaxContext         int      `json:"max_context,omitempty"`
	MaxResponse        int      `json:"max_response,omitempty"`
	MaxImages          int      `json:"max_images,omitempty"`
	MaxTools           int      `json:"max_tools,omitempty"`
	WebSearch          bool     `json:"web_search,omitempty"`
	LargeContext       bool     `json:"large_context,omitempty"`
	StrictOrder        bool     `json:"strict_order,omitempty"`
	InactivityMS       int      `json:"inactivity_ms,omitempty"`
	NoSampling         bool     `json:"no_sampling,omitempty"`
	Frequency          bool     `json:"frequency_penalty,omitempty"`
	Presence           bool     `json:"presence_penalty,omitempty"`
	Replacement        string   `json:"replacement,omitempty"`
	ReasoningRoundTrip bool     `json:"reasoning_round_trip,omitempty"`
	MaxVideos          int      `json:"max_videos,omitempty"`
	MinP               bool     `json:"min_p,omitempty"`
	AnonUserID         *bool    `json:"anon_user_id,omitempty"`
	Temperature        *float64 `json:"temperature,omitempty"`
	TopP               *float64 `json:"top_p,omitempty"`
}

func ValidCredentialLabel(label string) bool { return credentialLabel.MatchString(label) }

type Config struct {
	Listen     string              `json:"listen,omitempty"`
	MinPrivacy Privacy             `json:"min_privacy,omitempty"`
	Providers  map[string]Provider `json:"providers,omitempty"`
	Models     []Route             `json:"models"`
	// ToolNameTTLDays is how long an unknown tool name is remembered for the
	// doctor; zero keeps it forever.
	ToolNameTTLDays int `json:"tool_name_ttl_days,omitempty"`
}

// DefaultToolNameTTLDays is the retention applied when a config names none.
const DefaultToolNameTTLDays = 30

func Default() Config {
	return Config{Listen: DefaultListen, MinPrivacy: Consumer, Models: []Route{}, ToolNameTTLDays: DefaultToolNameTTLDays}
}

func (c Config) Provider(id string) (Provider, bool) {
	if p, ok := c.Providers[id]; ok {
		return p, true
	}
	p, ok := defaults[id]
	if p.customizable {
		for _, custom := range c.Providers {
			if custom.BaseURL == p.BaseURL {
				return Provider{}, false
			}
		}
	}
	return p, ok
}

func (c Config) ProviderIDs() []string {
	providers := maps.Clone(defaults)
	maps.Copy(providers, c.Providers)
	return slices.DeleteFunc(slices.Sorted(maps.Keys(providers)), func(id string) bool {
		_, available := c.Provider(id)
		return !available
	})
}

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return Config{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxConfigBytes {
		return Config{}, errors.New("config must be a regular file of at most 1 MiB")
	}
	cfg := new(Config)
	*cfg = Default()
	decoder := json.NewDecoder(io.LimitReader(file, maxConfigBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, err
	}
	if cfg == nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
		return Config{}, errors.New("expected exactly one config object")
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return *cfg, nil
}

// Validate checks the whole configuration before it is used.
func (c *Config) Validate() error { return c.validate() }

func (c *Config) validate() error {
	host, port, err := net.SplitHostPort(c.Listen)
	ip := net.ParseIP(host)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || !ip.IsLoopback() || portErr != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("listen must use a numeric loopback address and a port from 1 to 65535")
	}
	if !c.MinPrivacy.Valid() {
		return errors.New("invalid min_privacy")
	}
	if c.ToolNameTTLDays < 0 || c.ToolNameTTLDays > 3650 {
		return errors.New("tool_name_ttl_days must be from 0 to 3650")
	}
	baseURLs := make(map[string]string, len(defaults)+len(c.Providers))
	for id, p := range defaults {
		if !p.customizable {
			baseURLs[p.BaseURL] = id
		}
	}
	for id, p := range c.Providers {
		preset, reserved := defaults[id]
		if (reserved && !preset.customizable) || !providerID.MatchString(id) {
			return fmt.Errorf("invalid or reserved provider ID %q", id)
		}
		baseURL, err := validateBaseURL(p.BaseURL)
		if err != nil {
			return fmt.Errorf("provider %q: %w", id, err)
		}
		if existing, ok := baseURLs[baseURL]; ok {
			return fmt.Errorf("providers %q and %q share a base URL", existing, id)
		}
		baseURLs[baseURL] = id
		p.BaseURL = baseURL
		if p.KeyEnv != "" && !envName.MatchString(p.KeyEnv) {
			return fmt.Errorf("provider %q has an invalid key_env", id)
		}
		switch p.Transport {
		case TransportAPI, TransportVeniceWeb:
		default:
			return fmt.Errorf("provider %q has unknown transport %q", id, p.Transport)
		}
		if p.CatalogFormat != OpenAICatalog && (p.CatalogFormat != DashScopeCatalog ||
			!strings.HasSuffix(p.BaseURL, "/compatible-mode/v1")) {
			return fmt.Errorf("provider %q: use catalog_format dashscope with a /compatible-mode/v1 base URL", id)
		}
		c.Providers[id] = p
	}
	seen := make(map[string]bool, len(c.Models))
	for i := range c.Models {
		route := &c.Models[i]
		if !modelID.MatchString(route.ID) || !modelID.MatchString(route.UpstreamModel) {
			return fmt.Errorf("model entry %d has an invalid ID or upstream_model", i+1)
		}
		if seen[route.ID] {
			return fmt.Errorf("duplicate model ID %q", route.ID)
		}
		seen[route.ID] = true
		if _, ok := c.Provider(route.Provider); !ok {
			return fmt.Errorf("model %q has an unknown provider", route.ID)
		}
		if route.Protocol == "chat" {
			route.Protocol = Chat
		}
		switch route.Protocol {
		case Chat, Responses, Messages:
		default:
			return fmt.Errorf("model %q: protocol must be chat, responses, or messages", route.ID)
		}
		provider, _ := c.Provider(route.Provider)
		if _, custom := c.Providers[route.Provider]; !custom && !provider.Web() && route.Protocol != Chat &&
			defaults[route.Provider].native != route.Protocol {
			return fmt.Errorf("model %q: provider %q has no %s endpoint configured", route.ID, route.Provider, route.Protocol)
		}
		if route.Privacy == "" {
			route.Privacy = Consumer
		}
		if !route.Privacy.Valid() {
			return fmt.Errorf("model %q has an invalid privacy classification", route.ID)
		}
		if route.CredentialLabel != "" && !ValidCredentialLabel(route.CredentialLabel) {
			return fmt.Errorf("model %q has an invalid credential_label", route.ID)
		}
		if route.Protocol == Responses && route.CredentialLabel == "" {
			return fmt.Errorf("model %q: responses routes require a credential_label", route.ID)
		}
		if route.MaxContext < 0 || route.MaxContext > 10_000_000 ||
			route.MaxResponse < 0 || route.MaxResponse > 1_000_000 ||
			route.MaxImages < 0 || route.MaxImages > 50 ||
			route.MaxTools < 0 || route.MaxTools > 128 ||
			route.MaxVideos < 0 || route.MaxVideos > 20 ||
			route.InactivityMS != 0 && (route.InactivityMS < 1000 || route.InactivityMS > 600_000) {
			return fmt.Errorf("model %q has an out-of-range capability limit", route.ID)
		}
		if route.Temperature != nil && (*route.Temperature < 0 || *route.Temperature > 2) {
			return fmt.Errorf("model %q has an out-of-range temperature", route.ID)
		}
		if route.Replacement != "" && !modelID.MatchString(route.Replacement) {
			return fmt.Errorf("model %q has an invalid replacement ID", route.ID)
		}
		if route.TopP != nil && (*route.TopP < 0 || *route.TopP > 1) {
			return fmt.Errorf("model %q has an out-of-range top_p", route.ID)
		}
	}
	return nil
}

func validateBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" ||
		u.ForceQuery || u.Fragment != "" || u.RawPath != "" || strings.Contains(raw, "#") ||
		(u.Scheme != "https" && !(u.Scheme == "http" && net.ParseIP(u.Hostname()).IsLoopback())) {
		return "", errors.New("base_url must be HTTPS or numeric loopback HTTP, without credentials, query or fragment")
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}
