// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
)

// SetStreamPingInterval shortens the keep-alive period for a test and returns
// the previous one.
func SetStreamPingInterval(period time.Duration) time.Duration {
	previous := streamPingInterval
	streamPingInterval = period
	return previous
}

// InactivityWindow exposes the watchdog clamp for tests.
func InactivityWindow(route config.Route) time.Duration {
	return inactivity(route)
}
