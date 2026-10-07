// Copyright (c) 2025 Reliant Labs

// Package ghusers looks GitHub users up for a trigger's "Only from": a login
// to the numeric user id that trigger.sender.id carries, and an id back to the
// person's current login.
//
// An allowlist stores ids, because a login can be renamed and then registered
// by someone else while the id is never reused; people read logins. So a
// login is resolved to its id once, when it is added, and an id is shown by
// the login GitHub reports for it now — which follows a rename instead of
// showing a name that may belong to someone else.
//
// forge:outbound-io
//
// Every lookup asks GitHub with the CALLER's own token, the one their GitHub
// triggers use (control-plane's delegated token when hosted, their saved
// GitHub connection when self-hosted):
//
//	GET /users/{login}        a login to its user
//	GET /user/{account_id}    an id to its user's current login
package ghusers

import (
	"context"
	"errors"
)

// IntegrationID is the integration whose users this package looks up.
const IntegrationID = "github"

// MaxQueries bounds one Resolve: each query is a request to GitHub on the
// caller's rate limit.
const MaxQueries = 25

// TokenSource returns a user's current GitHub token. ghaccess's token sources
// satisfy it; an error the user must act on (not connected, needs reconnect)
// is passed through for the caller to classify.
type TokenSource interface {
	Token(ctx context.Context, userID string) (string, error)
}

// User is one GitHub user.
type User struct {
	// ID is GitHub's numeric user id, as a string: trigger.sender.id.
	ID string
	// Login is the user's login now. For display only.
	Login string
}

// Found is one query Resolve answered.
type Found struct {
	// Query is the login or id as it was asked.
	Query string
	User  User
}

var (
	// ErrTooManyQueries is a Resolve asked for more than MaxQueries.
	ErrTooManyQueries = errors.New("ghusers: too many people to look up at once")
	// ErrTokenRejected is GitHub refusing the caller's token (401): they
	// must reconnect GitHub.
	ErrTokenRejected = errors.New("ghusers: GitHub rejected the token")
)
