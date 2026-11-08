package svcerr

import (
	"path"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"

	"connectrpc.com/connect"
)

// A bounded call stack for server faults.
//
// An error that maps to Internal, Unknown, DataLoss or Unavailable is the
// system failing, and an issue built from it needs frames: the log call site
// alone ("rpc failed", in an interceptor) says nothing about where the failure
// began. The stack is captured where the error is first built or first wrapped
// — a server-fault constructor, or Wrap/ToConnect meeting an error nothing
// classified — and handed to observability through [StackOf]. It is never
// part of the error's text and never reaches a client: it lives in the Go
// error chain, which Connect does not serialize.
//
// Client-class errors (InvalidArgument, NotFound, PermissionDenied, ...) never
// capture one. They are the API working, they are frequent, and nobody pages
// on them.

const (
	// MaxStackFrames bounds the frames of a captured stack. Capture keeps the
	// innermost frames — where the failure happened — and says so when it cut
	// the rest, as Go's own traceback does.
	MaxStackFrames = 32

	// MaxStackBytes bounds the rendered text of a stack, in bytes. Frames are
	// dropped whole, outermost first, so the text is never cut mid-frame.
	MaxStackBytes = 8 << 10

	elidedLine = "...additional frames elided...\n"
)

// Frame is one frame of a [Stack].
type Frame struct {
	// Function is the fully qualified function name, as runtime reports it.
	Function string
	// File is the module-relative path ("internal/handlers/jobs/rpc_list.go")
	// for a frame of the main module, so it reads the same in a -trimpath
	// build and in a developer's checkout.
	File string
	Line int
}

// Stack is a captured stack, rendered.
type Stack struct {
	// Trace is the stack in Go's standard traceback layout, which is what
	// OpenTelemetry's exception.stacktrace carries and what the control
	// plane's issue view parses: a "goroutine N [running]:" header, then per
	// frame "pkg.Func(...)" and "\tfile.go:LINE +0xOFFSET". Runtime frames and
	// this package's own frames are omitted. The goroutine id is not captured
	// and reads 0.
	Trace string

	// Origin is the innermost application frame: the first frame of the main
	// module (any non-standard-library frame when the module is unknown).
	// Issue grouping uses it as the call site, because it is where the failure
	// came from and the log line's own site is usually an interceptor.
	Origin Frame
}

// stackError carries the program counters of the moment an error was built or
// first wrapped. It is invisible everywhere else: Error() is the wrapped
// error's text and Unwrap returns it, so errors.Is/As, Code, Classify, the
// reason header and the redaction rules all see straight through it.
//
// The counters sit inline, so a capture is ONE allocation and no names are
// resolved until [StackOf]: a server fault that is never reported pays only
// for runtime.Callers.
type stackError struct {
	err       error
	n         int
	truncated bool
	// A failing request reports the same stack on its log record, its
	// log.error event and its span: resolve it once.
	once     sync.Once
	rendered Stack
	// One slot beyond MaxStackFrames tells "exactly full" from "cut".
	pcs [MaxStackFrames + 1]uintptr
}

func (e *stackError) Error() string { return e.err.Error() }
func (e *stackError) Unwrap() error { return e.err }

// newStack wraps err with the calling stack. skip counts the frames to omit
// above newStack's caller: 0 starts the stack at that caller, 1 at its caller.
func newStack(err error, skip int) *stackError {
	s := &stackError{err: err}
	// Callers' own skip counts itself (0) and newStack (1).
	n := runtime.Callers(skip+2, s.pcs[:])
	if n > MaxStackFrames {
		n, s.truncated = MaxStackFrames, true
	}
	s.n = n
	return s
}

// faultCode reports whether code is one forge classifies as the server's
// fault and pages on: the codes whose errors carry a stack.
func faultCode(code connect.Code) bool {
	switch code {
	case connect.CodeInternal, connect.CodeUnknown, connect.CodeDataLoss, connect.CodeUnavailable:
		return true
	}
	return false
}

// faulted is the constructor form for the four server-fault kinds: wrapped,
// plus the stack of the code that built it. An empty detail returns the bare
// sentinel, which is a shared value and cannot carry one; Wrap captures at the
// boundary instead.
func faulted(sentinel error, detail string) error {
	err := wrapped(sentinel, detail)
	if err == sentinel {
		return err
	}
	// Skip faulted and the constructor: the stack starts at the application.
	return newStack(err, 2)
}

// withStack gives err a stack at its first wrap, when it is a server fault
// that has none. The caller has already checked the code; skip is newStack's.
func withStack(err error, skip int) error {
	if findStack(err) != nil || Classify(err) != ClassServer {
		return err
	}
	return newStack(err, skip)
}

// unstack returns the error a stackError carries, so [Cause] hands the log the
// error the application built and not our wrapper around it.
func unstack(err error) error {
	if s, ok := err.(*stackError); ok {
		return s.err
	}
	return err
}

// findStack returns the stack captured EARLIEST in err's tree: the innermost,
// and a [WithCause] cause before the error that relabelled it. The failure
// began where the deepest capture is; everything outside it is the same
// failure being passed up.
func findStack(err error) *stackError {
	for err != nil {
		switch e := err.(type) {
		case *redactedError:
			if s := findStack(e.cause); s != nil {
				return s
			}
			err = e.err
			continue
		case *stackError:
			if s := findStack(e.err); s != nil {
				return s
			}
			return e
		}
		switch u := err.(type) {
		case interface{ Unwrap() error }:
			err = u.Unwrap()
		case interface{ Unwrap() []error }:
			for _, inner := range u.Unwrap() {
				if s := findStack(inner); s != nil {
					return s
				}
			}
			return nil
		default:
			return nil
		}
	}
	return nil
}

// StackOf returns the stack to report for err, and whether there is one.
//
// There is one when err is a server fault — the client is told Internal,
// Unknown, DataLoss or Unavailable, and [Classify] says ClassServer — a stack
// was captured for it, and the stack holds at least one application frame. A
// stack of nothing but runtime, net/http and connect-go frames, which is what
// an error first wrapped by [Interceptor] after its handler returned has,
// says no more than the log line does, so it is not reported. The way to get
// the real one is the one the lint rule already asks for: return
// svcerr.Wrap (or WrapOr) from the handler.
//
// The OUTER error decides, as it does for everything a client reads: a
// NotFound that relabels an internal failure has no stack to report, an
// Unavailable marked [WithClass] ClassUser has none, and neither does a drain
// or a cancellation.
//
// It is the operator's view, for a log record or a span: use it to set
// exception.stacktrace, never to build a response. It resolves function names
// and files on first use (and remembers them), so call it for an error being
// reported, not for every error built.
func StackOf(err error) (Stack, bool) {
	if err == nil {
		return Stack{}, false
	}
	s := findStack(err)
	if s == nil || !faultCode(Code(err)) || Classify(err) != ClassServer {
		return Stack{}, false
	}
	s.once.Do(func() { s.rendered = s.render() })
	return s.rendered, s.rendered.Origin.Function != ""
}

// plumbingPrefix is the qualified-name prefix of this package's functions
// ("github.com/reliant-labs/forge/pkg/svcerr."), derived so a vendored or
// renamed copy still filters itself.
var plumbingPrefix = func() string {
	pc, _, _, _ := runtime.Caller(0)
	return packagePath(runtime.FuncForPC(pc).Name()) + "."
}()

func plumbing(function string) bool {
	return strings.HasPrefix(function, "runtime.") || strings.HasPrefix(function, plumbingPrefix)
}

var mainModule = sync.OnceValue(func() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Path != "command-line-arguments" {
		return bi.Main.Path
	}
	return ""
})

// inApp reports whether a function is application code: the main module's,
// or — when the build records none — anything that is not the standard
// library, whose import paths have no dot in their first element.
func inApp(function string) bool {
	if mod := mainModule(); mod != "" {
		return strings.HasPrefix(function, mod+"/") || strings.HasPrefix(function, mod+".") || strings.HasPrefix(function, "main.")
	}
	first, _, _ := strings.Cut(function, "/")
	return strings.Contains(first, ".") && strings.Contains(function, "/")
}

// packagePath is the import path of a qualified function name:
// "a.com/m/pkg.(*T).Method" is "a.com/m/pkg".
func packagePath(function string) string {
	slash := strings.LastIndex(function, "/")
	dot := strings.Index(function[slash+1:], ".")
	if dot < 0 {
		return function
	}
	return function[:slash+1+dot]
}

// relFile makes a frame's file path stable across builds. A main-module frame
// is module-relative whether or not the binary was built with -trimpath
// (which writes "<module>/<dir>/<file>"; without it the path is absolute and
// the function's package path, the module path plus the directory, supplies
// the directory). Other frames keep the form -trimpath gives them, with a
// module-cache or GOROOT prefix cut where the build did not trim it.
func relFile(f runtime.Frame) string {
	file := f.File
	mod := mainModule()
	if mod != "" && inApp(f.Function) {
		if rest, ok := strings.CutPrefix(file, mod+"/"); ok {
			return rest
		}
		if strings.HasPrefix(f.Function, "main.") {
			return path.Base(file) // package main: its directory is not in the function name
		}
		dir := strings.TrimSuffix(packagePath(f.Function), "_test") // an external test package sits beside its subject
		return path.Join(strings.TrimPrefix(strings.TrimPrefix(dir, mod), "/"), path.Base(file))
	}
	if i := strings.Index(file, "/pkg/mod/"); i >= 0 {
		return file[i+len("/pkg/mod/"):]
	}
	pkg := packagePath(f.Function)
	if first, _, _ := strings.Cut(pkg, "/"); !strings.Contains(first, ".") { // standard library
		if i := strings.LastIndex(file, "/"+pkg+"/"); i >= 0 {
			return file[i+1:]
		}
	}
	return file
}

// render resolves the counters into text. Frames beyond MaxStackFrames or
// MaxStackBytes are dropped from the OUTER end.
func (s *stackError) render() Stack {
	var (
		out       Stack
		kept      int
		truncated = s.truncated
		b         = make([]byte, 0, 1<<10)
		line      []byte
	)
	b = append(b, "goroutine 0 [running]:\n"...)

	frames := runtime.CallersFrames(s.pcs[:s.n])
	for {
		f, more := frames.Next()
		if f.Function != "" && !plumbing(f.Function) {
			file := relFile(f)
			line = append(line[:0], f.Function...)
			line = append(line, "(...)\n\t"...)
			line = append(line, file...)
			line = append(line, ':')
			line = strconv.AppendInt(line, int64(f.Line), 10)
			line = append(line, " +0x"...)
			line = strconv.AppendUint(line, uint64(f.PC-f.Entry), 16)
			line = append(line, '\n')
			if kept == MaxStackFrames || len(b)+len(line)+len(elidedLine) > MaxStackBytes {
				truncated = true
				break
			}
			b = append(b, line...)
			kept++
			if out.Origin.Function == "" && inApp(f.Function) {
				out.Origin = Frame{Function: f.Function, File: file, Line: f.Line}
			}
		}
		if !more {
			break
		}
	}
	if truncated {
		b = append(b, elidedLine...)
	}
	out.Trace = string(b)
	return out
}
