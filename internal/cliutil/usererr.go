// Package cliutil holds small helpers shared across forge's CLI surface.
//
// The package's first inhabitant is the user-facing error helper. Forge
// errors that bubble up to the CLI boundary follow a consistent shape so
// users (humans + LLM agents) always know:
//
//  1. Which subcommand surfaced the error (the context).
//  2. What concretely failed (the message).
//  3. Optionally, where in the source tree the failure points (file:line).
//  4. A one-line suggestion for the next action (the Fix clause).
//
// The shape:
//
//	<context>: <what failed> (at <file:line>). Fix: <one-line suggestion>.
//
// `at <file:line>` and `Fix: ...` are both optional; UserErr drops the
// clause entirely when its argument is empty rather than padding the
// message with `at unknown` or `Fix: see docs`. Internal errors (helper
// → helper) are NOT expected to use this helper — the wrapper's job is
// only to format the final user-visible boundary.
package cliutil

import (
	"errors"
	"fmt"
	"strings"
)

// UserErr formats a user-facing CLI error as:
//
//	<context>: <what> (at <at>). Fix: <fix>.
//
// Both `at` and `fix` are optional; pass "" to omit the corresponding
// clause cleanly. `context` and `what` are required.
//
// Example:
//
//	return cliutil.UserErr("forge pack add api-key",
//	    "pack 'api-key' depends on 'audit-log' which is not installed",
//	    "",
//	    "run 'forge pack add audit-log api-key' (auto-installs in topological order)",
//	)
func UserErr(context, what, at, fix string) error {
	return errors.New(formatUserErr(context, what, at, fix))
}

// UserErrf is the printf-style sibling of UserErr — `what` is rendered
// with fmt.Sprintf(format, args...). Use this when the message needs
// dynamic interpolation (e.g. the failing component name); use UserErr when
// the message is a literal string.
func UserErrf(context, format string, args ...any) error {
	return errors.New(formatUserErr(context, fmt.Sprintf(format, args...), "", ""))
}

// WrapUserErr wraps an underlying error with the standard user-facing
// shape, preserving `errors.Is` / `errors.As` semantics on the inner
// error. Use this when the inner error already has a meaningful message
// (e.g. an io.ErrClosedPipe, or a forge/pkg/* sentinel) and you only
// need to add the context/fix wrapping.
//
// The result reads as:
//
//	<context>: <what>: <inner>. Fix: <fix>.
//
// `at` and `fix` may be empty — the corresponding clause drops cleanly.
func WrapUserErr(context, what, at, fix string, inner error) error {
	if inner == nil {
		return UserErr(context, what, at, fix)
	}
	// Build the prefix without a trailing period so %w can compose.
	var head strings.Builder
	head.WriteString(context)
	head.WriteString(": ")
	if what != "" {
		head.WriteString(what)
		head.WriteString(": ")
	}
	tail := ""
	if at != "" {
		tail += " (at " + at + ")"
	}
	if fix != "" {
		tail += ". Fix: " + fix
	}
	if tail != "" && !strings.HasSuffix(tail, ".") {
		tail += "."
	}
	return fmt.Errorf("%s%w%s", head.String(), inner, tail)
}

// formatUserErr is the shared assembler for UserErr / UserErrf. Kept as
// a private helper so the public API remains a single forward shape.
func formatUserErr(context, what, at, fix string) string {
	var b strings.Builder
	b.WriteString(context)
	b.WriteString(": ")
	b.WriteString(what)
	if at != "" {
		b.WriteString(" (at ")
		b.WriteString(at)
		b.WriteString(")")
	}
	if fix != "" {
		b.WriteString(". Fix: ")
		b.WriteString(fix)
	}
	// Always end with a period so the shape is uniform whether or not a
	// Fix clause was supplied.
	if !strings.HasSuffix(b.String(), ".") {
		b.WriteString(".")
	}
	return b.String()
}
