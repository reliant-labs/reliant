package devidp

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ── Signing in an account created moments ago ─────────────────────────
//
// Zitadel is CQRS: creating a user appends an event and answers at once,
// and the user becomes readable when the user projection has processed
// that event. A session's user check reads the projection WITHOUT asking
// it to catch up first (v4.20.3 internal/api/grpc/session/v2/session.go:
// `GetUserByID(ctx, false, id)` and `GetUserByLoginName(ctx, false, name)`),
// so a session created right after the user can be refused with "User
// could not be found (QUERY-Dfbg2)" for an account that exists.
//
// The window is usually milliseconds. It is not always: right after the
// issuer starts, or while it is busy, its projections lag by seconds.
// Observed: a sign-up 12 seconds after a dev Zitadel started created the
// account and then failed to sign it in.
//
// So the sign-ins that follow a creation wait for the user to become
// visible, for a bounded time. Only those: an ordinary sign-in naming an
// unknown account must fail at once, both because waiting would only
// delay the answer and because a not-found that is slower than a wrong
// password is an account oracle.

// userNotFoundID is the query side's "User could not be found"
// (Errors.User.NotFound), verified against Zitadel v4.20.3
// (internal/query/user.go).
const userNotFoundID = "QUERY-Dfbg2"

// newUserVisibleWithin bounds how long a sign-in waits for a user it just
// created to become visible to the session API. A var so tests shrink it.
var newUserVisibleWithin = 10 * time.Second

// ErrSignInAfterSignUp marks a sign-up whose account WAS created but whose
// sign-in then failed. The caller must not answer "could not be created":
// the address now has an account, and the person can sign in with the
// password they just chose.
var ErrSignInAfterSignUp = errors.New("the account was created, but signing it in failed")

// isUserNotFound reports whether err is the issuer's query-side user
// not-found.
func isUserNotFound(err error) bool {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return false
	}
	id, _ := apiErr.errorID()
	return id == userNotFoundID
}

// createSessionForNewUser is CreateSession for a user this process has just
// created: a not-found is the projection lagging behind the creation, so
// it is retried, with backoff, until newUserVisibleWithin has passed or ctx
// ends. Any other answer, including a wrong password, returns at once. The
// password is never checked while the user is not found, so a retry does
// not count a failed attempt against the account.
func (c *Client) createSessionForNewUser(ctx context.Context, creds Credentials) (Session, error) {
	deadline := time.Now().Add(newUserVisibleWithin)
	delay := 25 * time.Millisecond
	for {
		s, err := c.CreateSession(ctx, creds)
		if err == nil || !isUserNotFound(err) || !time.Now().Add(delay).Before(deadline) {
			return s, err
		}
		if cerr := ctx.Err(); cerr != nil {
			// Checked before waiting: a select on a done context and a
			// timer that has also fired picks either.
			return Session{}, fmt.Errorf("%w (still waiting for the new account to become visible: %w)", err, cerr)
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return Session{}, fmt.Errorf("%w (still waiting for the new account to become visible: %w)", err, ctx.Err())
		case <-t.C:
		}
		delay = min(delay*2, 500*time.Millisecond)
	}
}

// signInNewUser is SignIn for an account created moments ago, named by the
// id its creation returned. The id, not the email: resolving an email to a
// login name searches the same lagging projection, and a miss there falls
// back to a login name that may not exist (see loginNameFor).
func (c *Client) signInNewUser(ctx context.Context, cfg FlowConfig, creds Credentials) (Tokens, error) {
	flow, err := c.BeginFlow(ctx, cfg)
	if err != nil {
		return Tokens{}, err
	}
	session, err := c.createSessionForNewUser(ctx, creds)
	if err != nil {
		return Tokens{}, err
	}
	if err := c.gate(ctx, cfg, session, creds.UserID); err != nil {
		return Tokens{}, err
	}
	return c.finish(ctx, cfg, flow, session, methodFromCredentials(creds))
}
