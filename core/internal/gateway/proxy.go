// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// ParseProxyList reads one proxy per line. A line is one of:
//
//	host:port
//	host:port:user:pass
//	scheme://[user:pass@]host:port
//
// Blank lines and lines starting with # are ignored. A missing scheme means
// http; https, socks5 and socks5h are accepted. IPv6 addresses need the URL
// form with brackets.
func ParseProxyList(text string) ([]*url.URL, error) {
	const maxProxies = 5000
	var proxies []*url.URL
	seen := map[string]bool{}
	for index, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		proxy, err := parseProxyLine(line)
		if err != nil {
			return nil, fmt.Errorf("proxy line %d: %w", index+1, err)
		}
		key := proxy.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		proxies = append(proxies, proxy)
		if len(proxies) > maxProxies {
			return nil, fmt.Errorf("more than %d proxies", maxProxies)
		}
	}
	return proxies, nil
}

func parseProxyLine(line string) (*url.URL, error) {
	if strings.Contains(line, "://") {
		proxy, err := url.Parse(line)
		if err != nil || proxy.Host == "" {
			return nil, errors.New("expected scheme://host:port")
		}
		switch proxy.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return nil, fmt.Errorf("scheme %q is not supported", proxy.Scheme)
		}
		if proxy.Path != "" && proxy.Path != "/" || proxy.RawQuery != "" || proxy.Fragment != "" {
			return nil, errors.New("a proxy URL carries no path, query or fragment")
		}
		if _, _, err := net.SplitHostPort(proxy.Host); err != nil {
			return nil, errors.New("the URL must carry host:port")
		}
		return proxy, nil
	}
	parts := strings.Split(line, ":")
	if len(parts) != 2 && len(parts) != 4 {
		return nil, errors.New("expected host:port or host:port:user:pass")
	}
	host, port := parts[0], parts[1]
	if host == "" || port == "" {
		return nil, errors.New("host and port are required")
	}
	if value, err := strconv.Atoi(port); err != nil || value < 1 || value > 65535 {
		return nil, errors.New("port must be 1-65535")
	}
	proxy := &url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}
	if len(parts) == 4 {
		if parts[2] == "" {
			return nil, errors.New("the user is empty")
		}
		proxy.User = url.UserPassword(parts[2], parts[3])
	}
	return proxy, nil
}

// proxyPool rotates a validated list, one proxy per outbound connection.
type proxyPool struct {
	mu   sync.RWMutex
	urls []*url.URL
	next atomic.Uint64
}

func (p *proxyPool) set(urls []*url.URL) {
	p.mu.Lock()
	p.urls = append([]*url.URL{}, urls...)
	p.mu.Unlock()
}

func (p *proxyPool) Proxy(*http.Request) (*url.URL, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.urls) == 0 {
		return nil, nil
	}
	index := p.next.Add(1) - 1
	return p.urls[index%uint64(len(p.urls))], nil
}

// SetProxyList parses a panel list and applies it to the API-key providers.
// Web-chat sessions do not share this transport.
func (g *Gateway) SetProxyList(text string) (int, error) {
	urls, err := ParseProxyList(text)
	if err != nil {
		return 0, err
	}
	if g.proxies != nil {
		g.proxies.set(urls)
	}
	if g.web != nil {
		g.web.SetProxies(urls)
	}
	return len(urls), nil
}
