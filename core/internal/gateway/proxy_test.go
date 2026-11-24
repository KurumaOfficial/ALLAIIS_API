// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"testing"
)

func TestParseProxyList(t *testing.T) {
	text := `# spend list
127.0.0.1:8080
127.0.0.1:8081:user:pass
http://u:p@127.0.0.1:3128
socks5://127.0.0.1:1080
# duplicate of the first
127.0.0.1:8080
`
	list, err := ParseProxyList(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 4 {
		t.Fatalf("parsed %d: %v", len(list), list)
	}
	if list[0].Scheme != "http" || list[0].Host != "127.0.0.1:8080" {
		t.Errorf("shorthand: %v", list[0])
	}
	if list[1].User == nil || list[1].User.Username() != "user" {
		t.Errorf("auth shorthand: %v", list[1])
	}
	if list[3].Scheme != "socks5" {
		t.Errorf("scheme: %v", list[3])
	}
	for _, bad := range []string{"nope", "host:0", "host:99999", "ftp://host:1", "http://host:1/path", "host:port:u:p"} {
		if _, err := ParseProxyList(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestProxyPoolRotates(t *testing.T) {
	list, err := ParseProxyList("127.0.0.1:1\n127.0.0.1:2\n")
	if err != nil {
		t.Fatal(err)
	}
	pool := &proxyPool{}
	pool.set(list)
	first, _ := pool.Proxy(nil)
	second, _ := pool.Proxy(nil)
	third, _ := pool.Proxy(nil)
	if first == nil || second == nil || third == nil {
		t.Fatal("empty proxy")
	}
	if first.Host != "127.0.0.1:1" || second.Host != "127.0.0.1:2" || third.Host != "127.0.0.1:1" {
		t.Fatalf("rotation %v %v %v", first, second, third)
	}
	pool.set(nil)
	if got, _ := pool.Proxy(nil); got != nil {
		t.Fatalf("empty pool returned %v", got)
	}
}
