// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"errors"

	"buf.build/go/protovalidate"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/protobuf/proto"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// The first lines of every rpc: who calls, as the domain's Actor, and the
// request checked against its buf.validate rules. The domain decides
// everything else (the gate, org scoping, who may do what) and validates
// every field again: the user chain runs NoValidate, so protovalidate here is
// a first line, never the only one.

// actor is the request's caller as a gtmsvc.Actor in orgID.
//
// Only a person-class credential is a Person: the ones auth.RequirePerson
// admits (an app session, a browser session, the self-host shared key). Every
// other credential is an Agent with no chat, an rlat_ access token included,
// on every rpc: the CLI login every agent shell inherits is one, so a token
// holder gets the domain's agent rules (no approving an item, verifying a
// claim, resolving a send or activating a strategy).
func actor(ctx context.Context, orgID string) (gtmsvc.Actor, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return gtmsvc.Actor{}, svcerr.Unauthenticated("user ID not found in context")
	}
	kind := gtmsvc.ActorAgent
	if personCredential(ctx) {
		kind = gtmsvc.ActorPerson
	}
	return gtmsvc.Actor{UserID: userID, OrgID: orgID, Kind: kind}, nil
}

// personCredential reports whether ctx was authenticated by a credential only
// a person holds (auth.RequirePerson).
func personCredential(ctx context.Context) bool {
	return auth.RequirePerson(ctx, "use GTM") == nil
}

// begin resolves the caller and checks req's buf.validate rules. A request
// that breaks them is InvalidArgument only for a caller GTM admits; anyone
// else gets the bare NotFound every refusal gets, so the rules of a surface
// the caller cannot see cannot be probed. With no domain service wired, GTM
// is absent: every rpc is that NotFound.
func (s *Service) begin(ctx context.Context, orgID string, req proto.Message) (gtmsvc.Actor, error) {
	a, err := actor(ctx, orgID)
	if err != nil {
		return a, err
	}
	if s.deps.Service == nil {
		return a, svcerr.Wrap(gtmsvc.ErrNotFound)
	}
	if verr := protovalidate.Validate(req); verr != nil {
		return a, s.invalid(ctx, a, svcerr.InvalidArgument(verr.Error()))
	}
	return a, nil
}

// invalid is the answer to a request the handler refuses before the domain
// sees it (its buf.validate rules, a timestamp that is not one): err for a
// caller GTM admits, and for anyone else the bare NotFound every refusal
// gets. Every pre-domain error after begin resolved the caller goes through
// here, so nothing about the surface reaches a caller who cannot see it.
func (s *Service) invalid(ctx context.Context, a gtmsvc.Actor, err error) error {
	if !s.enabled(ctx, a) {
		return svcerr.Wrap(gtmsvc.ErrNotFound)
	}
	return wireErr(err)
}

// enabled is the gate's answer for a: a refusal and an outage are false.
func (s *Service) enabled(ctx context.Context, a gtmsvc.Actor) bool {
	if s.deps.Service == nil {
		return false
	}
	access, err := s.deps.Service.GetAccess(ctx, a)
	return err == nil && access.Enabled
}

// wireErr maps a domain error to the Connect error the client reads, through
// svcerr. svcerr publishes only a constructor's detail, so a typed domain
// error (*ValidationError and the rest) would reach the client as its bare
// category ("invalid argument"); its own text, which the domain writes from
// fixed words and the caller's own ids, is published instead, and only when
// it is what decides the code (never a server-only cause). A revision
// conflict carries the GtmRevisionConflict detail. Everything else is
// svcerr.Wrap's: a recognised sentinel keeps its text, anything else is
// Internal with its text withheld.
func wireErr(err error) error {
	var conflict *gtmsvc.RevisionConflictError
	if errors.As(err, &conflict) && svcerr.Code(err) == svcerr.Code(conflict) {
		return svcerr.WithDetail(svcerr.WithMessage(err, conflict.Error()), &pb.GtmRevisionConflict{
			Id:               conflict.ID,
			ExpectedRevision: conflict.Expected,
			CurrentRevision:  conflict.Current,
		})
	}
	if typed := typedDomainError(err); typed != nil && svcerr.Code(err) == svcerr.Code(typed) {
		return svcerr.Wrap(svcerr.WithMessage(err, typed.Error()))
	}
	return svcerr.Wrap(err)
}

// typedDomainError is the domain's typed error in err's chain whose text is
// written for the caller, or nil.
func typedDomainError(err error) error {
	var (
		validation   *gtmsvc.ValidationError
		personOnly   *gtmsvc.PersonOnlyError
		transition   *gtmsvc.TransitionError
		link         *gtmsvc.LinkError
		inDoubt      *gtmsvc.SendInDoubtError
		inFlight     *gtmsvc.SendInFlightError
		unrecorded   *gtmsvc.SentUnrecordedError
		assetRefused *gtmsvc.AssetRejectedError
	)
	switch {
	case errors.As(err, &validation):
		return validation
	case errors.As(err, &personOnly):
		return personOnly
	case errors.As(err, &transition):
		return transition
	case errors.As(err, &link):
		return link
	case errors.As(err, &inDoubt):
		return inDoubt
	case errors.As(err, &inFlight):
		return inFlight
	case errors.As(err, &unrecorded):
		return unrecorded
	case errors.As(err, &assetRefused):
		return assetRefused
	}
	return nil
}
