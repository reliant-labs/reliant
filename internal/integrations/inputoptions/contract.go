// Copyright (c) 2025 Reliant Labs

// Package inputoptions lists the values a picker can offer for an
// integration-defined input type (`github.repository`), and says whether
// stored values can still be reached. It knows nothing about any integration:
// a type's manifest declares a read-only list action, templates for that
// action's params, and CEL projections from each item of its output to an
// option, and this package runs that declaration.
//
// A listing is the CALLER's, never a run's. It runs the type's list action
// with the credential connauth resolves for the authenticated user (the
// connection they named, else their default, else the integration's delegated
// authority), so the options are exactly what that person can see on the
// provider. A read keeps the user's own access whoever the automation will
// later act as: the acting identity only feeds the type's warnings.
//
// A short TTL cache, per user, connection, type and scope, keeps typing in a
// search box off the provider's rate limit. It holds the action's raw items,
// so a different acting identity or page projects from the same call.
//
//forge:not-a-component: built by reliant's hand-wired API server (internal/serverapi), not forge's composition root — until P3c (research/forge-migration/PLAN.md)
package inputoptions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

// Service lists and resolves the values of integration-defined types.
type Service interface {
	// List returns one page of the options of req.Type for req.UserID.
	//
	// A type whose action returns the whole list is paged here (an offset
	// token over the cached list); one that pages itself (options.paging)
	// returns the upstream page and a token for the next. A token is only
	// valid for the type, connection, scope and query it was issued for.
	// A type searched by an action param (options.search: param:<name>) lists
	// nothing until there is a query.
	//
	// A caller with no usable credential gets StateNeedsConnection and no
	// options, not an error.
	List(ctx context.Context, req ListRequest) (*ListResult, error)

	// Resolve labels stored values, and says which no longer resolve. It
	// asks the type's `resolve` action once per value when it has one, and
	// otherwise looks the values up in the type's options.
	Resolve(ctx context.Context, req ResolveRequest) (*Resolution, error)
}

// ListRequest is one page of options.
type ListRequest struct {
	// UserID is the authenticated caller, never read from a request body.
	UserID string
	// Type is the qualified type ref, "github.repository".
	Type string
	// ConnectionID names the connection to list through; empty is the
	// caller's default, else the integration's delegated authority.
	ConnectionID string
	// Scope holds the values of the type's declared scope keys. Keys the type
	// does not declare are ignored; an empty value is unset.
	Scope map[string]any
	// Query narrows the options: client-side over what the projection
	// produced, or sent to the action as the param the type names.
	Query string
	// PageToken is a previous result's NextPageToken.
	PageToken string
	// PageSize defaults to DefaultPageSize and is capped at MaxPageSize.
	PageSize int
	// ActingIdentity is "owner" (the default) or "app"; it only feeds the
	// type's warnings.
	ActingIdentity string
}

// ResolveRequest labels stored values.
type ResolveRequest struct {
	UserID         string
	Type           string
	ConnectionID   string
	Scope          map[string]any
	Values         []string
	ActingIdentity string
}

// State says whether the caller could list at all.
type State int

const (
	// StateUnknown: nothing was asked of the provider (a searched type with
	// no query yet), so nothing is known about the connection.
	StateUnknown State = iota
	// StateConnected: the options came from a usable credential.
	StateConnected
	// StateNeedsConnection: the caller has no usable credential for the
	// integration. The result holds no options.
	StateNeedsConnection
)

// ListResult is one page of options.
type ListResult struct {
	Options []Option
	// NextPageToken is empty on the last page.
	NextPageToken string
	State         State
}

// Option is one value a picker offers.
type Option struct {
	// Value is what is stored in the input.
	Value       string
	Label       string
	Description string
	Group       string
	// Annotations are the facts the type's projection extracted, as plain
	// JSON values.
	Annotations map[string]any
	// Warnings are the messages of every type warning that holds.
	Warnings []string
}

// Resolution is the outcome of Resolve.
type Resolution struct {
	State State
	// Options holds one option for each value that still resolves, in the
	// order the values were asked.
	Options []Option
	// Unreachable are values the provider definitely cannot reach for this
	// caller now: gone, or not visible to them.
	Unreachable []string
	// Unlisted are values that are not among the options but are not shown
	// unreachable either: the list may have been cut short (a very large
	// account), the type accepts free text (a label that does not exist yet),
	// or the type can only be searched and has no lookup.
	Unlisted []string
}

const (
	// DefaultPageSize is the page size of a request that names none.
	DefaultPageSize = 50
	// MaxPageSize caps PageSize.
	MaxPageSize = 200
	// MaxResolveValues caps one Resolve.
	MaxResolveValues = 100
)

// Typed failures. A caller maps them onto its own wire codes.
var (
	// ErrUnknownType: no integration declares the type.
	ErrUnknownType = errors.New("inputoptions: unknown type")
	// ErrNoOptions: the type declares no option list (free entry only).
	ErrNoOptions = errors.New("inputoptions: type has no options")
	// ErrBadScope: a required scope value is missing or malformed.
	ErrBadScope = errors.New("inputoptions: invalid scope")
	// ErrBadPageToken: the token is malformed or belongs to another listing.
	ErrBadPageToken = errors.New("inputoptions: invalid page token")
	// ErrBadRequest: any other malformed request.
	ErrBadRequest = errors.New("inputoptions: invalid request")
	// ErrInvalidType: the type's own declaration cannot be run (its list
	// action is missing, mutates, or is placed on a daemon). The manifest
	// loader refuses these, so it is a defect, not a request error.
	ErrInvalidType = errors.New("inputoptions: invalid type declaration")
	// ErrConnectionNotFound: the connection the request names does not exist
	// for this caller. Another user's connection reads exactly like a missing
	// one, so a caller cannot probe for ids.
	ErrConnectionNotFound = errors.New("inputoptions: no such connection")
)

// UpstreamError is the provider (or the action's declaration) failing to
// produce a list. Message is safe to show a user.
type UpstreamError struct {
	Message string
	// Retryable is whether trying again can help (a rate limit, a 5xx).
	Retryable  bool
	StatusCode int
}

func (e *UpstreamError) Error() string { return e.Message }

// detailError is a typed failure with a message for the person.
type detailError struct {
	kind error
	msg  string
}

func (e *detailError) Error() string { return fmt.Sprintf("%v: %s", e.kind, e.msg) }
func (e *detailError) Unwrap() error { return e.kind }

// Message is the part of err a person should read.
func Message(err error) string {
	var d *detailError
	if errors.As(err, &d) {
		return d.msg
	}
	return err.Error()
}

// TypeSource finds a declared type by its qualified ref, with the manifest it
// belongs to (the runner needs the manifest's connection and actions).
type TypeSource interface {
	Lookup(ref string) (*reliantv1.IntegrationManifest, *reliantv1.TypeSpec, bool)
}

// ActionRunner runs a manifest action with a credential source.
// *httpaction.Runner satisfies it.
type ActionRunner interface {
	RunAuthenticated(ctx context.Context, integration *reliantv1.IntegrationManifest, action *reliantv1.ActionSpec, params map[string]any, src httpaction.CredentialSource, site httpaction.CallSite) (*httpaction.Result, error)
}

// UserCredentials is the credential source for a user's own reads.
// connauth.Service satisfies it.
type UserCredentials interface {
	ForUser(userID string) httpaction.CredentialSource
}

// Deps are the service's collaborators.
type Deps struct {
	// Types finds a declared type by its ref. catalogindex.Index satisfies it.
	Types TypeSource
	// Runner runs the type's list and lookup actions. *httpaction.Runner
	// satisfies it.
	Runner ActionRunner
	// Credentials authenticates them as the caller. connauth.Service
	// satisfies it.
	Credentials UserCredentials
	// TTL is how long a listing is reused. Zero is DefaultTTL; negative
	// disables the cache.
	TTL time.Duration
	// Now is the clock. nil is time.Now.
	Now func() time.Time
	// Logger receives projections that failed for one item. nil discards.
	Logger *slog.Logger
}

// DefaultTTL keeps typing in a search box off the provider's rate limit
// without showing a list much staler than the page it was read from.
const DefaultTTL = 60 * time.Second
