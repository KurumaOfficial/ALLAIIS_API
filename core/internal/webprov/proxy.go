// SPDX-License-Identifier: GPL-3.0-only

package webprov

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	utls "github.com/refraction-networking/utls"
	xproxy "golang.org/x/net/proxy"
)

// proxySet rotates a validated pool; one proxy is picked per connection.
type proxySet struct {
	mu   sync.RWMutex
	urls []*url.URL
	next atomic.Uint64
}

func newProxySet() *proxySet { return &proxySet{} }

func (p *proxySet) set(urls []*url.URL) {
	p.mu.Lock()
	p.urls = append([]*url.URL{}, urls...)
	p.mu.Unlock()
}

func (p *proxySet) pick() *url.URL {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.urls) == 0 {
		return nil
	}
	index := p.next.Add(1) - 1
	return p.urls[index%uint64(len(p.urls))]
}

// SetProxies applies a pool to the web transport. The list is chosen per new
// HTTP/2 connection, so a blocked proxy is left behind on the next dial.
func (c *Client) SetProxies(urls []*url.URL) {
	if c.proxies == nil {
		c.proxies = newProxySet()
	}
	c.proxies.set(urls)
}

func (c *Client) dialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	var raw net.Conn
	var err error
	if proxy := c.proxies.pick(); proxy != nil {
		raw, err = dialProxy(ctx, proxy, addr)
	} else {
		raw, err = (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, addr)
	}
	if err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		raw.Close()
		return nil, err
	}
	raw.SetDeadline(time.Now().Add(15 * time.Second))
	conn := utls.UClient(raw, &utls.Config{
		ServerName: host, NextProtos: []string{"h2", "http/1.1"},
	}, utls.HelloChrome_Auto)
	if err := conn.Handshake(); err != nil {
		raw.Close()
		return nil, err
	}
	raw.SetDeadline(time.Time{})
	return conn, nil
}

// dialProxy opens a tunnel to target through an http, https or socks5 proxy.
func dialProxy(ctx context.Context, proxy *url.URL, target string) (net.Conn, error) {
	switch proxy.Scheme {
	case "socks5", "socks5h":
		var auth *xproxy.Auth
		if proxy.User != nil {
			password, _ := proxy.User.Password()
			auth = &xproxy.Auth{User: proxy.User.Username(), Password: password}
		}
		dialer, err := xproxy.SOCKS5("tcp", proxy.Host, auth, &net.Dialer{Timeout: 15 * time.Second})
		if err != nil {
			return nil, err
		}
		return dialer.Dial("tcp", target)
	case "http", "https":
		conn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", proxy.Host)
		if err != nil {
			return nil, err
		}
		if proxy.Scheme == "https" {
			host, _, err := net.SplitHostPort(proxy.Host)
			if err != nil {
				conn.Close()
				return nil, err
			}
			tlsConn := tls.Client(conn, &tls.Config{ServerName: host})
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				conn.Close()
				return nil, err
			}
			conn = tlsConn
		}
		tunnel, err := connectTunnel(conn, proxy, target)
		if err != nil {
			conn.Close()
			return nil, err
		}
		return tunnel, nil
	default:
		return nil, fmt.Errorf("proxy scheme %q is not supported", proxy.Scheme)
	}
}

func connectTunnel(conn net.Conn, proxy *url.URL, target string) (net.Conn, error) {
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	var request strings.Builder
	fmt.Fprintf(&request, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if proxy.User != nil {
		password, _ := proxy.User.Password()
		auth := base64.StdEncoding.EncodeToString([]byte(proxy.User.Username() + ":" + password))
		fmt.Fprintf(&request, "Proxy-Authorization: Basic %s\r\n", auth)
	}
	request.WriteString("\r\n")
	if _, err := io.WriteString(conn, request.String()); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("proxy answered CONNECT with %d", response.StatusCode)
	}
	conn.SetDeadline(time.Time{})
	if reader.Buffered() > 0 {
		// Keep bytes some proxies send right after the CONNECT response.
		return &bufferedConn{Conn: conn, reader: reader}, nil
	}
	return conn, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(buffer []byte) (int, error) { return c.reader.Read(buffer) }
