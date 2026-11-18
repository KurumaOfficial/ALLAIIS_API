// SPDX-License-Identifier: GPL-3.0-only

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
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

type Provider struct {
	BaseURL string `json:"base_url"`
	KeyEnv  string `json:"key_env,omitempty"`
	session bool
}

func (p Provider) SendSession() bool { return p.session }

var defaults = map[string]Provider{
	"openai":      {BaseURL: "https://api.openai.com/v1", KeyEnv: "OPENAI_API_KEY"},
	"deepseek":    {BaseURL: "https://api.deepseek.com", KeyEnv: "DEEPSEEK_API_KEY"},
	"zai":         {BaseURL: "https://api.z.ai/api/paas/v4", KeyEnv: "ZAI_API_KEY"},
	"venice":      {BaseURL: "https://api.venice.ai/api/v1", KeyEnv: "VENICE_API_KEY"},
	"qwen":        {BaseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", KeyEnv: "DASHSCOPE_API_KEY"},
	"gemini":      {BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", KeyEnv: "GEMINI_API_KEY"},
	"opencode":    {BaseURL: "https://opencode.ai/inference/openai/v1", KeyEnv: "OPENCODE_API_KEY", session: true},
	"opencode-go": {BaseURL: "https://opencode.ai/inference/go/openai/v1", KeyEnv: "OPENCODE_API_KEY", session: true},
}

var (
	providerID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)
	modelID    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,159}$`)
	envName    = regexp.MustCompile(`^[A-Z_][A-Z_0-9]*$`)
)

type Route struct {
	ID            string  `json:"id"`
	Provider      string  `json:"provider"`
	UpstreamModel string  `json:"upstream_model"`
	Privacy       Privacy `json:"privacy,omitempty"`
}

type Config struct {
	Listen     string              `json:"listen,omitempty"`
	MinPrivacy Privacy             `json:"min_privacy,omitempty"`
	Providers  map[string]Provider `json:"providers,omitempty"`
	Models     []Route             `json:"models"`
}

func Default() Config {
	return Config{Listen: DefaultListen, MinPrivacy: Consumer, Models: []Route{}}
}

func (c Config) Provider(id string) (Provider, bool) {
	if p, ok := defaults[id]; ok {
		return p, true
	}
	p, ok := c.Providers[id]
	return p, ok
}

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer file.Close()

	const maxSize = 1 << 20
	info, err := file.Stat()
	if err != nil {
		return Config{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSize {
		return Config{}, errors.New("config must be a regular file of at most 1 MiB")
	}
	cfg := new(Config)
	*cfg = Default()
	decoder := json.NewDecoder(io.LimitReader(file, maxSize+1))
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
	baseURLs := make(map[string]string, len(defaults)+len(c.Providers))
	for id, p := range defaults {
		baseURLs[p.BaseURL] = id
	}
	for id, p := range c.Providers {
		if _, reserved := defaults[id]; reserved || !providerID.MatchString(id) {
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
		if route.Privacy == "" {
			route.Privacy = Consumer
		}
		if !route.Privacy.Valid() {
			return fmt.Errorf("model %q has an invalid privacy classification", route.ID)
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
