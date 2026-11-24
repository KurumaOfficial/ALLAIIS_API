// SPDX-License-Identifier: GPL-3.0-only

package webprov

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestProxySetRotates(t *testing.T) {
	set := newProxySet()
	first, _ := url.Parse("http://127.0.0.1:1")
	second, _ := url.Parse("http://127.0.0.1:2")
	set.set([]*url.URL{first, second})
	if got := set.pick(); got.Host != "127.0.0.1:1" {
		t.Fatalf("first %v", got)
	}
	if got := set.pick(); got.Host != "127.0.0.1:2" {
		t.Fatalf("second %v", got)
	}
	if got := set.pick(); got.Host != "127.0.0.1:1" {
		t.Fatalf("rotated %v", got)
	}
	set.set(nil)
	if got := set.pick(); got != nil {
		t.Fatalf("empty set returned %v", got)
	}
}

func TestConnectTunnelSendsProxyAuth(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	lines := make(chan []string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		var headers []string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			headers = append(headers, strings.TrimSpace(line))
			if line == "\r\n" {
				break
			}
		}
		lines <- headers
		fmt.Fprint(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
		io.Copy(conn, reader)
	}()

	proxy, _ := url.Parse("http://user:pass@" + listener.Addr().String())
	conn, err := dialProxy(context.Background(), proxy, "example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	headers := <-lines
	if len(headers) < 2 || headers[0] != "CONNECT example.com:443 HTTP/1.1" {
		t.Fatalf("request %v", headers)
	}
	auth := ""
	for _, line := range headers {
		if strings.HasPrefix(strings.ToLower(line), "proxy-authorization:") {
			auth = line
		}
	}
	if !strings.Contains(auth, "Basic dXNlcjpwYXNz") {
		t.Fatalf("auth %q in %v", auth, headers)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(conn, buffer); err != nil || string(buffer) != "ping" {
		t.Fatalf("tunnel echo %q %v", buffer, err)
	}
}

func TestConnectTunnelRejectsARefusal(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		bufio.NewReader(conn).ReadString('\n')
		fmt.Fprint(conn, "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n")
	}()
	proxy, _ := url.Parse("http://" + listener.Addr().String())
	if _, err := dialProxy(context.Background(), proxy, "example.com:443"); err == nil {
		t.Fatal("a refused tunnel was accepted")
	}
}
