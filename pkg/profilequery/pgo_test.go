package profilequery

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/pprof/profile"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/pkg/testkit"
)

var (
	pgoFrom = time.Date(2026, 11, 1, 10, 15, 0, 0, time.UTC)
	pgoTo   = time.Date(2026, 11, 8, 10, 15, 0, 0, time.UTC)
)

func pgoQuery() PGOQuery {
	return PGOQuery{Services: []string{"api", "worker"}, Namespace: "shop", Env: "prod", From: pgoFrom, To: pgoTo}
}

func TestPGOQuerySQLBindsNamedParams(t *testing.T) {
	q := pgoQuery()
	q.Services = []string{"api", `we'ird\name`}
	sql, params := q.SQL()
	if sql != PGOEdgesSQL {
		t.Fatalf("SQL() returned a statement other than PGOEdgesSQL")
	}
	for _, want := range []string{"default.profile_pgo_edges", "{services:Array(String)}", "ServiceNamespace = {namespace:String}", "{env:String}", "{from:UInt32}", "{to:UInt32}", "GROUP BY caller, callee, offset", "LIMIT {limit:UInt32}"} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL lacks %q", want)
		}
	}
	if !strings.HasSuffix(sql, "FORMAT JSONEachRow") {
		t.Errorf("SQL must end in FORMAT JSONEachRow (pkg/obsquery strips that suffix)")
	}
	if strings.Contains(sql, "api") || strings.Contains(sql, "prod") || strings.Contains(sql, "shop") {
		t.Errorf("SQL embeds a value instead of binding it:\n%s", sql)
	}
	want := map[string]string{
		"services":  `['api','we\'ird\\name']`,
		"namespace": "shop",
		"env":       "prod",
		"from":      fmt.Sprint(pgoFrom.Unix()),
		"to":        fmt.Sprint(pgoTo.Unix()),
		"limit":     fmt.Sprint(DefaultMaxPGOEdges + 1),
	}
	if len(params) != len(want) {
		t.Fatalf("params = %v, want %v", params, want)
	}
	for k, v := range want {
		if params[k] != v {
			t.Errorf("params[%q] = %q, want %q", k, params[k], v)
		}
	}
}

func TestPGOQueryValidate(t *testing.T) {
	if err := pgoQuery().Validate(); err != nil {
		t.Fatalf("valid query refused: %v", err)
	}
	for name, mut := range map[string]func(*PGOQuery){
		"no services":   func(q *PGOQuery) { q.Services = nil },
		"empty service": func(q *PGOQuery) { q.Services = []string{"api", " "} },
		"from == to":    func(q *PGOQuery) { q.To = q.From },
		"from after to": func(q *PGOQuery) { q.From, q.To = q.To, q.From },
		"zero from":     func(q *PGOQuery) { q.From = time.Time{} },
		"negative max":  func(q *PGOQuery) { q.MaxEdges = -1 },
	} {
		q := pgoQuery()
		mut(&q)
		if err := q.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, q)
		}
		if _, err := ParsePGO(strings.NewReader(""), q); err == nil {
			t.Errorf("%s: ParsePGO accepted an invalid query", name)
		}
	}
}

const pgoRows = `{"caller":"main.main","callee":"main.hot","offset":7,"weight":"9000000","oldest":1761991200,"newest":1762596000}
{"caller":"main.serve","callee":"main.(*T).Do","offset":3,"weight":"500","oldest":1761987600,"newest":1762000000}
{"caller":"main.main","callee":"main.hot","offset":7,"weight":"1000000","oldest":1761900000,"newest":1762500000}
{"caller":"","callee":"main.hot","offset":1,"weight":"5"}
{"caller":"main.main","callee":"main.cold","offset":2,"weight":"0","oldest":1,"newest":2}
{"caller":"main.serve","callee":"main.(*T).Do","offset":-2,"weight":500.0,"oldest":1761987600,"newest":1761987600}
`

func TestParsePGO(t *testing.T) {
	p, err := ParsePGO(strings.NewReader(pgoRows), pgoQuery())
	if err != nil {
		t.Fatal(err)
	}
	want := []Edge{
		{Caller: "main.main", Callee: "main.hot", CallSiteOffset: 7, Weight: 10000000},
		{Caller: "main.serve", Callee: "main.(*T).Do", CallSiteOffset: -2, Weight: 500},
		{Caller: "main.serve", Callee: "main.(*T).Do", CallSiteOffset: 3, Weight: 500},
	}
	if fmt.Sprint(p.Edges) != fmt.Sprint(want) {
		t.Fatalf("edges = %+v\nwant    %+v (duplicates summed, empty/zero rows dropped, heaviest first)", p.Edges, want)
	}
	if p.TotalWeight() != 10001000 {
		t.Errorf("TotalWeight = %d", p.TotalWeight())
	}
	if !p.Oldest.Equal(time.Unix(1761900000, 0)) || !p.Newest.Equal(time.Unix(1762596000, 0)) {
		t.Errorf("oldest/newest = %v / %v, want the min and max over contributing rows", p.Oldest, p.Newest)
	}
	if p.Env != "prod" || p.Namespace != "shop" || strings.Join(p.Services, ",") != "api,worker" || !p.From.Equal(pgoFrom) || !p.To.Equal(pgoTo) {
		t.Errorf("query provenance not carried: %+v", p)
	}

	empty, err := ParsePGO(strings.NewReader(""), pgoQuery())
	if err != nil || len(empty.Edges) != 0 || !empty.Oldest.IsZero() || !empty.Newest.IsZero() {
		t.Errorf("empty result = %+v, %v; want no edges, zero hours, no error", empty, err)
	}
	if _, err := ParsePGO(strings.NewReader(" \n\t\n"), pgoQuery()); err != nil {
		t.Errorf("whitespace-only result refused: %v", err)
	}
}

// errAfter yields data, then fails: a connection that dies mid-answer.
type errAfter struct {
	data []byte
	err  error
}

func (r *errAfter) Read(b []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(b, r.data)
	r.data = r.data[n:]
	return n, nil
}

// ParsePGO reads to the end of the answer and refuses anything else: a
// partial or corrupt answer must never install a partial profile.
func TestParsePGORefusesPartialAnswers(t *testing.T) {
	row := `{"caller":"main.main","callee":"main.hot","offset":7,"weight":"9000"}` + "\n"
	for name, in := range map[string]string{
		"truncated row":     `{"caller":`,
		"stray ]":           row + "]",
		"stray }":           row + "}\n",
		"stray ] then rows": row + "]\n" + row,
		"trailing garbage":  row + "garbage",
		"null row":          row + "null\n",
		"array row":         row + `["main.main","main.hot",7,1]` + "\n",
		"string row":        row + `"x"` + "\n",
		"fractional offset": `{"caller":"main.main","callee":"main.hot","offset":7.9,"weight":"9000"}` + "\n",
		"fractional weight": `{"caller":"main.main","callee":"main.hot","offset":7,"weight":9000.5}` + "\n",
		"fractional exp":    `{"caller":"main.main","callee":"main.hot","offset":7,"weight":9.0005e3}` + "\n",
		"weight over int64": `{"caller":"main.main","callee":"main.hot","offset":7,"weight":1e19}` + "\n",
		"fractional hour":   `{"caller":"main.main","callee":"main.hot","offset":7,"weight":"9000","oldest":1761900000.5}` + "\n",
	} {
		if p, err := ParsePGO(strings.NewReader(in), pgoQuery()); err == nil {
			t.Errorf("%s: accepted as %+v", name, p.Edges)
		}
	}
	boom := errors.New("connection reset")
	if _, err := ParsePGO(&errAfter{data: []byte(row + row), err: boom}, pgoQuery()); !errors.Is(err, boom) {
		t.Errorf("a reader failing after two rows: %v, want its error", err)
	}
	if _, err := ParsePGO(&errAfter{data: []byte(row[:20]), err: boom}, pgoQuery()); !errors.Is(err, boom) {
		t.Errorf("a reader failing mid-row: %v, want its error", err)
	}
	if p, err := ParsePGO(&errAfter{data: []byte(row + row), err: io.EOF}, pgoQuery()); err != nil || len(p.Edges) != 1 || p.Edges[0].Weight != 18000 {
		t.Errorf("a clean end: %+v, %v", p, err)
	}
}

// A window over MaxEdges is refused, never cut to its heaviest edges: the
// SQL asks for one row more than the limit so the cut is visible.
func TestParsePGOEdgeLimit(t *testing.T) {
	var rows strings.Builder
	for i := range 3 {
		fmt.Fprintf(&rows, `{"caller":"main.main","callee":"main.f%d","offset":1,"weight":"10"}`+"\n", i)
	}
	q := pgoQuery()
	q.MaxEdges = 3
	if p, err := ParsePGO(strings.NewReader(rows.String()), q); err != nil || len(p.Edges) != 3 {
		t.Errorf("exactly MaxEdges: %+v, %v", p, err)
	}
	if _, params := q.SQL(); params["limit"] != "4" {
		t.Errorf("limit param = %q, want MaxEdges+1", params["limit"])
	}
	q.MaxEdges = 2
	_, err := ParsePGO(strings.NewReader(rows.String()), q)
	if !errors.Is(err, ErrTooManyEdges) || !strings.Contains(err.Error(), "--since") || !strings.Contains(err.Error(), "pgo.services") {
		t.Errorf("over MaxEdges: %v, want ErrTooManyEdges naming the fix", err)
	}
	q.MaxEdges = -1
	if err := q.Validate(); err == nil {
		t.Error("negative MaxEdges accepted")
	}
}

// A name `go tool preprofile` cannot carry drops its edge and is counted;
// the rest of the profile stands.
func TestParsePGODropsNamesPreprofileCannotCarry(t *testing.T) {
	long := strings.Repeat("x", MaxPGONameBytes+1)
	fits := strings.Repeat("y", MaxPGONameBytes)
	rows := fmt.Sprintf(`{"caller":"main.main","callee":"main.hot","offset":7,"weight":"9000"}
{"caller":"main.main","callee":"main.a\nb","offset":1,"weight":"5"}
{"caller":"main.c\rd","callee":"main.hot","offset":1,"weight":"5"}
{"caller":"main.main","callee":%q,"offset":2,"weight":"5"}
{"caller":"main.main","callee":%q,"offset":3,"weight":"4"}
`, long, fits)
	p, err := ParsePGO(strings.NewReader(rows), pgoQuery())
	if err != nil {
		t.Fatal(err)
	}
	if p.Dropped != 3 || len(p.Edges) != 2 || p.Edges[1].Callee != fits || p.TotalWeight() != 9004 {
		t.Errorf("dropped %d, edges %d (%d ns); want 3 dropped, the main.hot edge and the %d-byte name kept", p.Dropped, len(p.Edges), p.TotalWeight(), MaxPGONameBytes)
	}
}

func samplePGO() PGOProfile {
	return PGOProfile{
		Services: []string{"api", "worker"}, Namespace: "shop", Env: "prod", From: pgoFrom, To: pgoTo,
		Oldest: pgoFrom.Truncate(time.Hour), Newest: pgoTo.Truncate(time.Hour).Add(-time.Hour),
		Edges: []Edge{
			{Caller: "main.main", Callee: "main.hot", CallSiteOffset: 7, Weight: 9000},
			{Caller: "main.main", Callee: "main.hot", CallSiteOffset: 9, Weight: 800},
			{Caller: "main.fib", Callee: "main.fib", CallSiteOffset: 0, Weight: 70}, // recursion at offset 0
			{Caller: "main.stale", Callee: "main.hot", CallSiteOffset: -3, Weight: 6},
		},
	}
}

func TestWritePGOEncodesEdgesGoReads(t *testing.T) {
	in := samplePGO()
	var buf bytes.Buffer
	if err := WritePGO(&buf, in); err != nil {
		t.Fatal(err)
	}
	if b := buf.Bytes(); len(b) < 2 || b[0] != 0x1f || b[1] != 0x8b {
		t.Fatalf("output is not gzipped")
	}
	pp, err := profile.Parse(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(pp.SampleType) != 1 || pp.SampleType[0].Type != "cpu" || pp.SampleType[0].Unit != "nanoseconds" {
		t.Errorf("sample types = %v, want cpu/nanoseconds (one of the two Go's PGO reads)", pp.SampleType)
	}
	wantComment := `forge pgo v1 {"namespace":"shop","env":"prod","services":["api","worker"],"from":"2026-11-01T10:15:00Z","to":"2026-11-08T10:15:00Z",` +
		`"edges":4,"oldest":"2026-11-01T10:00:00Z","newest":"2026-11-08T09:00:00Z","total_weight":9876}`
	if len(pp.Comments) != 1 || pp.Comments[0] != wantComment {
		t.Errorf("comments = %q, want exactly %q", pp.Comments, wantComment)
	}
	if len(pp.Sample) != len(in.Edges) {
		t.Fatalf("%d samples, want one per edge (%d)", len(pp.Sample), len(in.Edges))
	}
	seen := map[string]bool{}
	for i, s := range pp.Sample {
		e := in.Edges[i]
		if len(s.Location) != 2 {
			t.Fatalf("sample %d has %d locations, want [callee, caller]", i, len(s.Location))
		}
		callee, caller := s.Location[0].Line[0], s.Location[1].Line[0]
		if callee.Function.Name != e.Callee || caller.Function.Name != e.Caller || s.Value[0] != e.Weight {
			t.Errorf("sample %d = %s -> %s (%d), want %+v", i, caller.Function.Name, callee.Function.Name, s.Value[0], e)
		}
		if caller.Function.StartLine == 0 {
			t.Errorf("sample %d: caller StartLine is 0; Go refuses a profile with no start lines", i)
		}
		if got := caller.Line - caller.Function.StartLine; got != int64(e.CallSiteOffset) {
			t.Errorf("sample %d: Line - StartLine = %d, want CallSiteOffset %d", i, got, e.CallSiteOffset)
		}
		if caller.Line < 1 {
			t.Errorf("sample %d: caller line %d, want >= 1", i, caller.Line)
		}
		// Go's graph merges nodes by (name, line, start line); the leaf must not
		// collide with the caller node or a recursive edge is dropped.
		if callee.Function.Name == caller.Function.Name && callee.Line == caller.Line {
			t.Errorf("sample %d: callee and caller frames are one node; Go drops self-edges", i)
		}
		seen[fmt.Sprint(s.Location[1].ID)] = true
	}
	if len(seen) != 4 {
		t.Errorf("%d distinct caller locations, want one per (caller, offset)", len(seen))
	}

	got, err := ReadPGO(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	sortEdges(in.Edges)
	if fmt.Sprint(got.Edges) != fmt.Sprint(in.Edges) || got.Env != in.Env || got.Namespace != "shop" || strings.Join(got.Services, ",") != "api,worker" ||
		!got.From.Equal(in.From) || !got.To.Equal(in.To) || !got.Oldest.Equal(in.Oldest) || !got.Newest.Equal(in.Newest) {
		t.Errorf("ReadPGO round trip:\n got %+v\nwant %+v", got, in)
	}
}

func TestWritePGORefusesBadEdgesAndWritesEmpty(t *testing.T) {
	for _, e := range []Edge{{Caller: "", Callee: "b", Weight: 1}, {Caller: "a", Callee: "b", Weight: 0}} {
		if err := WritePGO(&bytes.Buffer{}, PGOProfile{Edges: []Edge{e}}); err == nil {
			t.Errorf("WritePGO accepted edge %+v", e)
		}
	}
	var buf bytes.Buffer
	if err := WritePGO(&buf, PGOProfile{Services: []string{"api"}, From: pgoFrom, To: pgoTo}); err != nil {
		t.Fatalf("empty profile: %v", err)
	}
	got, err := ReadPGO(&buf)
	if err != nil || len(got.Edges) != 0 || got.Services[0] != "api" {
		t.Errorf("empty round trip = %+v, %v", got, err)
	}
}

// Provenance is one versioned JSON comment, so names a key=value line would
// split (spaces, commas, '=') round-trip; an edge whose name preprofile
// cannot carry is dropped and the count travels with the file.
func TestWritePGOProvenanceRoundTripsAnyNameAndCountsDrops(t *testing.T) {
	in := PGOProfile{
		Services:  []string{"api, v2", "worker=x y", `q"uote`},
		Namespace: "my shop", Env: "pre prod", From: pgoFrom, To: pgoTo, Dropped: 2,
		Edges: []Edge{
			{Caller: "main.main", Callee: "main.hot", CallSiteOffset: 7, Weight: 9000},
			{Caller: "main.main", Callee: "main.a\nb", CallSiteOffset: 1, Weight: 5},
			{Caller: strings.Repeat("z", MaxPGONameBytes+1), Callee: "main.hot", CallSiteOffset: 1, Weight: 5},
		},
	}
	var buf bytes.Buffer
	if err := WritePGO(&buf, in); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPGO(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Services, "|") != strings.Join(in.Services, "|") || got.Namespace != in.Namespace || got.Env != in.Env ||
		!got.From.Equal(in.From) || !got.To.Equal(in.To) {
		t.Errorf("provenance round trip: %+v", got)
	}
	if got.Dropped != 4 || len(got.Edges) != 1 || got.Edges[0].Callee != "main.hot" {
		t.Errorf("dropped %d, edges %+v; want the 2 already dropped plus the 2 unserializable edges, and main.main -> main.hot kept", got.Dropped, got.Edges)
	}
}

// Profiles written before the versioned comment still read; a newer
// version says so instead of passing as foreign or corrupt.
func TestReadPGOCommentVersions(t *testing.T) {
	write := func(comments ...string) *bytes.Buffer {
		pp, err := pgoPprof(samplePGO())
		if err != nil {
			t.Fatal(err)
		}
		pp.Comments = comments
		var buf bytes.Buffer
		if err := pp.Write(&buf); err != nil {
			t.Fatal(err)
		}
		return &buf
	}
	legacy, err := ReadPGO(write(
		"forge pgo: env=prod services=api,worker from=2026-11-01T10:15:00Z to=2026-11-08T10:15:00Z edges=4",
		"forge pgo data: oldest=2026-11-01T10:00:00Z newest=2026-11-08T09:00:00Z total_weight=9876"))
	if err != nil || legacy.Env != "prod" || strings.Join(legacy.Services, ",") != "api,worker" || !legacy.From.Equal(pgoFrom) ||
		!legacy.Newest.Equal(time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)) || len(legacy.Edges) != 4 {
		t.Errorf("legacy comment: %+v, %v", legacy, err)
	}
	if _, err := ReadPGO(write(`forge pgo v2 {"env":"prod"}`)); err == nil || errors.Is(err, ErrNotForgePGO) || !strings.Contains(err.Error(), "newer forge") {
		t.Errorf("v2 comment: %v, want a newer-forge error", err)
	}
	if _, err := ReadPGO(write(`forge pgo v1 {not json`)); err == nil || errors.Is(err, ErrNotForgePGO) {
		t.Errorf("corrupt v1 comment: %v, want a parse error", err)
	}
}

// zeros is an endless run of zero bytes.
type zeros struct{}

func (zeros) Read(b []byte) (int, error) {
	clear(b)
	return len(b), nil
}

// ReadPGO holds at most MaxPGOBytes, as stored and decompressed: a gzip bomb
// or an endless reader is refused, with the fix in the message.
func TestReadPGOBoundsItsInput(t *testing.T) {
	if _, err := ReadPGO(io.LimitReader(zeros{}, MaxPGOBytes+1)); err == nil || !strings.Contains(err.Error(), "as stored") || !strings.Contains(err.Error(), "--since") {
		t.Errorf("oversized file: %v", err)
	}
	var bomb bytes.Buffer
	zw := gzip.NewWriter(&bomb)
	if _, err := io.Copy(zw, io.LimitReader(zeros{}, MaxPGOBytes+1)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPGO(&bomb); err == nil || !strings.Contains(err.Error(), "decompressed") {
		t.Errorf("gzip bomb (%d bytes stored): %v", bomb.Len(), err)
	}
}

func TestReadPGORefusesForeignProfiles(t *testing.T) {
	var buf bytes.Buffer
	p := Profile{Service: "api", SampleType: "cpu", Unit: "nanoseconds", Stacks: []Stack{{Frames: []string{"main.main"}, Value: 1}}}
	if err := p.Pprof(&buf); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPGO(&buf); !errors.Is(err, ErrNotForgePGO) {
		t.Errorf("ReadPGO(obs profile output) = %v, want ErrNotForgePGO", err)
	}
	if _, err := ReadPGO(strings.NewReader("not a profile")); err == nil || errors.Is(err, ErrNotForgePGO) {
		t.Errorf("ReadPGO(garbage) = %v, want a parse error", err)
	}
}

// pgoProgram has one hot direct call (to a function too costly to inline
// without PGO) and one hot interface call (devirtualizable only with PGO),
// each marked so the test can compute its call-site offset.
const pgoProgram = `package main

import "fmt"

type shape interface{ area() int }

type sq struct{ n int }

func (s sq) area() int { return s.n * s.n }

type circ struct{ r int }

func (c circ) area() int { return 3 * c.r * c.r }

func hot(xs []int) int {
	s := 0
	for i := range xs {
		v := xs[i]
		if v%3 == 0 {
			s += v * 7
		} else if v%5 == 0 {
			s -= v / 3
		} else {
			s ^= v << 2
		}
		for j := 0; j < 4; j++ {
			s += (v + j) * (s % 11)
			s = s*31 + j
		}
		if s > 1<<40 {
			s %= 1000003
		}
		switch v & 7 {
		case 1:
			s += v * v
		case 2:
			s -= v * 13
		case 3:
			s ^= v * 17
		}
	}
	return s
}

func main() {
	xs := make([]int, 256)
	for i := range xs {
		xs[i] = i
	}
	shapes := []shape{sq{2}, circ{3}}
	total := 0
	for i := 0; i < 1000; i++ {
		total += hot(xs) // HOT_CALL
		total += shapes[i%1].area() // HOT_IFACE
	}
	fmt.Println(total)
}
`

// TestWritePGODropsWhatBreaksTheBuild proves MaxPGONameBytes and the CR/LF
// rule against the real toolchain: a raw profile carrying either kind of name
// fails `go build -pgo=` (go tool preprofile's newline-delimited output, read
// back by the compiler with a default bufio.Scanner), a name of exactly
// MaxPGONameBytes builds, and WritePGO's output of the same edges builds with
// the bad ones dropped and counted.
func TestWritePGODropsWhatBreaksTheBuild(t *testing.T) {
	testkit.Slow(t, "execs four go builds")
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	dir := t.TempDir()
	for name, body := range map[string]string{"go.mod": "module pgoprobe\n\ngo 1.21\n", "main.go": pgoProgram} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	build := func(profile string) (string, error) {
		cmd := exec.Command(goBin, "build", "-pgo="+profile, "-o", "probe", ".")
		cmd.Dir = dir
		cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=", "GOWORK=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0"))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	writeFile := func(name string, pp *profile.Profile) {
		var buf bytes.Buffer
		if err := pp.Write(&buf); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	callOff := int32(lineOf(t, pgoProgram, "HOT_CALL") - lineOf(t, pgoProgram, "func main() {"))
	fits := "main." + strings.Repeat("y", MaxPGONameBytes-len("main."))
	p := PGOProfile{Services: []string{"probe"}, From: pgoFrom, To: pgoTo, Edges: []Edge{
		{Caller: "main.main", Callee: "main.hot", CallSiteOffset: callOff, Weight: 9_000_000_000},
		{Caller: "main.main", Callee: fits, CallSiteOffset: 1, Weight: 1_000},
		{Caller: "main.main", Callee: "main.PLACEHOLDER", CallSiteOffset: 2, Weight: 1_000},
	}}

	// The raw rule: rename the placeholder to each bad name, bypassing WritePGO.
	for name, bad := range map[string]string{
		"too long": "main." + strings.Repeat("x", MaxPGONameBytes+1-len("main.")),
		"newline":  "main.a\nb",
	} {
		pp, err := pgoPprof(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range pp.Function {
			if f.Name == "main.PLACEHOLDER" {
				f.Name, f.SystemName = bad, bad
			}
		}
		writeFile("raw.pgo", pp)
		out, err := build("raw.pgo")
		switch {
		case err == nil:
			t.Errorf("%s: a profile carrying the name built; the rule WritePGO drops edges by is not what breaks the build", name)
		case !strings.Contains(out, "PGO error"):
			t.Errorf("%s: the build failed, but not on the profile:\n%s", name, out)
		}
	}

	// WritePGO: the fitting name stays, the bad ones go, and it builds.
	p.Edges = append(p.Edges[:2],
		Edge{Caller: "main.main", Callee: "main." + strings.Repeat("x", MaxPGONameBytes), CallSiteOffset: 2, Weight: 1_000},
		Edge{Caller: "main.a\nb", Callee: "main.hot", CallSiteOffset: 3, Weight: 1_000})
	f, err := os.Create(filepath.Join(dir, "default.pgo"))
	if err != nil {
		t.Fatal(err)
	}
	if err := WritePGO(f, p); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	back, err := os.Open(filepath.Join(dir, "default.pgo"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = back.Close() }()
	got, err := ReadPGO(back)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dropped != 2 || len(got.Edges) != 2 {
		t.Errorf("WritePGO kept %d edges, dropped %d; want 2 and 2", len(got.Edges), got.Dropped)
	}
	if out, err := build("default.pgo"); err != nil {
		t.Errorf("WritePGO's profile (a %d-byte name kept) does not build: %v\n%s", MaxPGONameBytes, err, out)
	}
}

func lineOf(t *testing.T, src, marker string) int {
	t.Helper()
	for i, l := range strings.Split(src, "\n") {
		if strings.Contains(l, marker) {
			return i + 1
		}
	}
	t.Fatalf("marker %q not in program", marker)
	return 0
}

// TestWritePGODrivesTheGoToolchain proves the encoding end to end: go tool
// preprofile reads the edges back with the offsets forge wrote, and a build
// with the profile makes PGO-only decisions (inlining past the normal budget,
// devirtualizing the interface call) that a build without it does not.
func TestWritePGODrivesTheGoToolchain(t *testing.T) {
	testkit.Slow(t, "execs go tool preprofile and two go builds")
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module pgoprobe\n\ngo 1.21\n")
	write("main.go", pgoProgram)
	mainLine := lineOf(t, pgoProgram, "func main() {")
	callOff := int32(lineOf(t, pgoProgram, "HOT_CALL") - mainLine)
	ifaceOff := int32(lineOf(t, pgoProgram, "HOT_IFACE") - mainLine)

	p := PGOProfile{Services: []string{"probe"}, Env: "test", From: pgoFrom, To: pgoTo, Edges: []Edge{
		{Caller: "main.main", Callee: "main.hot", CallSiteOffset: callOff, Weight: 9_000_000_000},
		{Caller: "main.main", Callee: "main.sq.area", CallSiteOffset: ifaceOff, Weight: 1_000_000_000},
	}}
	writePGO := func(name string, p PGOProfile) {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := WritePGO(f, p); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	writePGO("default.pgo", p)
	// The same edges one line off: a profile whose offsets the compiler
	// cannot match must change nothing, or the probe is not testing offsets.
	shifted := p
	shifted.Edges = nil
	for _, e := range p.Edges {
		e.CallSiteOffset++
		shifted.Edges = append(shifted.Edges, e)
	}
	writePGO("shifted.pgo", shifted)

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(goBin, args...)
		cmd.Dir = dir
		cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=", "GOWORK=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		var keep []string
		for _, l := range strings.Split(string(out), "\n") {
			if strings.Contains(l, "hot") || strings.Contains(l, "PGO") {
				keep = append(keep, l)
			}
		}
		t.Logf("go %s (lines naming hot/PGO):\n%s", strings.Join(args, " "), strings.Join(keep, "\n"))
		return string(out)
	}

	run("tool", "preprofile", "-i", "default.pgo", "-o", "pre.out")
	pre, err := os.ReadFile(filepath.Join(dir, "pre.out"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("preprofile output:\n%s", pre)
	for _, want := range []string{
		"GO PREPROFILE V1\n",
		fmt.Sprintf("main.main\nmain.hot\n%d 9000000000\n", callOff),
		fmt.Sprintf("main.main\nmain.sq.area\n%d 1000000000\n", ifaceOff),
	} {
		if !strings.Contains(string(pre), want) {
			t.Errorf("preprofile output lacks %q", want)
		}
	}

	off := run("build", "-pgo=off", "-gcflags=-m=2", "-o", "noprof", ".")
	if !strings.Contains(off, "cannot inline hot: function too complex") {
		t.Fatalf("without PGO main.hot should be over the inlining budget; the probe no longer tests anything")
	}
	if strings.Contains(off, "PGO devirtualizing") {
		t.Fatalf("devirtualized without a profile; the probe no longer tests anything")
	}
	miss := run("build", "-pgo=shifted.pgo", "-gcflags=-m -d=pgodebug=1", "-o", "shifted", ".")
	if strings.Contains(miss, "allows inlining for call main.hot") || strings.Contains(miss, "PGO devirtualizing") {
		t.Errorf("a profile with every offset one line off still drove PGO; the offsets are not what the compiler matches on")
	}
	on := run("build", "-pgo=default.pgo", "-gcflags=-m -d=pgodebug=1", "-o", "prof", ".")
	for _, want := range []string{
		"hot-budget check allows inlining for call main.hot",
		fmt.Sprintf("main.go:%d", mainLine+int(callOff)),
		"PGO devirtualizing interface call",
		"inlining call to hot",
	} {
		if !strings.Contains(on, want) {
			t.Errorf("PGO build output lacks %q", want)
		}
	}
}
