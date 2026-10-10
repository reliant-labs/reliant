// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/errclass"
)

// flattenForTemporal hands Temporal ONE failure for an activity error, with the
// causal chain rendered once in its message.
//
// Temporal's failure converter records a failure per Go wrap layer, each
// carrying that layer's full text, and ApplicationError.Error() prints every
// layer followed by its cause. A four-layer CallLLM error therefore reached
// chat 622675c2's pause row with its root cause four times over. The workflow
// never needs the layers: Go types do not survive serialization, and what it
// branches on — the ApplicationError's type, NonRetryable, NextRetryDelay —
// is taken from the first ApplicationError in the chain and put on the top
// failure. That also fixes a latent hole: an ApplicationError wrapped by a
// plain fmt.Errorf used to leave the top failure retryable whatever it said.
//
// Three shapes pass through untouched, because the SDK or the workflow keys on
// them as they are:
//   - cancellation (a CanceledError or context.Canceled anywhere in the
//     chain): the SDK reports it as a cancel only if it can still find it, and
//     pause and interrupt depend on that;
//   - activity.ErrResultPending, compared by identity;
//   - an ApplicationError carrying details, which cannot be re-encoded here.
func flattenForTemporal(err error) error {
	if err == nil || errors.Is(err, activity.ErrResultPending) {
		return err
	}
	var canceled *temporal.CanceledError
	if errors.As(err, &canceled) || errors.Is(err, context.Canceled) {
		return err
	}

	// A user error's Go type and svcerr marker do not survive serialization;
	// its category does. Benign tells the workflow side (errclass) and
	// Temporal itself that nobody has to act on it.
	category := errclass.TemporalCategory(err)

	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) {
		return temporal.NewApplicationErrorWithOptions(flatMessage(err), temporalErrType(err), temporal.ApplicationErrorOptions{
			Category: category,
		})
	}
	if appErr.HasDetails() {
		return err
	}
	if appErr.Category() != temporal.ApplicationErrorCategoryUnspecified {
		category = appErr.Category()
	}
	return temporal.NewApplicationErrorWithOptions(flatMessage(err), appErr.Type(), temporal.ApplicationErrorOptions{
		NonRetryable:   appErr.NonRetryable(),
		NextRetryDelay: appErr.NextRetryDelay(),
		Category:       category,
	})
}

// flatMessage renders err's causal chain once: each layer's own words, joined
// by ": ".
//
// A wrap layer's text is its own prefix followed by its cause's text, so the
// prefix is what remains once the cause is cut off the end. An
// ApplicationError contributes its message without the "(type: ..., retryable:
// ...)" its Error() appends, and a layer whose text already spells out its
// whole cause (classifyError's NewNonRetryableApplicationError(err.Error(), ...,
// err)) ends the walk instead of repeating it.
func flatMessage(err error) string {
	var parts []string
	for layer := err; layer != nil; {
		next := errors.Unwrap(layer)
		own := layer.Error()
		var appErr *temporal.ApplicationError
		if errors.As(layer, &appErr) && appErr == layer {
			own = appErr.Message()
		}
		if next != nil {
			rest := next.Error()
			if prefix, ok := strings.CutSuffix(own, rest); ok {
				own = strings.TrimRight(prefix, " :;,-")
			} else if strings.Contains(own, rest) {
				next = nil
			}
		}
		if own != "" {
			parts = append(parts, own)
		}
		layer = next
	}
	return strings.Join(parts, ": ")
}

// temporalErrType names a plain Go error the way Temporal's converter does
// (the dereferenced type name, empty for errors.New), so flattening does not
// rename any failure type.
func temporalErrType(err error) string {
	t := reflect.TypeOf(err)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == errorsNewType {
		return ""
	}
	return t.Name()
}

var errorsNewType = reflect.TypeOf(errors.New("")).Elem()
