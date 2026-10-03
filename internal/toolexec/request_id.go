// Copyright (c) 2025 Reliant Labs
package toolexec

import "github.com/google/uuid"

// newRequestID returns a correlation id for one daemon round trip.
//
// The id is the key the gateway parks a waiter under (pendingCommands /
// pendingToolRequests) and the key the daemon registers the command's cancel
// func under, so two in-flight requests sharing one id cross their replies:
// the second registration overwrites the first's waiter, one caller receives
// the other's response, and the other times out.
//
// It used to be time.Now().UnixNano(). That is not unique: on macOS the wall
// clock has microsecond resolution (ids observed in production end in "000"),
// and three goroutines started together drew the SAME value in 1996 of 2000
// trials. Fanning a workspace create out across repos in parallel is exactly
// that pattern, so the id has to be random rather than temporal.
func newRequestID() string {
	return uuid.NewString()
}
