// Copyright (c) 2025 Reliant Labs

package orgaccounts

import "time"

// SetClock replaces the clock the service stamps grants and marks with, for the tests of
// membership windows. It is a test seam: production reads the real clock.
func SetClock(s Service, now func() time.Time) { s.(*service).now = now }
