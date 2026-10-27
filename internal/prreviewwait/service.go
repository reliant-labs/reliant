// Copyright (c) 2025 Reliant Labs
package prreviewwait

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"

	"github.com/reliant-labs/reliant/internal/prhandoff"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
)

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ErrUnavailable is a terminal/unverified original author, not a spawn request.
var ErrUnavailable = errors.New("original author unavailable; needs human")

type service struct{ deps Deps }

// New constructs the disabled wait service, requiring every dependency.
func New(d Deps) (Service, error) {
	if d.Store == nil || d.Continuity == nil || d.Signals == nil {
		return nil, errors.New("review wait dependencies missing")
	}
	return &service{d}, nil
}

func (s *service) Arm(ctx context.Context, r Request) (Subscription, error) {
	if r.OwnerID == "" || r.ChatID == "" || r.Execution.WorkflowID == "" || r.Execution.RunID == "" || r.RepoID <= 0 || r.PR <= 0 || !shaPattern.MatchString(r.HeadSHA) {
		return Subscription{}, prhandoff.ErrRefused
	}
	b, err := s.deps.Store.Binding(ctx, r.OwnerID, r.RepoID, r.PR)
	if err != nil {
		return Subscription{}, err
	}
	if b.Author.OwnerID != r.OwnerID || b.Author.ChatID != r.ChatID || b.Author.RootID != r.Execution.WorkflowID || b.Pull.HeadSHA != r.HeadSHA {
		return Subscription{}, prhandoff.ErrRefused
	}
	proof, err := s.deps.Continuity.Resolve(ctx, runcontinuity.Execution{WorkflowID: b.Author.RootID, RunID: b.Author.ExecutionID})
	if err != nil {
		return Subscription{}, err
	}
	if !proof.Live || proof.Current != r.Execution {
		return Subscription{}, ErrUnavailable
	}
	return s.deps.Store.Arm(ctx, r, proof)
}
func (s *service) Ready(ctx context.Context, sub Subscription) error {
	return s.deps.Store.Ready(ctx, sub)
}
func (s *service) Read(ctx context.Context, sub Subscription) (Outcome, error) {
	return s.deps.Store.Read(ctx, sub)
}
func (s *service) Consume(ctx context.Context, sub Subscription, outcome Outcome) error {
	return s.deps.Store.Consume(ctx, sub, outcome)
}
func (s *service) Cancel(ctx context.Context, sub Subscription) error {
	return s.deps.Store.Cancel(ctx, sub)
}

func (s *service) Publish(ctx context.Context, o Outcome) error {
	if o.BindingID == "" || len(o.BindingID) > 255 || o.ProviderRef == "" || len(o.ProviderRef) > 255 || o.Sequence <= 0 || !shaPattern.MatchString(o.HeadSHA) || !validState(o.State) {
		return prhandoff.ErrRefused
	}
	// Deterministic reference identity; raw findings and caller run IDs do not exist.
	digest := sha256.Sum256([]byte(o.BindingID + "\x00" + o.HeadSHA + "\x00" + o.ProviderRef))
	o.ID = hex.EncodeToString(digest[:])
	sub, err := s.deps.Store.Put(ctx, o)
	if err != nil {
		return err
	}
	if sub.ID == "" || sub.State != "armed" || sub.ConsumedSequence >= o.Sequence {
		return nil
	}
	// A rollover can race the exact signal. Bound immediate retries, preserving
	// durable unconsumed inbox on failure so the producer activity can retry.
	for attempt := 0; attempt < 3; attempt++ {
		proof, resolveErr := s.deps.Continuity.Resolve(ctx, runcontinuity.Execution{WorkflowID: sub.WorkflowID, RunID: sub.OriginalRunID})
		if resolveErr != nil {
			return resolveErr
		}
		if !proof.Live {
			if err := s.deps.Store.Unavailable(ctx, sub); err != nil {
				return err
			}
			return ErrUnavailable
		}
		if proof.Current.RunID == sub.CurrentRunID && sub.NotifiedSequence >= o.Sequence {
			return nil
		}
		sub, err = s.deps.Store.Advance(ctx, sub, proof)
		if err != nil {
			return err
		}
		if err = s.deps.Signals.SignalReview(ctx, proof.Current, sub.ID); err == nil {
			return s.deps.Store.Notified(ctx, sub, o.Sequence)
		}
	}
	return fmt.Errorf("exact author review doorbell: %w", err)
}
func validState(s string) bool {
	return s == "approved" || s == "changes" || s == "closed" || s == "unavailable"
}
