package querycapture

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// EnvDir names the directory capture writes to. Unset or empty, nothing is
// captured and the ORM installs nothing.
const EnvDir = "FORGE_QUERY_CAPTURE_DIR"

// SchemaVersion is the version of the record format, the "v" of every
// record. A change a reader of the previous version would misread bumps it;
// adding a field does not.
const SchemaVersion = 1

// Record is one line of a capture file: the first time this process ran a
// statement of this shape from this caller.
type Record struct {
	Version     int      `json:"v"`
	Fingerprint string   `json:"fingerprint"`
	SQL         string   `json:"sql"`
	Operation   string   `json:"operation,omitempty"`
	Tables      []string `json:"tables,omitempty"`
	// Caller is the first frame outside the database stack (see the package
	// doc) and outside generated code: the code that asked for the query.
	Caller Frame `json:"caller"`
	// Generated is the generated frame (a file named *_gen.go, such as a
	// generated store method) the query passed through on its way from
	// Caller, when there was one.
	Generated *Frame `json:"generated,omitempty"`
	// Test is the Go test function (TestXxx) on the stack, when the query
	// ran on the test's own goroutine. Subtest names are not knowable from a
	// stack.
	Test string `json:"test,omitempty"`
}

// Frame is one stack frame.
type Frame struct {
	// File is the path relative to the root of the Go module that contains
	// it (the nearest go.mod above it), slash-separated: "internal/db/x.go".
	// When no go.mod is found it is AbsFile; in a -trimpath build it is the
	// import-path form the binary recorded.
	File string `json:"file"`
	// AbsFile is the absolute path, when the binary recorded one.
	AbsFile string `json:"abs_file,omitempty"`
	// Module is the module path of the go.mod File is relative to.
	Module   string `json:"module,omitempty"`
	Line     int    `json:"line"`
	Function string `json:"function"`
}

// A Recorder appends records to one capture file. Its methods are safe for
// concurrent use, and a nil *Recorder records nothing.
type Recorder struct {
	dir string

	mu   sync.Mutex
	seen map[seenKey]struct{}
	file *os.File
	// disabled is set, under mu, by the first failure; read without it on
	// the fast path.
	disabled atomic.Bool

	stmts     sync.Map // raw query → Statement
	stmtCount atomic.Int64

	modules sync.Map // directory → moduleInfo
}

type seenKey struct {
	fingerprint, file string
	line              int
}

// maxCachedStatements bounds the raw-text → Statement cache. Statements
// with inline values have unbounded distinct texts; past the bound they are
// normalized on every call instead.
const maxCachedStatements = 4096

var (
	processMu        sync.Mutex
	processRecorders = map[string]*Recorder{}
)

// FromEnv returns this process's Recorder for $FORGE_QUERY_CAPTURE_DIR, or
// nil when the variable is unset or empty. Every call with the same
// directory returns the same Recorder, so every client in a process shares
// one file and one dedup set.
func FromEnv() *Recorder {
	dir := os.Getenv(EnvDir)
	if dir == "" {
		return nil
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	processMu.Lock()
	defer processMu.Unlock()
	r, ok := processRecorders[dir]
	if !ok {
		r = New(dir)
		processRecorders[dir] = r
	}
	return r
}

// New returns a Recorder writing to a new file in dir, named
// <pid>-<random>.jsonl. Nothing is created until the first record.
func New(dir string) *Recorder {
	return &Recorder{dir: dir, seen: map[seenKey]struct{}{}}
}

// Record captures query, run from the calling goroutine: once per distinct
// (fingerprint, caller) pair in this Recorder's life. Only statements EXPLAIN
// can plan are captured (see Explainable); DDL, SET, BEGIN and the like are
// not query shapes. It never fails and
// never panics on the caller's behalf: a capture error is reported on stderr
// once and capture stops for the process.
func (r *Recorder) Record(query string) {
	if r == nil {
		return
	}
	if r.disabled.Load() {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			r.fail(fmt.Errorf("panic: %v", p))
		}
	}()

	stmt := r.normalize(query)
	if !Explainable(stmt.Operation) {
		return
	}
	var pcs [128]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	// Every frame read is checked for the test it runs under, the frames
	// callerOf skips included: a test of a database-stack package is one.
	test := ""
	next := func() (runtime.Frame, bool) {
		f, more := frames.Next()
		if test == "" {
			test = testFunc(f.File, f.Function)
		}
		return f, more
	}
	caller, generated, _ := r.callerOf(next)
	key := seenKey{fingerprint: stmt.Fingerprint, file: caller.AbsFile, line: caller.Line}
	if key.file == "" { // -trimpath: the module-relative path, qualified
		key.file = caller.Module + "/" + caller.File
	}

	r.mu.Lock()
	if _, dup := r.seen[key]; dup || r.disabled.Load() {
		r.mu.Unlock()
		return
	}
	r.seen[key] = struct{}{}
	r.mu.Unlock()

	if test == "" {
		test = testName(next)
	}
	rec := Record{
		Version:     SchemaVersion,
		Fingerprint: stmt.Fingerprint,
		SQL:         stmt.SQL,
		Operation:   stmt.Operation,
		Tables:      stmt.Tables,
		Caller:      caller,
		Generated:   generated,
		Test:        test,
	}
	line, err := json.Marshal(rec)
	if err != nil {
		r.fail(err)
		return
	}
	r.write(append(line, '\n'))
}

// Close closes the capture file. Records after Close open a new file.
func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

func (r *Recorder) normalize(query string) Statement {
	if v, ok := r.stmts.Load(query); ok {
		return v.(Statement)
	}
	stmt := Normalize(query)
	if r.stmtCount.Load() < maxCachedStatements {
		if _, loaded := r.stmts.LoadOrStore(query, stmt); !loaded {
			r.stmtCount.Add(1)
		}
	}
	return stmt
}

// write appends one record. A single write of a whole line to an O_APPEND
// file, so concurrent processes sharing a directory never interleave lines.
func (r *Recorder) write(line []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disabled.Load() {
		return
	}
	if r.file == nil {
		f, err := r.open()
		if err != nil {
			r.failLocked(err)
			return
		}
		r.file = f
	}
	if _, err := r.file.Write(line); err != nil {
		r.failLocked(err)
	}
}

func (r *Recorder) open() (*os.File, error) {
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return nil, err
	}
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("%d-%s.jsonl", os.Getpid(), hex.EncodeToString(b[:]))
	return os.OpenFile(filepath.Join(r.dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_EXCL, 0o644)
}

func (r *Recorder) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failLocked(err)
}

// warnOut is where the one capture warning goes. A var for tests.
var warnOut io.Writer = os.Stderr

func (r *Recorder) failLocked(err error) {
	if r.disabled.Load() {
		return
	}
	r.disabled.Store(true)
	fmt.Fprintf(warnOut, "forge: query capture to %s failed, capture is off for this process: %v\n", r.dir, err)
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
	}
}

// ─── callers ──────────────────────────────────────────────────────────────

// databaseStack are the packages between the code that asks for a query
// and the wire; the caller is the first frame outside them. A path matches
// the package itself and every package below it. connect and testing are
// here because they only ever sit BELOW the code that asked (a handler, a
// test), and naming them as the caller would say nothing.
var databaseStack = []string{
	"runtime",
	"database/sql",
	"github.com/uptrace/bun",
	"github.com/jackc/pgx",
	"github.com/jackc/puddle",
	"github.com/XSAM/otelsql",
	"go.opentelemetry.io",
	"connectrpc.com/connect",
	"testing",
	"github.com/reliant-labs/forge/pkg/orm",
	"github.com/reliant-labs/forge/pkg/crud",
	"github.com/reliant-labs/forge/pkg/querycapture",
	"github.com/reliant-labs/forge/pkg/observe/dbtrace",
}

// inDatabaseStack reports whether fn, a runtime function name, belongs to a
// package of the database stack. A test package of one ("…/pkg/orm_test")
// does not: its tests are callers.
func inDatabaseStack(fn string) bool {
	pkg := packageOf(fn)
	for _, p := range databaseStack {
		if pkg == p || strings.HasPrefix(pkg, p+"/") {
			return true
		}
	}
	return false
}

// packageOf returns the import path of a runtime function name such as
// "github.com/a/b.(*T).M.func1": up to the first dot after the last slash.
func packageOf(fn string) string {
	slash := strings.LastIndexByte(fn, '/')
	if dot := strings.IndexByte(fn[slash+1:], '.'); dot >= 0 {
		return fn[:slash+1+dot]
	}
	return fn
}

// callerOf consumes frames up to the caller: the first frame outside the
// database stack and outside a generated file, with the first generated
// frame skipped on the way. When only generated frames remain, the first
// of them is the caller. ok is false when the stack held no frame outside
// the database stack.
func (r *Recorder) callerOf(next func() (runtime.Frame, bool)) (caller Frame, generated *Frame, ok bool) {
	for {
		f, more := next()
		if f.Function != "" && !inDatabaseStack(f.Function) {
			fr := r.frame(f)
			if !strings.HasSuffix(f.File, "_gen.go") {
				return fr, generated, true
			}
			if generated == nil {
				generated = &fr
			}
		}
		if !more {
			break
		}
	}
	if generated != nil {
		return *generated, nil, true
	}
	return Frame{}, nil, false
}

// testName finds the Go test function on the rest of the stack. A closure
// inside a test (a subtest body, or a goroutine the test started) names the
// test too.
func testName(next func() (runtime.Frame, bool)) string {
	for {
		f, more := next()
		if name := testFunc(f.File, f.Function); name != "" {
			return name
		}
		if !more {
			return ""
		}
	}
}

// testFunc returns TestXxx when fn, in file, is a Go test or a closure in one.
func testFunc(file, fn string) string {
	if !strings.HasSuffix(file, "_test.go") {
		return ""
	}
	name := fn[strings.LastIndexByte(fn, '/')+1:]
	if dot := strings.IndexByte(name, '.'); dot >= 0 {
		name = name[dot+1:]
	}
	if dot := strings.IndexByte(name, '.'); dot >= 0 {
		name = name[:dot]
	}
	if strings.HasPrefix(name, "Test") {
		return name
	}
	return ""
}

type moduleInfo struct{ root, path string }

func (r *Recorder) frame(f runtime.Frame) Frame {
	fr := Frame{File: f.File, Line: f.Line, Function: f.Function}
	if !filepath.IsAbs(f.File) {
		// A -trimpath build records "<module>[@version]/<path>".
		if mod, rel, ok := trimmedModuleFile(f.File); ok {
			fr.File, fr.Module = rel, mod
		}
		return fr
	}
	fr.AbsFile = f.File
	if mod := r.moduleOf(filepath.Dir(f.File)); mod.root != "" {
		if rel, err := filepath.Rel(mod.root, f.File); err == nil {
			fr.File = filepath.ToSlash(rel)
			fr.Module = mod.path
		}
	}
	return fr
}

// moduleOf finds the go.mod at or above dir. Cached per directory: callers
// come from few directories.
func (r *Recorder) moduleOf(dir string) moduleInfo {
	if v, ok := r.modules.Load(dir); ok {
		return v.(moduleInfo)
	}
	var mod moduleInfo
	for d := dir; ; {
		if f, err := os.Open(filepath.Join(d, "go.mod")); err == nil {
			mod = moduleInfo{root: d, path: modulePath(f)}
			_ = f.Close()
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	r.modules.Store(dir, mod)
	return mod
}

func modulePath(f *os.File) string {
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, "module"); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}

// buildModules are the module paths of this binary (its main module and its
// dependencies), longest first, for reading -trimpath file names.
var buildModules = sync.OnceValue(func() []string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	var mods []string
	if info.Main.Path != "" {
		mods = append(mods, info.Main.Path)
	}
	for _, d := range info.Deps {
		mods = append(mods, d.Path)
	}
	sort.Slice(mods, func(i, j int) bool { return len(mods[i]) > len(mods[j]) })
	return mods
})

// trimmedModuleFile splits a -trimpath file name, "<module>/<path>" or
// "<module>@<version>/<path>", at the longest module of this binary it
// starts with.
func trimmedModuleFile(file string) (module, rel string, ok bool) {
	for _, m := range buildModules() {
		rest, found := strings.CutPrefix(file, m)
		if !found || rest == "" {
			continue
		}
		if rest[0] == '@' {
			slash := strings.IndexByte(rest, '/')
			if slash < 0 {
				continue
			}
			rest = rest[slash:]
		}
		if rest[0] == '/' && len(rest) > 1 {
			return m, rest[1:], true
		}
	}
	return "", "", false
}
