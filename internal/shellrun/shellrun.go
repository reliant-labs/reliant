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
// Supported: -q -i -s -n -F -E -r -e PATTERN, --include/--exclude/
// --exclude-dir, then FILE... (directories need -r). Without -E or -F a
// grep pattern is a POSIX basic regular expression, as GNU grep reads it.
func PortableCommands() map[string]CommandFunc {
	return map[string]CommandFunc{
		"grep": portableGrep,
		// rg searches directories recursively by default, and its patterns
		// are extended syntax.
		"rg": func(ctx context.Context, dir string, args []string, in io.Reader, out, errw io.Writer) int {
			return portableGrep(ctx, dir, append([]string{"-r", "-E"}, args...), in, out, errw)
		},
	}
}

// basicToRE2 rewrites a POSIX basic regular expression (grep without -E)
// into RE2 syntax. In a BRE `( ) { } | ? +` are literal characters and their
// backslashed forms are the operators — the reverse of RE2 — so a detection
// written for GNU grep, `grep -q 'connect\.NewError('`, must not be compiled
// as-is: RE2 rejects the unbalanced paren, and a detection that fails reads
// as "does not apply", silently. Bracket expressions pass through untouched.
func basicToRE2(bre string) string {
	var b strings.Builder
	inBracket := false
	for i := 0; i < len(bre); i++ {
		c := bre[i]
		if inBracket {
			b.WriteByte(c)
			// `]` closes the bracket unless it is the first member (`[]a]`, `[^]a]`).
			if c == ']' && !(bre[i-1] == '[' || (bre[i-1] == '^' && i >= 2 && bre[i-2] == '[')) {
				inBracket = false
			}
			continue
		}
		switch {
		case c == '[':
			inBracket = true
			b.WriteByte(c)
		case c == '\\' && i+1 < len(bre):
			i++
			if strings.IndexByte("(){}|?+", bre[i]) >= 0 {
				b.WriteByte(bre[i]) // \( is the BRE group operator
			} else {
				b.WriteByte('\\')
				b.WriteByte(bre[i])
			}
		case strings.IndexByte("(){}|?+", c) >= 0:
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// grepOpts is a parsed grep command line.
type grepOpts struct {
	quiet, fold, fixed, extended, recursive bool
	patterns                                []string
	filter                                  treeFilter
	// files are the operands after the flags and the pattern.
	files []string
}

// parseGrepArgs reads grep's flags and pattern. A usage error is reported on
// stderr, and ok is false: grep exits 2.
func parseGrepArgs(args []string, stderr io.Writer) (grepOpts, bool) {
	var o grepOpts
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
				o.filter.include = append(o.filter.include, value)
				continue
			case "--exclude":
				o.filter.exclude = append(o.filter.exclude, value)
				continue
			case "--exclude-dir":
				o.filter.excludeDir = append(o.filter.excludeDir, value)
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
				return o, false
			}
			i++
			o.patterns = append(o.patterns, args[i])
		default:
			for _, c := range a[1:] {
				switch c {
				case 'q':
					o.quiet = true
				case 'i':
					o.fold = true
				case 'F':
					o.fixed = true
				case 'E':
					o.extended = true
				case 'n', 's':
				case 'r', 'R':
					o.recursive = true
				default:
					fmt.Fprintf(stderr, "grep: unsupported flag -%c\n", c)
					return o, false
				}
			}
		}
	}
done:
	if len(o.patterns) == 0 {
		if i >= len(args) {
			fmt.Fprintln(stderr, "grep: missing pattern")
			return o, false
		}
		o.patterns = append(o.patterns, args[i])
		i++
	}
	o.files = args[i:]
	return o, true
}

// compile is the patterns as one RE2 expression: -F quotes them, a basic
// expression (no -E) is translated, -i folds case.
func (o grepOpts) compile() (*regexp.Regexp, error) {
	exprs := make([]string, len(o.patterns))
	for n, p := range o.patterns {
		switch {
		case o.fixed:
			p = regexp.QuoteMeta(p)
		case !o.extended:
			p = basicToRE2(p)
		}
		exprs[n] = p
	}
	expr := strings.Join(exprs, "|")
	if o.fold {
		expr = "(?i)" + expr
	}
	return regexp.Compile("(?m)" + expr)
}

func portableGrep(_ context.Context, dir string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	o, ok := parseGrepArgs(args, stderr)
	if !ok {
		return 2
	}
	re, err := o.compile()
	if err != nil {
		fmt.Fprintf(stderr, "grep: bad pattern: %v\n", err)
		return 2
	}
	quiet, recursive, filter, files := o.quiet, o.recursive, o.filter, o.files
	matched, failed := false, false
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
		// A file grep cannot read is an error, but not the end of the search:
		// GNU grep reports it, searches the remaining operands, and exits 0
		// when -q found a match anyway — so `grep -rqs X frontends packages`
		// matches in a project with no packages/ directory.
		if st, serr := os.Stat(p); serr != nil {
			fmt.Fprintf(stderr, "grep: %s: %v\n", f, serr)
			failed = true
			continue
		} else if st.IsDir() {
			if !recursive {
				fmt.Fprintf(stderr, "grep: %s: is a directory\n", f)
				failed = true
				continue
			}
			grepTree(p, filter, scan)
			continue
		}
		fh, oerr := os.Open(p)
		if oerr != nil {
			fmt.Fprintf(stderr, "grep: %s: %v\n", f, oerr)
			failed = true
			continue
		}
		scan(fh, f)
		_ = fh.Close()
	}
	switch {
	case matched && (quiet || !failed):
		return 0
	case failed:
		return 2
	}
	return 1
}
