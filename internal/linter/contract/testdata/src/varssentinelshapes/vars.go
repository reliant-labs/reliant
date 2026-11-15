// Package varssentinelshapes is every error-sentinel shape forge's own
// skills teach. Each one is an idiomatic sentinel, so none may be flagged
// — the rule's exception is about the TYPE being an error, not about which
// function produced the value.
package varssentinelshapes

import (
	stderrors "errors"
	"fmt"

	"varssentinelshapes/svcerr"
)

// The two shapes the old callee whitelist knew.
var ErrPlain = stderrors.New("plain")
var ErrFormatted = fmt.Errorf("formatted")

// Built from a forge/pkg/svcerr constructor — what `service-layer` and
// `api` tell an author to write instead of redeclaring the sentinel set.
var ErrSeatCap = svcerr.ResourceExhausted("seat cap")

// An alias of a shared sentinel: not a call at all.
var ErrMissing = svcerr.ErrNotFound

// errors.Join, and an explicitly-typed declaration.
var ErrJoined = stderrors.Join(ErrPlain, ErrFormatted)
var ErrDeclared error = fmt.Errorf("declared")

// A typed struct sentinel.
type validationError struct{ field string }

func (e validationError) Error() string { return "invalid " + e.field }
func (e validationError) Unwrap() error { return ErrPlain }

var ErrTypedSentinel = validationError{field: "name"}

// The exception must stay narrow: Err-named but NOT an error.
var ErrNotAnError = "boom" // want `exported package variable ErrNotAnError should be a method on a struct or a getter function`
