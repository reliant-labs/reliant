// Copyright (c) 2025 Reliant Labs
package logging

import (
	"reflect"
	"runtime"
	"strings"
)

// Where a record was logged from: the frame callSiteHandler stamps as the
// record's code.function.name, so an issue names the function that logged
// rather than this package's Error or a Temporal logger wrapper.

// maxLogStackDepth bounds the stack walked to find a call site.
const maxLogStackDepth = 64

// slogFramePrefix marks slog's own frames. For a record with no PC, the
// frames below the outermost slog frame are taken to be handler machinery.
const slogFramePrefix = "log/slog."

// loggerWrapperPrefixes are the loggers that adapt into slog from the call
// site's side: this package's Error/Warn/... and the Temporal loggers. A
// record's PC can name one of them — every logging.Error call records
// logging.Error as its caller, and a workflow logger can record the SDK's
// replay wrapper — so the call site is the first frame from the PC outward
// that is not one of these.
var loggerWrapperPrefixes = []string{
	reflect.TypeOf(callSiteHandler{}).PkgPath() + ".",
	"go.temporal.io/sdk/log.",
	"go.temporal.io/sdk/internal/log.",
}

// callerFrames is the stack of the code that logged, innermost first, so
// frames[0] is the call site. pc is the record's.
//
// The record's PC is the anchor. Every front end records it outside the
// handler machinery: slog's Logger records the caller of Logger.Error; the
// Temporal SDK's adapter records the caller of its Error, building the record
// itself and calling the handler directly, so no slog frame is on the stack
// at all; this package's Error records logging.Error. So the call site is the
// frame the PC names, moved outward past any logger wrapper, and every handler
// between there and here — this one, the metrics bridge, forge's error-class
// policy, whatever wraps them next — is passed over without being named.
//
// The call site used to be found as the first frame past slog's. On a
// Temporal-logged record there is none, so forge's errorClassHandler.Handle,
// the outermost handler, was named the culprit of every ERROR the workflow
// and activity loggers wrote (ELECTRON-CM, CK, CE). That rule survives only
// for a record that carries no PC.
func callerFrames(pc uintptr) []runtime.Frame {
	var pcs [maxLogStackDepth]uintptr
	n := runtime.Callers(1, pcs[:])
	iter := runtime.CallersFrames(pcs[:n])
	var stack []runtime.Frame
	for {
		frame, more := iter.Next()
		stack = append(stack, frame)
		if !more {
			break
		}
	}

	anchor, ok := frameAt(pc)
	if !ok {
		return pastLoggerWrappers(stack[pastSlog(stack):])
	}
	for i, frame := range stack {
		if sameLocation(frame, anchor) {
			return pastLoggerWrappers(stack[i:])
		}
	}
	// The record was built on another goroutine and handed to this one —
	// a handler that queues records does that — so this stack says nothing
	// about who logged. The PC still does.
	return []runtime.Frame{anchor}
}

// frameAt is the frame pc names: when the call was inlined, the innermost
// function, which is the frame runtime.Callers gave that PC.
func frameAt(pc uintptr) (runtime.Frame, bool) {
	if pc == 0 {
		return runtime.Frame{}, false
	}
	frame, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	return frame, frame.Function != ""
}

// sameLocation compares frames by where they are, not by PC: a front end that
// records the PC of the call instruction (runtime.Caller's) rather than the
// return address (runtime.Callers') still names the same line.
func sameLocation(a, b runtime.Frame) bool {
	return a.Function == b.Function && a.File == b.File && a.Line == b.Line
}

// pastSlog is the index of the first frame past the first run of slog
// frames, or 0 when slog is not on the stack.
func pastSlog(stack []runtime.Frame) int {
	inSlog := false
	for i, frame := range stack {
		if strings.HasPrefix(frame.Function, slogFramePrefix) {
			inSlog = true
			continue
		}
		if inSlog {
			return i
		}
	}
	return 0
}

// pastLoggerWrappers drops the leading frames that are loggers adapting into
// slog rather than the code that logged.
func pastLoggerWrappers(stack []runtime.Frame) []runtime.Frame {
	start := 0
	for start < len(stack) && isLoggerWrapperFrame(stack[start]) {
		start++
	}
	return stack[start:]
}

// isLoggerWrapperFrame reports whether frame is a logger adapting into slog
// (or, for a record with no PC, this package's own machinery) rather than the
// code that logged. A test file is never a wrapper: a test in this package
// that logs is the call site.
func isLoggerWrapperFrame(frame runtime.Frame) bool {
	if strings.HasSuffix(frame.File, "_test.go") {
		return false
	}
	for _, prefix := range loggerWrapperPrefixes {
		if strings.HasPrefix(frame.Function, prefix) {
			return true
		}
	}
	return false
}
