// Copyright (c) 2025 Reliant Labs

// Package readguard keeps one user's list reads from taking the database pool.
//
// The user-update stream makes a client refetch its chat list, its archived
// list and a chat's approvals whenever a chat changes. A burst of changes (an
// automation's runs finishing, a reconnect replaying them) used to send one
// refetch per change, and the server ran every one to completion: on
// 2026-10-29 one session sent 1,391 ListChats in a minute, filled the
// api-server's 25 connections, and every other user's reads queued behind
// them. Clients coalesce those refetches now (#1865), but a desktop build
// keeps storming until it updates, so the server has to hold its own line.
//
// The guard is a Connect interceptor on the reads it names. For each user:
//
//   - Identical requests share one read. A request that arrives while the same
//     user's identical request (same procedure, same message) is running waits
//     for that read's answer instead of starting another. The read runs on a
//     context of its own, so the caller that started it giving up does not fail
//     the others; it is cancelled when every caller waiting on it has gone.
//   - At most PerUser distinct reads run at once. One more waits up to Wait for
//     a slot (or for an identical read to join), and at most Queue wait; past
//     either it is refused with ResourceExhausted and a Retry-After, which the
//     client's query retry honours with its usual backoff.
//
// So one user holds at most PerUser of the pool's connections through these
// reads, whatever their clients send.
//
// Built by hand, by internal/app's apiMountOptions (providers_api.go): never a
// node compose.go constructs from Deps.
//
//forge:provided
package readguard

import (
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
)

// Service is the guard: a connect.Interceptor, mounted as a handler option on
// the services whose reads Deps names. Streams pass through it untouched.
type Service interface {
	WrapUnary(next connect.UnaryFunc) connect.UnaryFunc
	WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc
	WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc
}

// Procedure is a unary read the guard shares and bounds. Read builds one.
type Procedure interface {
	// Name is the procedure's full name, as a request's Spec().Procedure has it.
	Name() string
	// Respond wraps msg, a copy of the shared response message, for one more
	// caller.
	Respond(msg proto.Message) connect.AnyResponse
}

// Deps is what the guard guards and how hard.
type Deps struct {
	// Procedures are the reads guarded. Every other procedure passes through.
	Procedures []Procedure
	// Limits is how hard. Production takes the defaults; tests tighten them.
	Limits Limits
}

// Limits bounds the guard. A zero field takes its default.
type Limits struct {
	// PerUser bounds the distinct reads one user has running at once.
	PerUser int
	// Queue bounds how many more of one user's distinct reads wait for a slot.
	Queue int
	// Wait bounds how long a read waits for a slot before it is refused.
	Wait time.Duration
	// ReadTimeout bounds a shared read, which no single caller's deadline does.
	ReadTimeout time.Duration
	// RetryAfter is what a refused read is told.
	RetryAfter time.Duration
}
