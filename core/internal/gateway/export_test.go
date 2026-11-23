// SPDX-License-Identifier: GPL-3.0-only

package gateway

import "time"

// SetStreamPingInterval shortens the keep-alive period for a test and returns
// the previous one.
func SetStreamPingInterval(period time.Duration) time.Duration {
	previous := streamPingInterval
	streamPingInterval = period
	return previous
}
