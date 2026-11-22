package templates

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/google/pprof/profile"

	"github.com/reliant-labs/forge/internal/goexec"
)

// The profile_pgo_edges materialized view, run against a real ClickHouse: rows
// shaped like the contrib clickhouse exporter's INSERT, carrying the two
// attributes pkg/observe attaches to CPU samples, must become exactly the call
// edges Go's PGO graph would count (internal/profile NewGraph: the two leaf
// locations only; per sample, once per distinct (caller node, callee node)
// pair, never a node with itself; a node is (address, line, name, start line)).
//
// Set FORGE_ALERT_CLICKHOUSE to a running container with clickhouse-client
// (the convention of internal/obsdash's ClickHouse tests), and
// FORGE_ALERT_CLICKHOUSE_PASSWORD when its password is not "pw". The image the
// ClickStack template runs has no password:
//
//	docker run -d --name ch mirror.gcr.io/clickhouse/clickstack-local:2.40.0
//	FORGE_ALERT_CLICKHOUSE=ch FORGE_ALERT_CLICKHOUSE_PASSWORD= task test -- ./internal/templates/ -run TestProfilePGOEdges
func pgoClickHouse(t *testing.T) (ch func(stdin, query string) string, db string) {
	t.Helper()
	container := os.Getenv("FORGE_ALERT_CLICKHOUSE")
	if testing.Short() || container == "" {
		t.Skip("needs a ClickHouse container; set FORGE_ALERT_CLICKHOUSE=<container> (runs in task test)")
	}
	password, ok := os.LookupEnv("FORGE_ALERT_CLICKHOUSE_PASSWORD")
	if !ok {
		password = "pw"
	}
	ch = func(stdin, query string) string {
		t.Helper()
		args := []string{"exec", "-i", container, "clickhouse-client", "--multiquery"}
		if password != "" {
			args = append(args, "--password", password)
		}
		if query != "" {
			args = append(args, "-q", query)
		}
		cmd := exec.Command("docker", args...)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("clickhouse-client: %v\n%s", err, out)
		}
		return string(out)
	}

	db = fmt.Sprintf("forge_pgo_%d_%s", os.Getpid(), strings.ToLower(strings.TrimPrefix(t.Name(), "TestProfilePGOEdges")))
	ch("", "DROP DATABASE IF EXISTS "+db+"; CREATE DATABASE "+db)
	t.Cleanup(func() { ch("", "DROP DATABASE IF EXISTS "+db) })
	sql, err := ProjectTemplates().Render("profiles-schema.sql.tmpl", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	schema := strings.ReplaceAll(string(sql), "default.", db+".")
	ch(schema, "")
	ch(schema, "") // the job re-applies it on every `forge env up`
	return ch, db
}

// pgoCols is the subset of the exporter's INSERT columns the view reads.
const pgoCols = "(Timestamp, SampleType, SampleUnit, ServiceName, ResourceAttributes, SampleAttributes, Addresses, FunctionNames, LineNumbers, Values)"

// pgoFrame is one flattened frame: what the exporter writes per frame, plus
// the start line pkg/observe adds.
type pgoFrame struct {
	addr  uint64
	name  string
	line  int64
	start int64
}

// pgoRow renders one exporter row of a CPU sample whose first leaf frames
// belong to its two leaf-most locations.
func pgoRow(ts, service, res string, frames []pgoFrame, leaf int, value int64) string {
	var addrs, names, lines, starts []string
	for _, f := range frames {
		addrs = append(addrs, strconv.FormatUint(f.addr, 10))
		names = append(names, "'"+f.name+"'")
		lines = append(lines, strconv.FormatInt(f.line, 10))
		starts = append(starts, strconv.FormatInt(f.start, 10))
	}
	attrs := fmt.Sprintf("{'forge.profile.start_lines':'%s','forge.profile.leaf_frames':'%d'}", strings.Join(starts, ","), leaf)
	return fmt.Sprintf("('2026-11-18 %s', 'cpu', 'nanoseconds', '%s', %s, %s, [%s], [%s], [%s], [%d])",
		ts, service, res, attrs, strings.Join(addrs, ","), strings.Join(names, ","), strings.Join(lines, ","), value)
}

func TestProfilePGOEdgesFromExporterRows(t *testing.T) {
	ch, db := pgoClickHouse(t)
	shop := `{'service.namespace':'shop','deployment.environment.name':'prod','service.version':'v1'}`
	other := `{'service.namespace':'other','deployment.environment.name':'prod','service.version':'v1'}`
	f := func(addr uint64, name string, line, start int64) pgoFrame { return pgoFrame{addr, name, line, start} }

	deep := pgoRow("10:05:00", "api", shop, []pgoFrame{
		f(0x10, "main.hash", 12, 10), f(0x20, "main.work", 25, 20), f(0x30, "main.main", 5, 3),
		f(0x40, "runtime.main", 283, 150), f(0x50, "runtime.goexit", 1700, 1690)}, 2, 30_000_000)
	rows := []string{
		// A deep stack: only main.work -> main.hash (its two leaf locations).
		deep,
		// An inlined leaf: main.inner inlined into main.work (one location,
		// one address), then main.main.
		pgoRow("10:06:00", "api", shop, []pgoFrame{
			f(0x60, "main.inner", 42, 40), f(0x60, "main.work", 10, 8), f(0x70, "main.main", 4, 2), f(0x40, "runtime.main", 283, 150)}, 3, 10_000_000),
		// Recursion: one name, two lines (two nodes) is an edge.
		pgoRow("10:07:00", "api", shop, []pgoFrame{f(0x80, "main.fib", 12, 10), f(0x90, "main.fib", 15, 10)}, 2, 300),
		// Recursion through one node (the leaf PC is the return address): a
		// node paired with itself is no edge.
		pgoRow("10:07:00", "api", shop, []pgoFrame{f(0xa0, "main.loop", 7, 5), f(0xa0, "main.loop", 7, 5)}, 2, 400),
		// One call reached through two different node pairs in one sample
		// counts twice (main.f -> main.g at offset 10, from 0xb0 and 0xc0);
		// main.g -> main.f once.
		pgoRow("10:08:00", "api", shop, []pgoFrame{
			f(0xb0, "main.g", 3, 1), f(0xb0, "main.f", 20, 10), f(0xc0, "main.g", 5, 1), f(0xc0, "main.f", 20, 10)}, 4, 11),
		// The same node pair twice in one sample counts once.
		pgoRow("10:08:00", "api", shop, []pgoFrame{
			f(0xd0, "main.x", 7, 5), f(0xd8, "main.y", 15, 12), f(0xd0, "main.x", 7, 5), f(0xd8, "main.y", 15, 12)}, 4, 7),
		// A caller with no start line (assembly) is kept at its absolute
		// line, as Go keeps it.
		pgoRow("10:09:00", "api", shop, []pgoFrame{f(0xe0, "main.a", 6, 5), f(0xe8, "main.asm", 9, 0)}, 2, 77),
		// An unsymbolized frame is no edge.
		pgoRow("10:10:00", "api", shop, []pgoFrame{f(0xf0, "", 0, 0), f(0x20, "main.work", 22, 20)}, 2, 66),
		// Another project's service of the same name stays apart.
		pgoRow("10:11:00", "api", other, []pgoFrame{f(0x10, "main.hash", 12, 10), f(0x20, "main.work", 25, 20)}, 2, 5),
		// Contribute nothing: no attributes (older producer), a heap row,
		// a CPU row in another unit, start lines / addresses / line numbers
		// that do not align with the names.
		fmt.Sprintf("('2026-11-18 10:12:00', 'cpu', 'nanoseconds', 'api', %s, {}, [1,2], ['main.old','main.caller'], [1,2], [99])", shop),
		fmt.Sprintf("('2026-11-18 10:12:00', 'inuse_space', 'bytes', 'api', %s, {'forge.profile.start_lines':'1,1','forge.profile.leaf_frames':'2'}, [1,2], ['main.alloc','main.caller'], [3,9], [4096])", shop),
		fmt.Sprintf("('2026-11-18 10:12:00', 'cpu', 'count', 'api', %s, {'forge.profile.start_lines':'1,1','forge.profile.leaf_frames':'2'}, [1,2], ['main.count','main.caller'], [3,9], [1])", shop),
		fmt.Sprintf("('2026-11-18 10:12:00', 'cpu', 'nanoseconds', 'api', %s, {'forge.profile.start_lines':'1,2,3','forge.profile.leaf_frames':'2'}, [1,2], ['main.short','main.starts'], [3,9], [55])", shop),
		fmt.Sprintf("('2026-11-18 10:12:00', 'cpu', 'nanoseconds', 'api', %s, {'forge.profile.start_lines':'1,2','forge.profile.leaf_frames':'2'}, [1], ['main.short','main.addrs'], [3,9], [56])", shop),
		fmt.Sprintf("('2026-11-18 10:12:00', 'cpu', 'nanoseconds', 'api', %s, {'forge.profile.start_lines':'1,2','forge.profile.leaf_frames':'2'}, [1,2], ['main.short','main.lines'], [3], [57])", shop),
	}
	ch("", "INSERT INTO "+db+".otel_profiles "+pgoCols+" VALUES "+strings.Join(rows, ", "))
	// A second insert in the same hour sums into the same edge.
	ch("", "INSERT INTO "+db+".otel_profiles "+pgoCols+" VALUES "+deep)

	got := ch("", "SELECT ServiceNamespace, Env, ServiceVersion, Hour, Caller, Callee, CallSiteOffset, sum(Weight) FROM "+db+
		".profile_pgo_edges GROUP BY ALL ORDER BY ALL FORMAT TSV")
	want := strings.Join([]string{
		"other\tprod\tv1\t2026-11-18 10:00:00\tmain.work\tmain.hash\t5\t5",
		"shop\tprod\tv1\t2026-11-18 10:00:00\tmain.asm\tmain.a\t9\t77",
		"shop\tprod\tv1\t2026-11-18 10:00:00\tmain.f\tmain.g\t10\t22",
		"shop\tprod\tv1\t2026-11-18 10:00:00\tmain.fib\tmain.fib\t5\t300",
		"shop\tprod\tv1\t2026-11-18 10:00:00\tmain.g\tmain.f\t4\t11",
		"shop\tprod\tv1\t2026-11-18 10:00:00\tmain.main\tmain.work\t2\t10000000",
		"shop\tprod\tv1\t2026-11-18 10:00:00\tmain.work\tmain.hash\t5\t60000000",
		"shop\tprod\tv1\t2026-11-18 10:00:00\tmain.work\tmain.inner\t2\t10000000",
		"shop\tprod\tv1\t2026-11-18 10:00:00\tmain.x\tmain.y\t2\t7",
		"shop\tprod\tv1\t2026-11-18 10:00:00\tmain.y\tmain.x\t3\t7",
	}, "\n") + "\n"
	if got != want {
		t.Errorf("profile_pgo_edges:\n%s\nwant:\n%s", got, want)
	}
}

// TestProfilePGOEdgesMatchGoPreprofile is the differential check: one CPU
// pprof holding every shape the view must get right is (a) flattened into
// exporter rows exactly as the clickhouse exporter and pkg/observe do and fed
// through the view, and (b) handed to `go tool preprofile`, Go's own reading.
// The two edge sets must be equal, except for edges with an unnamed endpoint,
// which the view leaves out on purpose.
func TestProfilePGOEdgesMatchGoPreprofile(t *testing.T) {
	ch, db := pgoClickHouse(t)
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}

	p := &profile.Profile{
		SampleType: []*profile.ValueType{{Type: "cpu", Unit: "nanoseconds"}},
		PeriodType: &profile.ValueType{Type: "cpu", Unit: "nanoseconds"},
		Period:     10_000_000,
	}
	fn := map[string]*profile.Function{}
	function := func(name string, start int64) *profile.Function {
		if f, ok := fn[name]; ok {
			return f
		}
		f := &profile.Function{ID: uint64(len(fn) + 1), Name: name, SystemName: name, StartLine: start}
		fn[name] = f
		p.Function = append(p.Function, f)
		return f
	}
	type ln struct {
		name        string
		line, start int64
	}
	loc := func(addr uint64, lines ...ln) *profile.Location {
		l := &profile.Location{ID: uint64(len(p.Location) + 1), Address: addr}
		for _, x := range lines {
			l.Line = append(l.Line, profile.Line{Function: function(x.name, x.start), Line: x.line})
		}
		p.Location = append(p.Location, l)
		return l
	}
	var (
		// an inlined leaf: main.inner inlined into main.work (pprof order:
		// inlinee first)
		inlined  = loc(0x1000, ln{"main.inner", 42, 40}, ln{"main.work", 10, 8})
		mainLoc  = loc(0x2000, ln{"main.main", 4, 2})
		rtMain   = loc(0x3000, ln{"runtime.main", 283, 150})
		goexit   = loc(0x3100, ln{"runtime.goexit", 1700, 1690})
		fibLeaf  = loc(0x4000, ln{"main.fib", 12, 10})
		fibCall  = loc(0x4100, ln{"main.fib", 15, 10})
		loop     = loc(0x5000, ln{"main.loop", 7, 5})
		loopCall = loc(0x5100, ln{"main.drive", 30, 28})
		twoA     = loc(0x6000, ln{"main.g", 3, 1}, ln{"main.f", 20, 10})
		twoB     = loc(0x6100, ln{"main.g", 5, 1}, ln{"main.f", 20, 10})
		unsym    = loc(0x7000)
		asm      = loc(0x8000, ln{"main.asm", 30, 0})
		hash     = loc(0x9000, ln{"main.hash", 12, 10})
		work     = loc(0x9100, ln{"main.work", 25, 8})
	)
	sample := func(v int64, locs ...*profile.Location) {
		p.Sample = append(p.Sample, &profile.Sample{Location: locs, Value: []int64{v}})
	}
	sample(30_000_000, hash, work, mainLoc, rtMain, goexit) // deep: only the two leaf locations count
	sample(10_000_000, inlined, mainLoc, rtMain)            // inlined leaf
	sample(20_000_000, inlined)                             // one location: its inlined pair only
	sample(300, fibLeaf, fibCall, mainLoc)                  // recursion through two nodes
	sample(400, loop, loop, loopCall)                       // one node twice: no self-edge
	sample(11, twoA, twoB)                                  // one call via two node pairs
	sample(77, fibLeaf, asm)                                // a caller without a start line
	sample(66, unsym, mainLoc)                              // unsymbolized leaf
	sample(55, mainLoc, unsym)                              // unsymbolized caller
	sample(0, hash, work)                                   // weightless
	if err := p.CheckValid(); err != nil {
		t.Fatal(err)
	}

	// (a) the view, over the rows the exporter would write.
	var rows []string
	for _, s := range p.Sample {
		var frames []pgoFrame
		leaf := 0
		for i, l := range s.Location {
			if len(l.Line) == 0 {
				frames = append(frames, pgoFrame{addr: l.Address})
			}
			for _, x := range l.Line {
				frames = append(frames, pgoFrame{l.Address, x.Function.Name, x.Line, x.Function.StartLine})
			}
			if i < 2 {
				leaf = len(frames)
			}
		}
		rows = append(rows, pgoRow("10:05:00", "diff", `{'service.namespace':'shop'}`, frames, leaf, s.Value[0]))
	}
	ch("", "INSERT INTO "+db+".otel_profiles "+pgoCols+" VALUES "+strings.Join(rows, ", "))
	var view []string
	for _, l := range strings.Split(strings.TrimSpace(ch("", "SELECT Caller, Callee, CallSiteOffset, sum(Weight) FROM "+db+
		".profile_pgo_edges GROUP BY ALL HAVING sum(Weight) > 0 FORMAT TSV")), "\n") {
		if l != "" {
			view = append(view, l)
		}
	}
	sort.Strings(view)

	// (b) Go's own reading.
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := p.Write(&buf); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cpu.pprof"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(goBin, "tool", "preprofile", "-i", "cpu.pprof", "-o", "pre.out")
	cmd.Dir = dir
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=", "GOWORK=off", "GOTOOLCHAIN=local"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go tool preprofile: %v\n%s", err, out)
	}
	pre, err := os.ReadFile(filepath.Join(dir, "pre.out"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(pre), "\n"), "\n")
	if len(lines) == 0 || lines[0] != "GO PREPROFILE V1" || (len(lines)-1)%3 != 0 {
		t.Fatalf("unexpected preprofile output:\n%s", pre)
	}
	var goEdges, unnamed []string
	for i := 1; i < len(lines); i += 3 {
		off, weight, _ := strings.Cut(lines[i+2], " ")
		e := strings.Join([]string{lines[i], lines[i+1], off, weight}, "\t")
		if lines[i] == "" || lines[i+1] == "" {
			unnamed = append(unnamed, e)
			continue
		}
		goEdges = append(goEdges, e)
	}
	sort.Strings(goEdges)
	t.Logf("go tool preprofile:\n%s\nleft out (unnamed endpoint): %q", strings.Join(goEdges, "\n"), unnamed)
	if strings.Join(view, "\n") != strings.Join(goEdges, "\n") {
		t.Errorf("view edges:\n%s\n\ngo tool preprofile edges:\n%s", strings.Join(view, "\n"), strings.Join(goEdges, "\n"))
	}
	if len(unnamed) != 2 {
		t.Errorf("preprofile reported %d edges with an unnamed endpoint, want the 2 the unsymbolized samples make: %q", len(unnamed), unnamed)
	}
}
