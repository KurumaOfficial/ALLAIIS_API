// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"testing"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

func TestStoredProxyTextWithoutAVault(t *testing.T) {
	if got := storedProxyText(context.Background(), nil); got != "" {
		t.Fatalf("nil store returned %q", got)
	}
	client := webprov.New(time.Second)
	applyProxiesToWeb(context.Background(), nil, client)
}
