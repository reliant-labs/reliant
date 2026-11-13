// Copyright (c) 2025 Reliant Labs

// Package orgpolicy answers reliant's enforcement points with an org's admin
// policy (control-plane docs/design/org-policies.md). Control-plane is the
// only source of truth: this package asks it, through
// AccessTokenInternalService.ResolveOrgPolicy, and keeps nothing beyond a
// short-lived cache and the last answer it got.
//
// ── EVERY ANSWER CARRIES ITS VERSION ──────────────────────────────────
//
// A Policy is one version of the org's document. An enforcement point records
// Policy.Version on every decision it makes with it, so any refusal is
// attributable to the exact edit that caused it (§5.3).
//
// ── THE CACHE ─────────────────────────────────────────────────────────
//
// An answer is trusted for CacheTTL (30 s, the membership cache's bound), so
// an admin's tightening reaches a run within CacheTTL plus its next decision
// point. "No policy" is an answer like any other and is cached the same way.
// A newer version is never replaced by an older one: once a versioned policy
// is known, an older answer, or a control plane that no longer has the rpc,
// is a failed confirmation (below), never a fresh answer.
//
// ── WHEN CONTROL-PLANE CANNOT BE REACHED (D10) ───────────────────────
//
// The last good answer for the org keeps serving for up to Grace (10 min)
// after it was last CONFIRMED, marked Stale. A failed fetch, an older version
// than the one held, and (once a versioned policy is known) a control plane
// without the rpc all fail to confirm it. Past that, Resolve FAILS CLOSED with
// ErrUnavailable: the caller refuses the work retryably, never runs it
// unconstrained. An org whose last answer was "no policy" is no exception:
// within Grace it is still "no policy"; beyond it, it is unknown.
//
// ── NO CONTROL PLANE, OR AN OLD ONE ───────────────────────────────────
//
// A self-hosted deployment has no orgs and no policies: its Resolver is nil,
// and a nil *Resolver answers "no policy" for every org. A control plane that
// predates the rpc answers the same way (ErrUnsupported is "no policy") while
// no versioned policy is known for the org, the pre-policy behaviour an old
// control plane implies (§7).
//
// ── ON THE CONTEXT ────────────────────────────────────────────────────
//
// A process installs its Resolver once, at the edges where it installs org
// membership (WithResolver: every API and gateway request, every activity),
// and on its background (SetProcessResolver). For reads the one installed on
// ctx, else the process's.
//
//forge:exclude-contract: a cache over one control-plane call; consumers call For or declare the one-method slice they use, and tests pass a fake Source
package orgpolicy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/protobuf/proto"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/logging"
)

// CacheTTL bounds how long an answer about one org is trusted: the lag
// between an admin's edit at control-plane and its effect here.
const CacheTTL = 30 * time.Second

// Grace is how long after it was fetched the last good answer keeps serving
// while control-plane cannot be reached. Past it, Resolve fails closed.
const Grace = 10 * time.Minute

// ReasonUnavailable is the x-forge-error-reason of ErrUnavailable.
const ReasonUnavailable = "org_policy_unavailable"

// ErrUnavailable is an org whose policy cannot be known: control-plane has
// not answered for longer than Grace. It is retryable; work governed by the
// policy is refused until an answer arrives, never run unconstrained.
var ErrUnavailable = errors.New("orgpolicy: the organization's policy cannot be read right now")

// Source is control-plane's answer. accesstokenclient.Service is one.
type Source interface {
	ResolveOrgPolicy(ctx context.Context, orgID string) (accesstokenclient.OrgPolicy, error)
}

// Policy is one version of an org's policy document, as an enforcement point
// uses it.
type Policy struct {
	OrgID string
	// Version is 0 for "no policy", then the document's version.
	Version int64
	// Doc is the document; never nil. Empty (no sections) is no policy.
	// It is shared by every caller of the same version: read it, never
	// modify it.
	Doc *controlplanev1.OrgPolicy
	// Stale marks the last good answer served while control-plane cannot be
	// reached (within Grace).
	Stale bool
}

// None reports whether p has no rule at all: today's behaviour exactly.
func (p Policy) None() bool { return p.Doc == nil || proto.Size(p.Doc) == 0 }

// none is the "no policy" answer for orgID.
func none(orgID string) Policy {
	return Policy{OrgID: orgID, Doc: &controlplanev1.OrgPolicy{}}
}

// retryAfter is how long after a failed fetch the next one waits: during an
// outage every decision is answered from what is held (the last good policy,
// or the refusal) instead of every decision asking control-plane again.
const retryAfter = 5 * time.Second

// Resolver caches Source's answers. A nil *Resolver is a deployment with no
// policies.
type Resolver struct {
	src   Source
	ttl   time.Duration
	grace time.Duration
	retry time.Duration
	now   func() time.Time

	mu      sync.Mutex
	entries map[string]*entry
}

// entry is what is held for one org: its last good answer, the last failure,
// and the fetch in flight.
type entry struct {
	policy Policy
	// fetched is when policy was last confirmed by control-plane; zero when
	// it never was.
	fetched time.Time
	// failed is the refusal of the last fetch, and retryAt when the next one
	// may run; both zero after a success.
	failed  error
	retryAt time.Time
	// inflight is closed when the fetch now running for this org ends; nil
	// when none runs.
	inflight chan struct{}
}

// New returns a Resolver over src. A nil src is no control plane: New
// returns nil, the Resolver of a deployment with no policies.
func New(src Source) *Resolver {
	if src == nil {
		return nil
	}
	return &Resolver{src: src, ttl: CacheTTL, grace: Grace, retry: retryAfter, now: time.Now, entries: map[string]*entry{}}
}

// Resolve returns orgID's policy. "No policy" is a successful answer; an
// error is ErrUnavailable (a retryable svcerr Unavailable, reason
// ReasonUnavailable) or the caller's own context ending. orgID "" is work
// outside any org: no policy.
func (r *Resolver) Resolve(ctx context.Context, orgID string) (Policy, error) {
	if r == nil || orgID == "" {
		return none(orgID), nil
	}
	for {
		r.mu.Lock()
		now := r.now()
		e := r.entries[orgID]
		switch {
		case e == nil:
			e = &entry{}
			r.entries[orgID] = e
		case !e.fetched.IsZero() && now.Before(e.fetched.Add(r.ttl)):
			p := e.policy
			r.mu.Unlock()
			return p, nil
		case e.inflight != nil:
			// One fetch per org at a time: wait for it, then look again.
			wait := e.inflight
			r.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return Policy{}, ctx.Err()
			}
		case now.Before(e.retryAt):
			p, err := r.heldLocked(e, now)
			r.mu.Unlock()
			return p, err
		}
		done := make(chan struct{})
		e.inflight = done
		r.mu.Unlock()
		p, err := r.fetch(ctx, orgID, e)
		close(done)
		return p, err
	}
}

// heldLocked is what e can answer without control-plane: the last good
// policy within Grace (Stale), else the last refusal.
func (r *Resolver) heldLocked(e *entry, now time.Time) (Policy, error) {
	if !e.fetched.IsZero() && now.Before(e.fetched.Add(r.grace)) {
		p := e.policy
		p.Stale = true
		return p, nil
	}
	if e.failed != nil {
		return Policy{}, e.failed
	}
	return Policy{}, unavailable(errors.New("no answer held"))
}

// fetch asks control-plane and settles e with the answer. The caller set
// e.inflight and closes it after fetch returns.
func (r *Resolver) fetch(ctx context.Context, orgID string, e *entry) (Policy, error) {
	got, err := r.src.ResolveOrgPolicy(ctx, orgID)
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	e.inflight = nil
	r.evictLocked(now, e)
	known := !e.fetched.IsZero() && e.policy.Version > 0
	switch {
	case err == nil && known && got.Version < e.policy.Version:
		// Versions only move forward. An answer older than the policy held
		// confirms nothing: it is a failed confirmation, like an outage, so
		// the held document keeps its fetched time and Grace runs out on it.
		err = fmt.Errorf("control-plane answered version %d of a policy already known at version %d", got.Version, e.policy.Version)
	case err == nil:
		p := Policy{OrgID: orgID, Version: got.Version, Doc: got.Policy}
		if p.Doc == nil {
			p.Doc = &controlplanev1.OrgPolicy{}
		}
		e.policy, e.fetched, e.failed, e.retryAt = p, now, nil, time.Time{}
		return p, nil
	case errors.Is(err, accesstokenclient.ErrUnsupported) && !known:
		// A control plane without the rpc has no policies (§7), as long as
		// none is known. Once a versioned policy was read, the rpc going
		// away (a control plane rolled back) is a failed confirmation below,
		// never a silent "no policy".
		e.policy, e.fetched, e.failed, e.retryAt = none(orgID), now, nil, time.Time{}
		return e.policy, nil
	case ctx.Err() != nil:
		// The caller gave up; that is not control-plane's outage, and the
		// held answer is left as it was for the next caller.
		return Policy{}, ctx.Err()
	}
	e.failed, e.retryAt = unavailable(err), now.Add(r.retry)
	p, herr := r.heldLocked(e, now)
	if herr == nil {
		logging.WarnContext(ctx, "orgpolicy: control-plane did not answer; serving the last good policy",
			"org_id", orgID, "version", p.Version, "age", now.Sub(e.fetched).Round(time.Second), "error", err)
		return p, nil
	}
	logging.ErrorContext(ctx, "orgpolicy: control-plane did not answer and no good policy is held; refusing the org's governed work",
		"org_id", orgID, "error", err)
	return Policy{}, herr
}

// evictLocked drops every entry but keep that can answer nothing any more (no
// good answer within Grace, no refusal to repeat, no fetch running), so the
// map holds at most the orgs asked about within Grace.
func (r *Resolver) evictLocked(now time.Time, keep *entry) {
	for id, e := range r.entries {
		if e == keep || e.inflight != nil {
			continue
		}
		goodGone := e.fetched.IsZero() || !now.Before(e.fetched.Add(r.grace))
		if goodGone && !now.Before(e.retryAt) {
			delete(r.entries, id)
		}
	}
}

func unavailable(cause error) error {
	return svcerr.Wrap(svcerr.WithReason(svcerr.WithCause(svcerr.Unavailable(ErrUnavailable.Error()+"; try again shortly"),
		fmt.Errorf("%w: %w", ErrUnavailable, cause)), ReasonUnavailable))
}

// IsUnavailable reports whether err is Resolve failing closed.
func IsUnavailable(err error) bool { return errors.Is(err, ErrUnavailable) }

// ── On the context ────────────────────────────────────────────────────

type resolverKey struct{}

// processResolver is the Resolver of a decision whose context carries none.
var processResolver atomic.Pointer[Resolver]

// SetProcessResolver installs r for every decision whose context carries
// none: a process's background, once it has built its resolver at boot. nil
// clears it (no policies).
func SetProcessResolver(r *Resolver) { processResolver.Store(r) }

// WithResolver returns ctx carrying r. A process installs it once, at its
// edge, where it installs membership (internal/authz).
func WithResolver(ctx context.Context, r *Resolver) context.Context {
	return context.WithValue(ctx, resolverKey{}, r)
}

// ResolverFrom is the Resolver installed on ctx, else the process's, else
// nil (no policies).
func ResolverFrom(ctx context.Context) *Resolver {
	if r, ok := ctx.Value(resolverKey{}).(*Resolver); ok && r != nil {
		return r
	}
	return processResolver.Load()
}

// For is orgID's policy from the Resolver on ctx (ResolverFrom).
func For(ctx context.Context, orgID string) (Policy, error) {
	return ResolverFrom(ctx).Resolve(ctx, orgID)
}
