// SPDX-License-Identifier: GPL-3.0-only

package catalog

import (
	"net/url"
	"testing"
)

func TestRotatingProxy(t *testing.T) {
	if rotatingProxy(nil) != nil {
		t.Fatal("an empty pool returned a proxy")
	}
	first, _ := url.Parse("http://127.0.0.1:1")
	second, _ := url.Parse("http://127.0.0.1:2")
	pick := rotatingProxy([]*url.URL{first, second})
	for index, want := range []string{"127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:1"} {
		got, err := pick(nil)
		if err != nil || got == nil || got.Host != want {
			t.Fatalf("pick %d = %v, %v; want %s", index, got, err, want)
		}
	}
}
