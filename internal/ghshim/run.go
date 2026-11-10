// Copyright (c) 2025 Reliant Labs
package ghshim

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// Dispatch runs this process as the shim and exits when its first argument
// is DispatchArg, and returns at once otherwise. Call it first thing in main,
// before any command-line parsing: the arguments after it are gh's.
func Dispatch() {
	if len(os.Args) < 2 || os.Args[1] != DispatchArg {
		return
	}
	cwd, _ := os.Getwd()
	os.Exit(Run(os.Args[2:], os.Stdin, os.Stdout, os.Stderr, os.Environ(), cwd)) //nolint:forbidigo // the shim runs gh with the shell's own environment; not configuration
}

// Run is `<self> __gh-shim <shim dir> pr create|new [flags]`: append the
// chat's marker to the pull request body, run the real gh (the first gh on
// env's PATH that is not a shim) and return its exit status. Anything it
// cannot do runs gh with the arguments as given.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, env []string, cwd string) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "reliant "+DispatchArg+": missing the shim directory")
		return 2
	}
	here, ghArgs := args[0], args[1:]
	env = withoutPathDir(env, here)
	gh, err := findGh(env, here)
	if err != nil {
		fmt.Fprintln(stderr, "gh: "+err.Error())
		return 127
	}
	r := runner{gh: gh, env: env, cwd: cwd, stdin: stdin, stdout: stdout, stderr: stderr}

	p := planCreate(ghArgs, env, cwd, stdin)
	defer p.cleanup()
	if !p.fillAfter {
		return r.run(p.args, stdout)
	}
	// --fill: gh writes the body, so the marker goes on once the pull request
	// exists. Its URL is the last line gh prints.
	var out bytes.Buffer
	code := r.run(p.args, io.MultiWriter(stdout, &limitedWriter{w: &out, n: 1 << 20}))
	if url := lastPullURL(out.String()); url != "" {
		if err := r.markAfter(url, p.chat); err != nil {
			fmt.Fprintln(stderr, "reliant: the pull request was created, but naming this chat in its body failed: "+err.Error())
		}
	}
	return code
}

// plan is what Run passes gh.
type plan struct {
	args []string
	// fillAfter: append the marker to the body gh wrote, after it creates
	// the pull request.
	fillAfter bool
	chat      string
	tmp       string
}

func (p plan) cleanup() {
	if p.tmp != "" {
		_ = os.Remove(p.tmp)
	}
}

// planCreate decides how `gh pr create args` gets the marker. A plan that
// changes nothing has args as given.
func planCreate(args, env []string, cwd string, stdin io.Reader) plan {
	unchanged := plan{args: args}
	if len(args) < 2 || args[0] != "pr" || (args[1] != "create" && args[1] != "new") {
		return unchanged
	}
	if off, _ := lookupEnv(env, OffEnv); strings.EqualFold(strings.TrimSpace(off), "off") {
		return unchanged
	}
	chat, _ := lookupEnv(env, ChatIDEnv)
	chat = strings.TrimSpace(chat)
	if !chatIDRe.MatchString(chat) {
		return unchanged
	}
	fl, ok := parseCreateFlags(args[2:])
	if !ok {
		return unchanged
	}
	// Interactive or browser paths, and a recovered run, write their own body.
	for _, name := range []string{"web", "editor", "recover", "help"} {
		if fl.isSet(name) {
			return unchanged
		}
	}
	body, hasBody := fl.last("body")
	file, hasFile := fl.last("body-file")
	switch {
	case hasBody && hasFile:
		return unchanged // gh refuses both; let it say so
	case !hasBody && !hasFile:
		if !fl.isSet("dry-run") && (fl.isSet("fill") || fl.isSet("fill-first") || fl.isSet("fill-verbose")) {
			return plan{args: args, fillAfter: true, chat: chat}
		}
		return unchanged
	}
	if fl.clustered("body") || fl.clustered("body-file") {
		return unchanged // a body inside a cluster of short flags (-db x): not worth rewriting
	}

	text, fromStdin := body, false
	if hasFile {
		var err error
		if file == "-" {
			var b []byte
			b, err = io.ReadAll(stdin)
			text, fromStdin = string(b), true
		} else {
			var b []byte
			b, err = os.ReadFile(resolve(cwd, file))
			text = string(b)
		}
		if err != nil {
			return unchanged // gh reports a missing file itself
		}
	}
	if markerRe.MatchString(text) {
		if fromStdin {
			// Already read: hand gh the text it would have read.
			return plan{args: rewrite(args, fl, "--body", text)}
		}
		return unchanged
	}
	stamped := stamp(text, chat)

	// gh resolves an --attach path a body references against the body file's
	// directory, else the working directory: keep the rewritten body there.
	dir := os.TempDir()
	if fl.isSet("attach") {
		dir = cwd
		if hasFile && file != "-" {
			dir = filepath.Dir(resolve(cwd, file))
		}
	}
	tmp, err := writeTemp(dir, stamped)
	if err != nil {
		if fromStdin {
			return plan{args: rewrite(args, fl, "--body", stamped)}
		}
		return unchanged
	}
	return plan{args: rewrite(args, fl, "--body-file", tmp), tmp: tmp}
}

// rewrite is args' subcommand (`pr create` or `pr new`) followed by its flags
// minus every body flag, then body (the replacement body flag and its value),
// then the end-of-options `--` when the command ended with one: after it gh
// would read body as positional arguments, not a flag.
func rewrite(args []string, fl createFlags, body ...string) []string {
	flags := fl.without("body", "body-file")
	if fl.terminated {
		flags = flags[:len(flags)-1]
	}
	out := append([]string{args[0], args[1]}, flags...)
	out = append(out, body...)
	if fl.terminated {
		out = append(out, "--")
	}
	return out
}

// markAfter appends chat's marker to the body of the pull request at url,
// unless it already names a chat.
func (r runner) markAfter(url, chat string) error {
	var body, errOut bytes.Buffer
	if code := r.runTo([]string{"pr", "view", url, "--json", "body", "--jq", ".body"}, &body, &errOut); code != 0 {
		return fmt.Errorf("gh pr view: exit %d: %s", code, strings.TrimSpace(errOut.String()))
	}
	text := strings.TrimRight(body.String(), "\n")
	if markerRe.MatchString(text) {
		return nil
	}
	tmp, err := writeTemp(os.TempDir(), stamp(text, chat))
	if err != nil {
		return err
	}
	defer os.Remove(tmp) //nolint:errcheck // best effort
	errOut.Reset()
	if code := r.runTo([]string{"pr", "edit", url, "--body-file", tmp}, io.Discard, &errOut); code != 0 {
		return fmt.Errorf("gh pr edit: exit %d: %s", code, strings.TrimSpace(errOut.String()))
	}
	return nil
}

// stamp is text with chat's marker appended (owning_chat.go's format).
func stamp(text, chat string) string {
	marker := "<!-- reliant-chat: " + chat + " -->"
	if strings.TrimSpace(text) == "" {
		return marker
	}
	return strings.TrimRight(text, "\n") + "\n\n" + marker
}

// markerRe finds any chat marker, whoever's (the same rule as
// internal/llm/tools/owning_chat.go: the caller's own marker wins).
var markerRe = regexp.MustCompile(`<!--\s*reliant-chat:\s*[^\s>]+\s*-->`)

// chatIDRe is a chat id: what control-plane's pr-fix reads from the marker.
var chatIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var pullURLRe = regexp.MustCompile(`https?://[^\s/]+/[^\s/]+/[^\s/]+/pull/[0-9]+`)

func lastPullURL(out string) string {
	all := pullURLRe.FindAllString(out, -1)
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1]
}

func resolve(cwd, p string) string {
	if filepath.IsAbs(p) || cwd == "" {
		return p
	}
	return filepath.Join(cwd, p)
}

func writeTemp(dir, text string) (string, error) {
	f, err := os.CreateTemp(dir, ".reliant-pr-body-*.md")
	if err != nil {
		return "", err
	}
	_, werr := f.WriteString(text)
	if err := errors.Join(werr, f.Close()); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// ---- gh pr create's flags ----------------------------------------------

// createValueFlags are gh pr create's flags that take a value (gh 2.x,
// including the inherited --repo), by long name, with their shorthands.
var createValueFlags = map[string]byte{
	"assignee": 'a', "attach": 0, "base": 'B', "body": 'b', "body-file": 'F', "head": 'H',
	"label": 'l', "milestone": 'm', "project": 'p', "recover": 0, "reviewer": 'r',
	"template": 'T', "title": 't', "repo": 'R',
}

// createBoolFlags are its boolean flags.
var createBoolFlags = map[string]byte{
	"draft": 'd', "dry-run": 0, "editor": 'e', "fill": 'f', "fill-first": 0, "fill-verbose": 0,
	"no-maintainer-edit": 0, "web": 'w', "help": 'h',
}

type occurrence struct {
	name, value string
	from, to    int // the args it spans, [from, to)
	clustered   bool
}

type createFlags struct {
	args []string
	occ  []occurrence
	// terminated: args end with the end-of-options `--`.
	terminated bool
}

// parseCreateFlags reads args the way gh's flag parser (pflag) does. It
// fails on anything it does not know — a flag gh added, a positional
// argument — so the shim never rewrites a command it misread.
func parseCreateFlags(args []string) (createFlags, bool) {
	fl := createFlags{args: args}
	short := map[byte]string{}
	for name, c := range createValueFlags {
		if c != 0 {
			short[c] = name
		}
	}
	for name, c := range createBoolFlags {
		if c != 0 {
			short[c] = name
		}
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			// Everything after it is positional, and gh pr create takes no
			// positional arguments: only a trailing `--` is understood.
			fl.terminated = true
			return fl, len(args) == i+1
		case strings.HasPrefix(a, "--"):
			name, value, hasValue := strings.Cut(a[2:], "=")
			if _, ok := createValueFlags[name]; ok {
				if !hasValue {
					next := args[i+1:]
					if len(next) == 0 {
						return fl, false
					}
					value = next[0]
					fl.occ = append(fl.occ, occurrence{name: name, value: value, from: i, to: i + 2})
					i++
					continue
				}
				fl.occ = append(fl.occ, occurrence{name: name, value: value, from: i, to: i + 1})
				continue
			}
			if _, ok := createBoolFlags[name]; ok {
				if !hasValue {
					value = "true"
				}
				if _, err := strconv.ParseBool(value); err != nil {
					return fl, false
				}
				fl.occ = append(fl.occ, occurrence{name: name, value: value, from: i, to: i + 1})
				continue
			}
			return fl, false
		case strings.HasPrefix(a, "-") && len(a) > 1:
			cluster := a[1:]
			start := len(fl.occ)
			for j := 0; j < len(cluster); j++ {
				name, ok := short[cluster[j]]
				if !ok {
					return fl, false
				}
				if _, isBool := createBoolFlags[name]; isBool {
					fl.occ = append(fl.occ, occurrence{name: name, value: "true", from: i, to: i + 1})
					continue
				}
				value := strings.TrimPrefix(cluster[j+1:], "=")
				to := i + 1
				if j+1 == len(cluster) {
					next := args[i+1:]
					if len(next) == 0 {
						return fl, false
					}
					value, to = next[0], i+2
				}
				fl.occ = append(fl.occ, occurrence{name: name, value: value, from: i, to: to})
				i = to - 1
				break
			}
			if len(fl.occ)-start > 1 {
				for k := start; k < len(fl.occ); k++ {
					fl.occ[k].clustered = true
				}
			}
		default:
			return fl, false
		}
	}
	return fl, true
}

// last is the value of name's last occurrence (pflag keeps the last).
func (f createFlags) last(name string) (string, bool) {
	for i := len(f.occ) - 1; i >= 0; i-- {
		if f.occ[i].name == name {
			return f.occ[i].value, true
		}
	}
	return "", false
}

func (f createFlags) isSet(name string) bool {
	v, ok := f.last(name)
	if !ok {
		return false
	}
	if _, isBool := createBoolFlags[name]; isBool {
		b, _ := strconv.ParseBool(v)
		return b
	}
	return true
}

func (f createFlags) clustered(name string) bool {
	for _, o := range f.occ {
		if o.name == name && o.clustered {
			return true
		}
	}
	return false
}

// without is the arguments minus every occurrence of names.
func (f createFlags) without(names ...string) []string {
	drop := make([]bool, len(f.args))
	for _, o := range f.occ {
		for _, n := range names {
			if o.name == n {
				for k := o.from; k < o.to; k++ {
					drop[k] = true
				}
			}
		}
	}
	var out []string
	for i, a := range f.args {
		if !drop[i] {
			out = append(out, a)
		}
	}
	return out
}

// ---- running gh ------------------------------------------------------------

type runner struct {
	gh, cwd        string
	env            []string
	stdin          io.Reader
	stdout, stderr io.Writer
}

func (r runner) run(args []string, stdout io.Writer) int {
	return r.runWith(args, r.stdin, stdout, r.stderr)
}

func (r runner) runTo(args []string, stdout, stderr io.Writer) int {
	return r.runWith(args, nil, stdout, stderr)
}

// runWith runs the real gh as a child (so the rewritten body's file can be
// removed after) and returns its exit status. Interrupts are passed on to it.
func (r runner) runWith(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := exec.Command(r.gh, args...)
	cmd.Env, cmd.Dir = r.env, r.cwd
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(stderr, "gh: "+err.Error())
		return 127
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case s := <-sigs:
				_ = cmd.Process.Signal(s)
			case <-done:
				return
			}
		}
	}()
	err := cmd.Wait()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exitErr) && exitErr.ExitCode() >= 0:
		return exitErr.ExitCode()
	default:
		return 1
	}
}

// findGh is the first gh on env's PATH outside here that is not a shim.
func findGh(env []string, here string) (string, error) {
	path, _ := lookupEnv(env, "PATH")
	for _, d := range filepath.SplitList(path) {
		if d == "" {
			d = "."
		}
		if filepath.Clean(d) == filepath.Clean(here) {
			continue
		}
		p := filepath.Join(d, "gh")
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() || fi.Mode()&0o111 == 0 {
			continue
		}
		if isShim(p) {
			continue
		}
		return p, nil
	}
	return "", errors.New("command not found on PATH")
}

func isShim(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close() //nolint:errcheck // read-only
	head := make([]byte, 256)
	n, _ := io.ReadFull(f, head)
	return bytes.Contains(head[:n], []byte(scriptTag))
}

// withoutPathDir is env with dir removed from PATH, so nothing gh runs comes
// back through the shim.
func withoutPathDir(env []string, dir string) []string {
	path, ok := lookupEnv(env, "PATH")
	if !ok {
		return env
	}
	var keep []string
	for _, d := range filepath.SplitList(path) {
		if filepath.Clean(d) != filepath.Clean(dir) {
			keep = append(keep, d)
		}
	}
	out := make([]string, len(env), len(env)+1)
	copy(out, env)
	return append(out, "PATH="+strings.Join(keep, string(os.PathListSeparator)))
}

// limitedWriter keeps the first n bytes written to it and drops the rest.
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		_, _ = l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}
