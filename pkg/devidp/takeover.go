package devidp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── Pre-registration takeover, closed ────────────────────────────────────
//
// THE ATTACK: someone registers victim@example.com with a password of their
// own, or links their own social account to it. The account is unverified,
// so it cannot sign in — until the owner of the inbox proves it, by a magic
// link, a verification link or an invitation. That proof verifies the
// ADDRESS; it must not also legitimise what the attacker set before it.
//
// TWO HALVES:
//
//   - Nothing the sign-up routes create carries a credential before its
//     proof. A sign-up's password and an unverified provider's link wait,
//     sealed in the browser that chose them (passwordCookie, linkCookie),
//     and are applied only when that browser proves the address.
//   - Whatever an unverified account DOES hold when a proof arrives — an
//     account created before this rule, by the legacy broker's sign-up, or
//     by anything else — is revoked by the proof (proveAddress): a random
//     password, no links, no passkeys. Nothing proves who set it.
//
// A revocation that fails locks the account (fail closed) and the proof's
// caller answers "unavailable": a verified account must never be left with
// credentials nobody proved.

// maxPasswordBytes bounds a password the sign-up routes accept, so a sealed
// binding that carries one keeps its fixed size.
const maxPasswordBytes = 256

// errRevokeFailed marks a proof that succeeded but whose unproven
// credentials could not be taken back: the account is left locked.
var errRevokeFailed = errors.New("the proof succeeded but credentials set before it could not be revoked; the account is locked")

// proveAddress runs proof — the issuer's check of an emailed code that also
// verifies the address — and applies the rule above. before is the account
// as read before the proof; it decides only whether the address still needs
// proving, and is read again once this proof has the account to itself.
//
// An address already verified decides nothing: proof runs alone. Otherwise
// the account is LOCKED FIRST, so nothing can sign in to it in the window
// between "verified" and "revoked" (once VerifyEmail succeeds, a password
// planted before it would otherwise work until the revocation lands):
//
//	mark → lock → [announce a window → read again → end every session
//	→ revoke credentials → take the window back] → proof → re-lock
//	→ revoke credentials → end every session → [wait out other windows]
//	→ unlock → clear the mark
//
// The bracketed steps run only under RequireVerifiedEmail (see below).
//
// WHO MAY UNLOCK. Two proofs of one account can run at once — an attacker
// who knows the user id keeps a wrong-code proof in flight while the owner
// proves the address — so "was it locked when I looked" cannot say whose
// lock it is: the wrong-code proof would lift the lock under the owner's
// succeeding one. Ownership is explicit instead, and it is what THIS CALL
// did, never what a first read suggested:
//
//   - Proofs of one account run one at a time under its proof lock: in
//     this process always, across replicas with a shared ProofLocker (see
//     THE PROOF LOCK below).
//   - Across replicas, the proof writes a fresh mark — the time and its own
//     nonce — to MetaProofLock before it locks. A FAILED proof clears the
//     mark and unlocks only while the mark is still its own; a later proof
//     that took the lock over owns it.
//   - A SUCCESSFUL proof always re-locks, whatever a concurrent failed proof
//     did, then revokes. It unlocks when this call locked the account: it
//     wrote its mark and locked, or its re-lock found the account unlocked.
//     The mark it wrote may have been cleared by another replica meanwhile;
//     that does not make the lock anyone else's.
//   - A lock with no FRESH mark (proofLockTTL) is not a proof's: the
//     issuer's own lockout, one a failed revocation left, or one under a
//     mark some earlier proof failed to clear. No proof lifts it.
//
// Everything after a successful proof runs detached from the request
// (proofCleanupTimeout): a client that hangs up then must not leave the
// account verified, locked and marked. So does the cleanup of a failed one.
//
// A revocation that fails leaves the account locked, clears the mark so the
// lock reads as one nothing lifts, and returns errRevokeFailed. revoked
// reports that credentials were taken back.
//
// The lock is not what keeps a planted credential out across replicas.
// Without a compare-and-swap at the issuer, a failed proof that reads its
// own mark and then, two round-trips later, unlocks, can land that unlock
// after another replica's proof has marked, locked and verified — until that
// proof's re-lock. So where an unverified account cannot sign in
// (RequireVerifiedEmail), every proof revokes BEFORE it verifies as well as
// after: by the time any proof marks the address verified, the account holds
// nothing set before it, and an unlock landing in that window exposes no
// unproven credential. (Nothing can add one to an unverified account
// meanwhile: the sign-up routes keep a pending password or link sealed in
// the browser until its proof.) That revocation takes nothing usable even
// when the proof then fails — an unverified account's credentials sign in to
// nothing there, and any successful proof would revoke them. Where an
// unverified account CAN sign in (mail "none"), its credentials and sessions
// are its holder's working ones, so nothing is touched before a proof
// succeeds; a late unlock there exposes nothing the account did not already
// allow. Within one process the proofs lock also closes the window itself.
//
// THE PRE-PROOF CLEANUP IS FENCED IN TIME, because it is destructive and a
// proof that will FAIL runs it too. Without a fence, a wrong-code proof that
// read the account as unverified could stall right before its revocation
// while another replica's proof verified the address and the owner set a
// password or enrolled a passkey — and then take that credential away
// (forge#1821 review, B1). So each proof announces its window
// (MetaProofWindowPrefix) before it reads the account again, sends every
// destructive call inside it or none at all, and takes the announcement back
// once its calls have returned; a successful proof waits until every
// announced window, plus proofFenceSlack, has passed before it hands the
// account back (awaitPreProofWindows). A proof whose read comes after the
// other's VerifyEmail finds the address verified and takes nothing; one whose
// read came before had announced its window first, so the successful proof
// sees it. What this leans on: the issuer answers a metadata read with the
// writes before it (the proof lock leans on the same), and a request sent
// before its deadline is served within proofFenceSlack.
//
// THE PROOF LOCK. Every proof of an account runs under its proof lock
// (lockAccount). With a ProofLocker every replica shares, a stale proof
// holds it until each call it is waiting on has returned, so no other
// proof — and no credential install, which takes the same lock — can run
// meanwhile, however late the issuer serves those calls (forge#2121). The
// fence above then covers only a call the proof gave up on, and its window
// is long (lockedPreProofWindow): a competing proof waits for the lock, not
// for the window, so the length costs nothing unless a call really stalls.
// Without a shared locker the lock is this process's, and the fence is the
// only guard between replicas.
func (r *SignInRoutes) proveAddress(ctx context.Context, before User, proof func() error) (revoked bool, err error) {
	if before.EmailVerified {
		return false, proof()
	}
	release, err := r.lockAccount(ctx, before.ID)
	if err != nil {
		return false, err
	}
	defer release()
	return r.proveAddressHeld(ctx, before, proof)
}

// proveAddressHeld is proveAddress for a caller that already holds the
// account's proof lock — HandleVerify, whose hold also covers the
// credentials it installs after the proof.
func (r *SignInRoutes) proveAddressHeld(ctx context.Context, before User, proof func() error) (revoked bool, err error) {
	if before.EmailVerified {
		return false, proof()
	}
	userID := before.ID
	c := r.cfg.Client
	// Read again: a proof that held the account a moment ago may have
	// verified it, or left it locked.
	now, err := c.FindUserByID(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("read the account for its proof: %w", err)
	}
	if now.EmailVerified {
		return false, proof()
	}
	held := false // locked by something other than a proof: never lifted here
	if now.Locked {
		mark, _, err := c.Metadata(ctx, userID, MetaProofLock)
		if err != nil {
			return false, fmt.Errorf("read the account's proof lock: %w", err)
		}
		held = !freshProofMark(mark, time.Now())
	}
	// owned: this call locked the account, so this call unlocks it.
	mark, owned := "", false
	if !held {
		mark = newProofMark(time.Now())
		if err := c.SetMetadata(ctx, userID, MetaProofLock, mark); err != nil {
			return false, fmt.Errorf("mark the account's proof lock: %w", err)
		}
		if _, err := c.lockUser(ctx, userID); err != nil {
			// Not proven yet: refuse rather than verify an account that
			// cannot be held still, and take the mark back.
			r.clearProofMark(context.WithoutCancel(ctx), userID, "an account that would not lock")
			return false, fmt.Errorf("lock the account for its proof: %w", err)
		}
		owned = true
	}
	// releaseFailed lifts this proof's lock after it failed — only while
	// the mark is still this proof's, and clearing the mark first: a mark
	// left on an unlocked account would make a later issuer lockout look
	// like a proof's. Unsure is locked: fail closed. An unlock that fails
	// puts the mark back, so the next proof can take the lock over.
	releaseFailed := func(why string) {
		if !owned {
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), proofCleanupTimeout)
		defer cancel()
		current, _, err := c.Metadata(ctx, userID, MetaProofLock)
		if err != nil {
			r.cfg.Logger.ErrorContext(ctx, "could not read the proof lock after "+why+"; the account stays locked", "user_id", userID, "error", err)
			return
		}
		if current != mark {
			return // a later proof owns the lock now
		}
		if !r.clearProofMark(ctx, userID, why) {
			return // still marked, so still a proof's lock: the next proof takes it over
		}
		if err := c.UnlockUser(ctx, userID); err != nil {
			restored := c.SetMetadata(ctx, userID, MetaProofLock, mark) == nil
			r.cfg.Logger.ErrorContext(ctx, "ALERT: an account locked for its proof could not be unlocked after "+why,
				"user_id", userID, "error", err, "mark_restored", restored, "alert", "identity.revoke_failed")
		}
	}
	// The pre-proof cleanup runs only where an unverified account cannot
	// sign in (RequireVerifiedEmail). There, its sessions and credentials
	// authenticate nothing until a proof, and any successful proof revokes
	// them anyway, so taking them before the proof — even one that then
	// fails — takes nothing a caller could use. Without that gate (mail
	// "none"), an unverified account is a working one: its password,
	// passkeys, links and sessions are its holder's, and a wrong code must
	// leave every one of them intact. Nothing new is exposed there by a
	// late unlock either: the account's credentials sign in unverified.
	pre := 0
	if r.cfg.Flow.RequireVerifiedEmail {
		// ANNOUNCE THE WINDOW FIRST. Every call below that takes something
		// from the account is sent inside [start, end) — r.preProofWindow()
		// long — or not at all, and the announcement is written before the
		// account is read again. So a proof that succeeds on another replica either
		// finds this account verified by the read below — this proof then
		// takes nothing — or finds the announcement, and waits the window
		// out before it hands the account back (awaitPreProofWindows). A
		// wrong-code proof therefore never revokes a password or passkey a
		// successful proof just installed (forge#1821 review, B1).
		start := time.Now()
		end := start.Add(r.preProofWindow())
		window := MetaProofWindowPrefix + newProofNonce()
		if err := c.SetMetadata(ctx, userID, window, announceWindow(end)); err != nil {
			releaseFailed("an unannounced pre-proof cleanup")
			return false, fmt.Errorf("announce the account's pre-proof cleanup: %w", err)
		}
		pctx, pcancel := context.WithDeadline(context.WithoutCancel(ctx), end)
		// dropWindow takes the announcement back once nothing this proof
		// sent can still land; a window left behind only costs a later
		// successful proof its wait.
		dropWindow := func() {
			dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), proofCleanupTimeout)
			defer dcancel()
			if err := c.DeleteMetadata(dctx, userID, window); err != nil {
				r.cfg.Logger.WarnContext(dctx, "could not take a proof's pre-proof window back", "user_id", userID, "error", err)
			}
		}
		again, err := c.FindUserByID(pctx, userID)
		if err != nil {
			pcancel()
			dropWindow()
			releaseFailed("an account that could not be read again")
			return false, fmt.Errorf("read the account again before its pre-proof cleanup: %w", err)
		}
		if again.EmailVerified {
			// Another proof verified the address after this one first read
			// it: what the account holds now is that proof's, not planted.
			pcancel()
			dropWindow()
			releaseFailed("an address another proof verified")
			return false, proof()
		}
		// End every session first. One created a moment before the lock
		// already carries a passed password check; the verified-email gate
		// would let it through the moment proof() marks the address
		// verified, before the revocation below could end it.
		if _, err := c.EndSessions(pctx, userID); err != nil {
			pcancel()
			releaseFailed("a failed session cleanup")
			return false, fmt.Errorf("end the account's sessions before its proof: %w", err)
		}
		// Take the unproven credentials back BEFORE the proof too, so the
		// address is never verified while one survives. The lock alone
		// cannot promise that across replicas: a failed proof elsewhere can
		// lift it between this proof's VerifyEmail and its re-lock (see WHO
		// MAY UNLOCK). Detached: a client that hangs up must not leave a
		// half-done revocation behind.
		n, err := r.revoke(pctx, userID, true)
		windowSpent := pctx.Err() != nil
		pcancel()
		if err != nil {
			if windowSpent {
				// Nothing proven here and nothing usable taken: give the
				// account back. The announcement stays, because a call sent
				// near the deadline may still land.
				releaseFailed("a pre-proof cleanup that ran out of its window")
				return false, fmt.Errorf("the account's pre-proof cleanup ran out of time: %w", err)
			}
			if !held {
				r.clearProofMark(context.WithoutCancel(ctx), userID, "an account left locked")
			}
			return false, err
		}
		dropWindow()
		pre = n
	}
	if err := proof(); err != nil {
		releaseFailed("a refused proof")
		return false, err
	}

	// Proven: the address is verified now, so nothing below may stop
	// halfway because the client went away.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), proofCleanupTimeout)
	defer cancel()
	// Re-lock whatever a concurrent failed proof on another replica did to
	// the lock; a failure here must not stop the revocation. Finding the
	// account unlocked makes this call's lock the one to lift.
	if locked, err := c.lockUser(ctx, userID); err != nil {
		r.cfg.Logger.ErrorContext(ctx, "could not re-lock a proven account before revoking its credentials", "user_id", userID, "error", err)
	} else if locked {
		owned = true
	}
	n, err := r.revoke(ctx, userID, false)
	if err != nil {
		if !held {
			r.clearProofMark(ctx, userID, "an account left locked")
		}
		return false, err
	}
	n += pre
	if r.cfg.Flow.RequireVerifiedEmail {
		// A wrong-code proof on another replica may still be inside its
		// pre-proof window; nothing may be installed on the account until
		// it has closed. The account stays locked meanwhile.
		r.awaitPreProofWindows(ctx, userID)
		var ucancel context.CancelFunc
		ctx, ucancel = context.WithTimeout(context.WithoutCancel(ctx), proofCleanupTimeout)
		defer ucancel()
	}
	if !owned {
		return n > 0, nil
	}
	if err := c.UnlockUser(ctx, userID); err != nil {
		r.cfg.Logger.ErrorContext(ctx, "address proven and credentials revoked, but the account could not be unlocked", "user_id", userID, "error", err)
		return n > 0, fmt.Errorf("unlock the account after its proof: %w", err)
	}
	if mark != "" {
		r.clearProofMark(ctx, userID, "a proven account") // inert on an unlocked, verified account if it fails
	}
	return n > 0, nil
}

// The pre-proof window: every call a proof makes to take an unverified
// account's credentials back before its proof is sent within preProofWindow
// of its announcement, or not at all. A successful proof waits until every
// announced window plus proofFenceSlack has passed — the slack covers a call
// sent just before the deadline and still in flight, and clock skew between
// replicas (the window's start is the announcing replica's clock). Package
// variables so a test can shrink them.
//
// lockedPreProofWindow is the window under a ProofLocker every replica
// shares: long, because a proof that competes for the account waits for the
// lock rather than for the window, so the window costs the owner time only
// when a call really stalls — and every call that returns inside it is
// covered by the lock however late it was served.
var (
	preProofWindow       = 15 * time.Second
	proofFenceSlack      = 5 * time.Second
	lockedPreProofWindow = 2 * time.Minute
)

// preProofWindow is this broker's window: lockedPreProofWindow under a
// shared ProofLocker, else preProofWindow.
func (r *SignInRoutes) preProofWindow() time.Duration {
	if r.sharedLock {
		return lockedPreProofWindow
	}
	return preProofWindow
}

// announceWindow is a MetaProofWindowPrefix value for a window that ends at
// end. The value is unix milliseconds and is read as a window START that
// lasts preProofWindow — what every replica since #2098 reads — so it is
// written as end minus preProofWindow: a replica that predates the longer
// window still waits for the right end (windowEnd).
func announceWindow(end time.Time) string {
	return strconv.FormatInt(end.Add(-preProofWindow).UnixMilli(), 10)
}

// windowEnd is the end of an announced window, and false when v is not one.
func windowEnd(v string) (time.Time, bool) {
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.UnixMilli(ms).Add(preProofWindow), true
}

// awaitPreProofWindows blocks until every pre-proof window another proof
// announced on the account has closed (plus proofFenceSlack). A window that
// ended more than proofLockTTL ago is left over from a proof that never took
// it back: it is ignored and removed. If the windows cannot be read, it waits
// one whole window from now — fail closed, since a window may have opened
// just now.
func (r *SignInRoutes) awaitPreProofWindows(ctx context.Context, userID string) {
	c := r.cfg.Client
	now := time.Now()
	until := now.Add(r.preProofWindow() + proofFenceSlack)
	windows, err := c.MetadataWithPrefix(ctx, userID, MetaProofWindowPrefix)
	if err != nil {
		r.cfg.Logger.WarnContext(ctx, "could not read the account's pre-proof windows; waiting a whole window", "user_id", userID, "error", err)
	} else {
		until = time.Time{}
		for key, v := range windows {
			end, ok := windowEnd(v)
			if !ok || now.Sub(end) > proofLockTTL {
				_ = c.DeleteMetadata(ctx, userID, key) // left over: inert either way
				continue
			}
			if end = end.Add(proofFenceSlack); end.After(until) {
				until = end
			}
		}
	}
	if d := time.Until(until); d > 0 {
		r.cfg.Logger.InfoContext(ctx, "waiting out another proof's pre-proof window before handing the account back", "user_id", userID, "wait", d)
		t := time.NewTimer(d)
		defer t.Stop()
		<-t.C
	}
}

const (
	// proofLockTTL is how long a proof's mark names the lock under it. A
	// proof takes seconds; a mark older than this (or from a clock this far
	// ahead) is left over, and the lock under it is treated as the issuer's.
	proofLockTTL = 5 * time.Minute
	// proofCleanupTimeout bounds the steps after a proof, which run
	// detached from the request.
	proofCleanupTimeout = 30 * time.Second
)

// newProofMark is a MetaProofLock value: "<unix seconds>.<nonce>".
func newProofMark(at time.Time) string {
	return strconv.FormatInt(at.Unix(), 10) + "." + newProofNonce()
}

// newProofNonce is 128 random bits, URL-safe.
func newProofNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// freshProofMark reports whether mark is a proof's mark written within
// proofLockTTL of now. An empty, malformed or stale mark is not.
func freshProofMark(mark string, now time.Time) bool {
	secs, nonce, ok := strings.Cut(mark, ".")
	if !ok || nonce == "" {
		return false
	}
	at, err := strconv.ParseInt(secs, 10, 64)
	if err != nil {
		return false
	}
	age := now.Sub(time.Unix(at, 0))
	return age < proofLockTTL && age > -proofLockTTL
}

// clearProofMark deletes MetaProofLock, retrying once: a mark left on an
// account that is later locked by the issuer would read as a proof's lock
// while it is fresh. It reports whether the mark is gone.
func (r *SignInRoutes) clearProofMark(ctx context.Context, userID, what string) bool {
	c := r.cfg.Client
	err := c.DeleteMetadata(ctx, userID, MetaProofLock)
	if err != nil {
		err = c.DeleteMetadata(ctx, userID, MetaProofLock)
	}
	if err != nil {
		r.cfg.Logger.ErrorContext(ctx, "could not clear the proof lock of "+what, "user_id", userID, "error", err)
		return false
	}
	return true
}

// keyedMutex serialises work per key — here, address proofs per user id —
// and forgets a key once nobody holds or waits for it. The zero value is
// ready to use.
type keyedMutex struct {
	mu   sync.Mutex
	keys map[string]*keyedSlot
}

type keyedSlot struct {
	ch   chan struct{} // holds one token while the key is locked
	refs int           // holders and waiters
}

// lock waits for key, or for ctx to end, and returns the release.
func (k *keyedMutex) lock(ctx context.Context, key string) (func(), error) {
	k.mu.Lock()
	if k.keys == nil {
		k.keys = map[string]*keyedSlot{}
	}
	s := k.keys[key]
	if s == nil {
		s = &keyedSlot{ch: make(chan struct{}, 1)}
		k.keys[key] = s
	}
	s.refs++
	k.mu.Unlock()
	select {
	case s.ch <- struct{}{}:
		return func() { <-s.ch; k.drop(key, s) }, nil
	case <-ctx.Done():
		k.drop(key, s)
		return nil, ctx.Err()
	}
}

func (k *keyedMutex) drop(key string, s *keyedSlot) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if s.refs--; s.refs == 0 {
		delete(k.keys, key)
	}
}

// revoke takes back an account's unproven credentials and ends its
// sessions, retrying once. The caller holds the account locked. If both
// tries fail it makes one last attempt at the password alone (the credential
// a planted sign-up most likely set) and raises an alert: the account stays
// locked until someone resets it by hand.
//
// pre marks the cleanup BEFORE a proof, bounded by its window: when ctx's
// deadline is what stopped it, the account is still unverified and nothing
// on it is usable, so the error is returned as the deadline (no last
// attempt, no alert) and the caller gives the account back.
func (r *SignInRoutes) revoke(ctx context.Context, userID string, pre bool) (int, error) {
	c := r.cfg.Client
	n, err := c.RevokeCredentials(ctx, userID)
	if err != nil && ctx.Err() == nil {
		n, err = c.RevokeCredentials(ctx, userID)
	}
	if err == nil {
		if n > 0 {
			r.cfg.Logger.InfoContext(ctx, "credentials set before the address was proven were revoked", "user_id", userID, "count", n)
		}
		return n, nil
	}
	if pre && ctx.Err() != nil {
		return 0, fmt.Errorf("take back the account's unproven credentials: %w", ctx.Err())
	}
	pwErr := c.SetPassword(ctx, userID, unguessablePassword())
	r.cfg.Logger.ErrorContext(ctx, "ALERT: unproven credentials could not be revoked; the account stays LOCKED until its credentials are reset by hand",
		"user_id", userID, "error", err, "password_replaced", pwErr == nil, "alert", "identity.revoke_failed")
	return 0, fmt.Errorf("%w: %v", errRevokeFailed, err)
}

// grantSignupRoleIfSelfSignup grants signup_role to an account public
// sign-up created (MetaSelfSignup), once (MetaSignupRoleGranted), and only
// when the policy still lets its now-verified address sign up. An invited or
// declared account, or a re-verification, gets nothing.
func (r *SignInRoutes) grantSignupRoleIfSelfSignup(ctx context.Context, userID, email string) {
	if r.cfg.Policy.SignupRole == "" || r.cfg.ProjectID == "" || !r.cfg.Policy.SignupAllows(email) {
		return
	}
	c := r.cfg.Client
	self, err := c.HasMetadata(ctx, userID, MetaSelfSignup)
	if err != nil || !self {
		if err != nil {
			r.cfg.Logger.ErrorContext(ctx, "could not read how the account was created; no sign-up role", "user_id", userID, "error", err)
		}
		return
	}
	if done, err := c.HasMetadata(ctx, userID, MetaSignupRoleGranted); err != nil || done {
		return
	}
	if !r.grantSignupRole(ctx, userID) {
		return
	}
	if err := c.SetMetadata(ctx, userID, MetaSignupRoleGranted, "1"); err != nil {
		r.cfg.Logger.WarnContext(ctx, "sign-up role granted but not recorded", "user_id", userID, "error", err)
	}
}
