// Copyright (c) 2025 Reliant Labs
package logging

import (
	"context"
	"log/slog"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Keys forge's error_inputs (observe.addErrorInputs) writes the call site
// under, and sfp1 (obsquery.ServerFingerprint) reads. They are OTel semconv's.
const (
	attrCodeFunction = "code.function.name"
	attrCodeFile     = "code.file.path"
	attrCodeLine     = "code.line.number"
)

// callSiteHandler stamps the call site on an ERROR record, for forge's
// error_inputs handler behind it, which would otherwise name the record's PC.
//
// A PC is the call site only for a front end that adds no frame of its own.
// This package's Error, the Temporal loggers and the like each record
// themselves, so every logging.Error in the process would carry
// code.function.name "logging.Error" and sfp1 would merge every one of them
// into a single issue. The site is resolved by callerFrames (callsite.go):
// outward from the PC past the logger wrappers.
//
// A server fault that carries a svcerr stack is the exception: its origin
// frame, where the error was first built or wrapped, is a better grouping key
// than any logging site (distinct errors logged through one wrapper stay
// distinct), and forge uses it when the record has no code.* of its own. So
// the stamp is skipped for such a record. Only an error forge itself would
// find (the first error-valued attribute of the record) counts.
//
// The attributes are the ones forge writes; forge leaves them alone when a
// record already has them and still adds exception.type and
// exception.message. File paths are as the binary recorded them: module-
// relative in a -trimpath build, which every prod image and release is.
type callSiteHandler struct{ next slog.Handler }

func newCallSiteHandler(next slog.Handler) slog.Handler { return callSiteHandler{next: next} }

func (h callSiteHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h callSiteHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError && !recordHasAttr(r, attrCodeFunction) && !hasServerFaultStack(r) {
		if frames := callerFrames(r.PC); len(frames) > 0 && frames[0].Function != "" {
			site := frames[0]
			// The record's attribute storage may be shared with the caller's.
			r = r.Clone()
			r.AddAttrs(
				slog.String(attrCodeFunction, site.Function),
				slog.String(attrCodeFile, site.File),
				slog.Int(attrCodeLine, site.Line),
			)
		}
	}
	return h.next.Handle(ctx, r)
}

func (h callSiteHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return callSiteHandler{next: h.next.WithAttrs(attrs)}
}

func (h callSiteHandler) WithGroup(name string) slog.Handler {
	return callSiteHandler{next: h.next.WithGroup(name)}
}

func recordHasAttr(r slog.Record, key string) bool {
	found := false
	r.Attrs(func(a slog.Attr) bool {
		found = a.Key == key
		return !found
	})
	return found
}

// hasServerFaultStack reports whether forge will take the call site from the
// record's error: the first error-valued attribute has a svcerr stack. It
// mirrors observe.addErrorInputs, which looks no further.
func hasServerFaultStack(r slog.Record) bool {
	var err error
	r.Attrs(func(a slog.Attr) bool {
		if e, ok := a.Value.Resolve().Any().(error); ok && e != nil {
			err = e
			return false
		}
		return true
	})
	if err == nil {
		return false
	}
	_, ok := svcerr.StackOf(err)
	return ok
}
