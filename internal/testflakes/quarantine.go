package testflakes

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// The quarantine list is one file per quarantined test:
//
//	ci/quarantine/<lang>/<package dir>/<Test>.yaml
//
// <package dir> is the package's directory relative to the module root, and
// `_root` for the module's root package. One file per test is what lets two
// quarantine pull requests in one merge batch never conflict: each adds its
// own file, and the path is the key. The skip list is derived from the file
// NAMES alone, so a gate needs no YAML parser (control-plane's
// scripts/lib/quarantine.sh reads the same tree with sed).
//
// The file holds flat `key: value` lines — lang, package, test, issue,
// reason, signature, quarantined_at, expires_at, commit, run — so it is YAML
// and a line-oriented shell reader alike.

// QuarantineDir is the quarantine list's directory, relative to the project
// root, slash-separated.
const QuarantineDir = "ci/quarantine"

// RootPackageDir names the module's root package's directory in the list.
const RootPackageDir = "_root"

// DefaultQuarantineTTL is how long a new entry lives before it is overdue:
// the quarantine is the gap between detection and deletion, not a parking lot.
const DefaultQuarantineTTL = 48 * time.Hour

// EnvQuarantineExtra names an overlay file of "<package>\t<Test>" lines added
// to the committed Go skip list. control-plane's ci-full-main.sh sets it to
// re-judge a red without the flakes it just confirmed; the name is kept so
// that caller works unchanged.
const EnvQuarantineExtra = "CI_QUARANTINE_EXTRA"

// Entry is one quarantine file.
type Entry struct {
	Lang    testreport.Lang
	Package string // the import path
	Test    string // the top-level test
	Issue   string
	Reason  string
	// Signature is the failure signature (testreport.FailureSignature) the
	// test was quarantined for.
	Signature     string
	QuarantinedAt time.Time
	ExpiresAt     time.Time // zero: never overdue
	Commit        string
	Run           string
	// Path is the file, relative to the project root, slash-separated.
	Path string
}

// ID is the entry's test id.
func (e Entry) ID() testreport.TestID {
	return testreport.TestID{Lang: e.Lang, Package: e.Package, Name: e.Test}
}

// Overdue reports whether the entry's expires_at has passed at now. An
// overdue entry stays applied: it is reported, never dropped.
func (e Entry) Overdue(now time.Time) bool {
	return !e.ExpiresAt.IsZero() && !now.Before(e.ExpiresAt)
}

// Document is the entry as forge.test.flakes/v1 carries it.
func (e Entry) Document(now time.Time) *testreport.QuarantineEntry {
	q := &testreport.QuarantineEntry{Path: e.Path, Issue: e.Issue, Reason: e.Reason, Signature: e.Signature, Overdue: e.Overdue(now)}
	if !e.ExpiresAt.IsZero() {
		t := e.ExpiresAt
		q.ExpiresAt = &t
	}
	return q
}

// ModulePath reads the module path from root's go.mod.
func ModulePath(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	mp := modfile.ModulePath(data)
	if mp == "" {
		return "", fmt.Errorf("%s: no module line", filepath.Join(root, "go.mod"))
	}
	return mp, nil
}

// goTestName is a Go top-level test function name the skip list accepts.
var goTestName = regexp.MustCompile(`^Test[A-Za-z0-9_]*$`)

// ParseQuarantineTarget reads the test a quarantine command names, in the
// `<lang>:<package>:<name>` form forge prints (`go:example.com/shop/billing:TestRefund`)
// or as `<package>:<name>` (a Go test). <package> may be the import path or
// the package's directory relative to the module root (`./internal/billing`,
// `internal/billing`, `.`). A subtest is reduced to its top-level test: that
// is what -skip addresses.
func ParseQuarantineTarget(arg, module string) (testreport.TestID, error) {
	id, err := testreport.ParseTestID(arg)
	if err != nil || strings.ContainsAny(string(id.Lang), "/.") {
		pkg, name, ok := strings.Cut(arg, ":")
		if !ok {
			return testreport.TestID{}, fmt.Errorf("test %q: want go:<package>:<Test> or <package>:<Test>", arg)
		}
		id = testreport.TestID{Lang: testreport.LangGo, Package: pkg, Name: name}
	}
	if id.Lang != testreport.LangGo {
		return testreport.TestID{}, fmt.Errorf("test %q: only Go tests can be quarantined today (the %s directory is reserved)", arg, id.Lang)
	}
	id = id.TopLevel()
	if !goTestName.MatchString(id.Name) {
		return testreport.TestID{}, fmt.Errorf("test %q: %q is not a Go test function name (Test…)", arg, id.Name)
	}
	pkg, err := importPath(id.Package, module)
	if err != nil {
		return testreport.TestID{}, fmt.Errorf("test %q: %w", arg, err)
	}
	id.Package = pkg
	return id, nil
}

// importPath turns a package given as an import path or a module-relative
// directory into the import path.
func importPath(pkg, module string) (string, error) {
	pkg = strings.TrimSuffix(strings.TrimSpace(pkg), "/")
	switch {
	case pkg == "" || pkg == "." || pkg == "./" || pkg == RootPackageDir:
		if module == "" {
			return "", errors.New("the module root package needs a go.mod to name it")
		}
		return module, nil
	case module != "" && pkg == module:
		return pkg, nil
	case module != "" && strings.HasPrefix(pkg, module+"/"):
		if err := checkPackageDir(strings.TrimPrefix(pkg, module+"/")); err != nil {
			return "", fmt.Errorf("package %s: %w", pkg, err)
		}
		return pkg, nil
	case strings.HasPrefix(pkg, "./"):
		pkg = strings.TrimPrefix(pkg, "./")
	case strings.Contains(strings.SplitN(pkg, "/", 2)[0], ".") && !strings.HasPrefix(pkg, "."):
		// A host-qualified import path of another module.
		return "", fmt.Errorf("package %s is not in module %s", pkg, module)
	}
	if module == "" {
		return "", fmt.Errorf("package %s: no go.mod to resolve it against", pkg)
	}
	clean := path.Clean(pkg)
	if strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("package %s is outside the module", pkg)
	}
	if err := checkPackageDir(clean); err != nil {
		return "", fmt.Errorf("package %s: %w", pkg, err)
	}
	return module + "/" + clean, nil
}

// checkPackageDir refuses a module-relative package directory that is not
// a plain relative path inside the module: absolute, `.` or `..` segments,
// empty segments, or a backslash. It is what keeps an entry's file under
// ci/quarantine/<lang>/.
func checkPackageDir(dir string) error {
	if dir == "" || strings.HasPrefix(dir, "/") || strings.Contains(dir, `\`) {
		return fmt.Errorf("%q is not a directory inside the module", dir)
	}
	for _, seg := range strings.Split(dir, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("%q is not a directory inside the module", dir)
		}
	}
	return nil
}

// EntryPath is the file an entry for id lives in, relative to the project
// root and slash-separated. It refuses an id whose package or name would
// put the file anywhere but under ci/quarantine/<lang>/: callers join the
// path to the project root and write it.
func EntryPath(id testreport.TestID, module string) (string, error) {
	dir := RootPackageDir
	switch {
	case id.Package == module:
	case strings.HasPrefix(id.Package, module+"/"):
		dir = strings.TrimPrefix(id.Package, module+"/")
		if err := checkPackageDir(dir); err != nil {
			return "", fmt.Errorf("package %s: %w", id.Package, err)
		}
	default:
		return "", fmt.Errorf("package %s is not in module %s", id.Package, module)
	}
	if !goTestName.MatchString(id.Name) {
		return "", fmt.Errorf("test %q is not a Go test function name (Test…)", id.Name)
	}
	if id.Lang != testreport.LangGo {
		return "", fmt.Errorf("test %s: only Go tests can be quarantined today", id)
	}
	return path.Join(QuarantineDir, string(id.Lang), dir, id.Name+".yaml"), nil
}

// QuarantineList reads every entry under root's ci/quarantine/, sorted by
// path. A file that cannot be read is skipped and reported in the error list,
// never fatal: an unreadable list must not stop a gate from running tests.
func QuarantineList(root string) ([]Entry, []error) {
	base := filepath.Join(root, filepath.FromSlash(QuarantineDir))
	var (
		out  []Entry
		errs []error
	)
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == base && errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipAll
			}
			errs = append(errs, err)
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".yaml") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		parts := strings.Split(strings.TrimPrefix(rel, QuarantineDir+"/"), "/")
		if len(parts) < 3 {
			return nil // ci/quarantine/<lang>/<file>: no package directory, not an entry
		}
		e, rerr := readEntry(p)
		if rerr != nil {
			errs = append(errs, rerr)
			return nil
		}
		e.Path = rel
		if e.Lang == "" {
			e.Lang = testreport.Lang(parts[0])
		}
		if e.Test == "" {
			e.Test = strings.TrimSuffix(parts[len(parts)-1], ".yaml")
		}
		out = append(out, e)
		return nil
	})
	if err != nil {
		errs = append(errs, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, errs
}

func readEntry(p string) (Entry, error) {
	f, err := os.Open(p)
	if err != nil {
		return Entry{}, err
	}
	defer func() { _ = f.Close() }()
	var e Entry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = unquote(strings.TrimSpace(v))
		switch strings.TrimSpace(k) {
		case "lang":
			e.Lang = testreport.Lang(v)
		case "package":
			e.Package = v
		case "test":
			e.Test = v
		case "issue":
			e.Issue = v
		case "reason":
			e.Reason = v
		case "signature":
			e.Signature = v
		case "quarantined_at":
			e.QuarantinedAt, _ = time.Parse(time.RFC3339, v)
		case "expires_at":
			e.ExpiresAt, _ = time.Parse(time.RFC3339, v)
		case "commit":
			e.Commit = v
		case "run":
			e.Run = v
		}
	}
	if err := sc.Err(); err != nil {
		return Entry{}, fmt.Errorf("%s: %w", p, err)
	}
	return e, nil
}

func unquote(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		if s, err := strconv.Unquote(v); err == nil {
			return s
		}
	}
	return v
}

// yamlValue writes v as a YAML scalar a `sed 's/^key: *//'` reader also takes
// as is: plain when it can be, double-quoted (and on one line) when a plain
// scalar would change its meaning.
func yamlValue(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	if v == "" {
		return `""`
	}
	needsQuote := strings.ContainsAny(v[:1], `!&*?|>'"%@`+"`{[,#-") ||
		strings.Contains(v, ": ") || strings.Contains(v, " #") || strings.HasSuffix(v, ":")
	if !needsQuote {
		return v
	}
	return strconv.Quote(v)
}

// AddResult is what QuarantineAdd did.
type AddResult struct {
	Entry Entry
	// Existed: an entry was already there. It is left as it was unless
	// Force rewrote it.
	Existed   bool
	Rewritten bool
}

// QuarantineAdd writes e's file under root. An entry already there is left
// alone (a test is never quarantined twice) unless force is set.
func QuarantineAdd(root, module string, e Entry, force bool) (AddResult, error) {
	rel, err := EntryPath(e.ID(), module)
	if err != nil {
		return AddResult{}, err
	}
	e.Path = rel
	abs := filepath.Join(root, filepath.FromSlash(rel))
	_, statErr := os.Stat(abs)
	existed := statErr == nil
	if existed && !force {
		old, rerr := readEntry(abs)
		if rerr != nil {
			return AddResult{}, rerr
		}
		old.Path = rel
		return AddResult{Entry: old, Existed: true}, nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return AddResult{}, err
	}
	if err := atomicfile.WriteFile(abs, renderEntry(e), 0o644); err != nil {
		return AddResult{}, err
	}
	return AddResult{Entry: e, Existed: existed, Rewritten: existed}, nil
}

func renderEntry(e Entry) []byte {
	var b strings.Builder
	b.WriteString("# Quarantined by `forge test quarantine add`. Delete this file in the pull request\n")
	b.WriteString("# that deletes the test or rewrites it as a deterministic test.\n")
	field := func(k, v string, always bool) {
		if v == "" && !always {
			return
		}
		fmt.Fprintf(&b, "%s: %s\n", k, yamlValue(v))
	}
	field("lang", string(e.Lang), true)
	field("package", e.Package, true)
	field("test", e.Test, true)
	field("issue", e.Issue, false)
	field("reason", e.Reason, false)
	field("signature", e.Signature, false)
	if !e.QuarantinedAt.IsZero() {
		field("quarantined_at", e.QuarantinedAt.UTC().Format(time.RFC3339), true)
	}
	if !e.ExpiresAt.IsZero() {
		field("expires_at", e.ExpiresAt.UTC().Format(time.RFC3339), true)
	}
	field("commit", e.Commit, false)
	field("run", e.Run, false)
	return []byte(b.String())
}

// ErrNotQuarantined is QuarantineRemove's error for a test with no entry.
var ErrNotQuarantined = errors.New("not quarantined")

// QuarantineRemove deletes id's entry, and the package directories it leaves
// empty (never ci/quarantine/<lang> itself). It returns the removed path.
func QuarantineRemove(root, module string, id testreport.TestID) (string, error) {
	rel, err := EntryPath(id, module)
	if err != nil {
		return "", err
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.Remove(abs); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return rel, fmt.Errorf("%s: %w (no %s)", id, ErrNotQuarantined, rel)
		}
		return rel, err
	}
	stop := filepath.Join(root, filepath.FromSlash(QuarantineDir), string(id.Lang))
	for dir := filepath.Dir(abs); dir != stop && strings.HasPrefix(dir, stop); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil { // not empty, or gone
			break
		}
	}
	return rel, nil
}

// GoSkipNames are the quarantined Go test names, sorted and unique: the file
// names under ci/quarantine/go/, plus the EnvQuarantineExtra overlay's
// "<package>\t<Test>" lines when extra names a readable file. Names that are
// not Go test functions are ignored, as quarantine.sh ignores them.
func GoSkipNames(root, extra string) []string {
	seen := map[string]bool{}
	_ = filepath.WalkDir(filepath.Join(root, filepath.FromSlash(QuarantineDir), "go"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if name, ok := strings.CutSuffix(d.Name(), ".yaml"); ok && goTestName.MatchString(name) {
			seen[name] = true
		}
		return nil
	})
	if extra != "" {
		if data, err := os.ReadFile(extra); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				f := strings.Split(strings.TrimRight(line, "\r"), "\t")
				if len(f) >= 2 && goTestName.MatchString(f[1]) {
					seen[f[1]] = true
				}
			}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// GoSkipRegex is the `go test -skip` value that skips every quarantined Go
// test, `^(TestA|TestB)$`, or "" when none is quarantined.
func GoSkipRegex(root, extra string) string {
	names := GoSkipNames(root, extra)
	if len(names) == 0 {
		return ""
	}
	return "^(" + strings.Join(names, "|") + ")$"
}
