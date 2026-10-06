// Copyright (c) 2025 Reliant Labs

package connections

import "time"

// SetBrokerClock replaces the clock the broker stamps and checks flow expiry
// with. Expiry is decided in Go (ConsumeOAuthFlow is handed b.now()), so a test
// must move THIS clock to put a flow past its TTL — editing expires_at with the
// database's now() compares two different clocks.
func SetBrokerClock(b *Broker, now func() time.Time) {
	b.now = now
}
