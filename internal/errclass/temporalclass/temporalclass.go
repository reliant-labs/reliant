// Copyright (c) 2025 Reliant Labs

// Package temporalclass is errclass's Temporal half: the rule that reads a
// Temporal error's verdict, and the category an activity error carries across
// the activity boundary.
//
// Importing it registers the rule with errclass (init). Every package that
// produces or consumes Temporal errors imports it, directly or through a
// package that does, so a binary that runs Temporal code always classifies
// them; errclass itself stays free of the Temporal SDK.
//
//forge:exclude-contract: error classification over Temporal's error types and benign category; registers one errclass rule from init, no I/O, state or time
package temporalclass

import (
	"errors"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/errclass"
)

func init() { errclass.Register(Rule) }

// Rule is the Temporal verdict: a benign ApplicationError is a user error (the
// activity boundary marks one that way, since no Go type or svcerr marker
// survives it) and a CanceledError is canceled.
func Rule(err error) svcerr.Class {
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) && appErr.Category() == temporal.ApplicationErrorCategoryBenign {
		return svcerr.ClassUser
	}
	var canceled *temporal.CanceledError
	if errors.As(err, &canceled) {
		return svcerr.ClassCanceled
	}
	return svcerr.ClassNone
}

// Category is the ApplicationError category an activity error should carry
// across the activity boundary: Benign for an error that is not a server
// fault, so the workflow side — and Temporal — still know nobody has to act.
func Category(err error) temporal.ApplicationErrorCategory {
	switch errclass.Classify(err) {
	case svcerr.ClassUser, svcerr.ClassCanceled:
		return temporal.ApplicationErrorCategoryBenign
	default:
		return temporal.ApplicationErrorCategoryUnspecified
	}
}
