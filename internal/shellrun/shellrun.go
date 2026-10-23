// Package shellrun runs a POSIX shell script in-process (mvdan.cc/sh), so the
// same script behaves identically on every OS — including Windows, where no
// `sh` binary is on PATH. External programs the script invokes (npm, go,
// docker, ...) are resolved by the interpreter's default exec handler, which
// uses LookPathDir: PATH search plus PATHEXT on Windows.
package shellrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// Options configures one Run.
type Options struct {
	// Dir is the working directory; empty inherits the process cwd.
	Dir string
	// Env overlays the process environment; overlay keys win.
	Env map[string]string
	// Stdout/Stderr default to io.Discard when nil.
	Stdout, Stderr io.Writer
	// Commands maps command names to in-process implementations that take
	// precedence over PATH lookup (e.g. a portable grep).
	Commands map[string]CommandFunc
}

// CommandFunc is an in-process command. args excludes the command name.
// Return an interp.ExitStatus-compatible error via ExitCode(n).
type CommandFunc func(ctx context.Context, dir string, args []string, stdin io.Reader, stdout, stderr io.Writer) int

// ExitError is returned when the script exits non-zero.
type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// Run parses and executes script. A non-zero exit yields an ExitError;
// context cancellation yields the context's error.
func Run(ctx context.Context, script string, opts Options) error {
	file, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil {
		return fmt.Errorf("parse shell script: %w", err)
	}
	environ := os.Environ()
	for k, v := range opts.Env {
		environ = append(environ, k+"="+v)
	}
	stdout, stderr := opts.Stdout, opts.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	ropts := []interp.RunnerOption{
		interp.Env(expand.ListEnviron(environ...)),
		interp.StdIO(nil, stdout, stderr),
	}
	if opts.Dir != "" {
		ropts = append(ropts, interp.Dir(opts.Dir))
	}
	if len(opts.Commands) > 0 {
		ropts = append(ropts, interp.ExecHandlers(func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return func(ctx context.Context, args []string) error {
				fn, ok := opts.Commands[args[0]]
				if !ok {
					return next(ctx, args)
				}
				hc := interp.HandlerCtx(ctx)
				if code := fn(ctx, hc.Dir, args[1:], hc.Stdin, hc.Stdout, hc.Stderr); code != 0 {
					return interp.ExitStatus(uint8(code))
				}
				return nil
			}
		}))
	}
	runner, err := interp.New(ropts...)
	if err != nil {
		return fmt.Errorf("init shell interpreter: %w", err)
	}
	runErr := runner.Run(ctx, file)
	var status interp.ExitStatus
	switch {
	case runErr == nil:
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.As(runErr, &status):
		return ExitError{Code: int(status)}
	}
	return runErr
}

// PortableCommands returns in-process grep and rg (minimal)
// implementations for scripts that must run where those tools are absent.
// Supported: -q -i -F -E -r -e PATTERN, then FILE... (directories need -r).
func PortableCommands() map[string]CommandFunc {
	return map[string]CommandFunc{
		"grep": portableGrep,
		// rg searches directories recursively by default.
		"rg": func(ctx context.Context, dir string, args []string, in io.Reader, out, errw io.Writer) int {
			return portableGrep(ctx, dir, append([]string{"-r"}, args...), in, out, errw)
		},
	}
}

func portableGrep(_ context.Context, dir string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var quiet, fold, fixed, recursive bool
	var patterns []string
	var filter treeFilter
	i := 0
	for ; i < len(args) && strings.HasPrefix(args[i], "-") && args[i] != "-"; i++ {
		// GNU grep's file-selection flags, which detection scripts use to
		// keep a recursive search to Go sources and out of gen/ and vendor/.
		// Unsupported, they made the whole command fail (exit 2) — and a
		// detection that fails reads as "does not apply", silently.
		if name, value, ok := strings.Cut(args[i], "="); ok {
			value = strings.Trim(value, `'"`)
			switch name {
			case "--include":
				filter.include = append(filter.include, value)
				continue
			case "--exclude":
				filter.exclude = append(filter.exclude, value)
				continue
			case "--exclude-dir":
				filter.excludeDir = append(filter.excludeDir, value)
				continue
			}
		}
		switch a := args[i]; a {
		case "--":
			i++
			goto done
		case "-e":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "grep: -e needs a pattern")
				return 2
			}
			i++
			patterns = append(patterns, args[i])
		default:
			for _, c := range a[1:] {
				switch c {
				case 'q':
					quiet = true
				case 'i':
					fold = true
				case 'F':
					fixed = true
				case 'E', 'n', 's':
				case 'r', 'R':
					recursive = true
				default:
					fmt.Fprintf(stderr, "grep: unsupported flag -%c\n", c)
					return 2
				}
			}
		}
	}
done:
	if len(patterns) == 0 {
		if i >= len(args) {
			fmt.Fprintln(stderr, "grep: missing pattern")
			return 2
		}
		patterns = append(patterns, args[i])
		i++
	}
	exprs := make([]string, len(patterns))
	for n, p := range patterns {
		if fixed {
			p = regexp.QuoteMeta(p)
		}
		exprs[n] = p
	}
	expr := strings.Join(exprs, "|")
	if fold {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile("(?m)" + expr)
	if err != nil {
		fmt.Fprintf(stderr, "grep: bad pattern: %v\n", err)
		return 2
	}
	files := args[i:]
	matched := false
	scan := func(r io.Reader, name string) {
		data, _ := io.ReadAll(r)
		for _, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				matched = true
				if !quiet {
					fmt.Fprintf(stdout, "%s:%s\n", name, line)
				}
			}
		}
	}
	if len(files) == 0 {
		scan(stdin, "(stdin)")
	}
	for _, f := range files {
		p := f
		if !strings.HasPrefix(p, "/") && !(len(p) > 1 && p[1] == ':') && dir != "" {
			p = dir + string(os.PathSeparator) + p
		}
		if st, serr := os.Stat(p); serr != nil {
			fmt.Fprintf(stderr, "grep: %s: %v\n", f, serr)
			return 2
		} else if st.IsDir() {
			if !recursive {
				fmt.Fprintf(stderr, "grep: %s: is a directory\n", f)
				return 2
			}
			grepTree(p, filter, scan)
			continue
		}
		fh, oerr := os.Open(p)
		if oerr != nil {
			fmt.Fprintf(stderr, "grep: %s: %v\n", f, oerr)
			return 2
		}
		scan(fh, f)
		_ = fh.Close()
	}
	if matched {
		return 0
	}
	return 1
}
