package profilequery

// Profile-guided optimization (PGO) profiles.
//
// `go build -pgo=<file>` (and the default -pgo=auto, which reads
// cmd/<bin>/default.pgo) consumes a CPU pprof profile, but it reads very little
// of it. What the toolchain needs was read from its own source
// (cmd/internal/pgo/pprof.go, internal/profile/graph.go and
// cmd/compile/internal/pgoir/irgraph.go, Go 1.27):
//
//   - Sample type: the FIRST sample type that is `samples/count` or
//     `cpu/nanoseconds` is the weight; a profile with neither is an error.
//     Weights are only ever compared as a share of the total (the hot
//     call-site CDF), so the unit does not matter beyond that name.
//   - Edges: the profile becomes a graph (internal/profile.NewGraph) whose
//     nodes are keyed by (address, line, function name, function start line)
//     and whose edges come from the two LEAF-MOST locations of each sample
//     only (and the inlined lines inside them): "frames higher up the stack
//     are unlikely to be repeated calls". A sample adds its weight once to
//     each distinct (parent node, child node) pair and never to a pair of one
//     node with itself. Each edge out of a node then becomes a
//     NamedCallEdge{caller name, callee name, node line - caller StartLine},
//     and edges that project to the same NamedCallEdge are summed.
//   - Start lines: at least one node must carry a non-zero Function.start_line
//     or the profile is refused ("profile missing Function.start_line data").
//   - Matching: the compiler keys an IR call site on (ir.LinkFuncName(caller),
//     ir.LinkFuncName(callee), line of the call - line of the caller's `func`
//     keyword). Those are the linker symbol names the Go runtime writes into
//     its own CPU profiles (main.hot, pkg.(*T).M), and the runtime's
//     Function.start_line is that same `func` line. File names, addresses
//     and mappings are never read: names and the line offset are the whole
//     key.
//   - Serialization: `go build` runs `go tool preprofile`, which writes each
//     edge as newline-separated text the compiler reads back with a default
//     bufio.Scanner (cmd/internal/pgo/serialize.go, deserialize.go). A name
//     holding a CR or LF, or one longer than the scanner's 64 KiB token
//     limit, breaks that file and with it the build. Such edges are dropped
//     and counted (PGOProfile.Dropped) rather than failing the whole profile.
//
// So WritePGO encodes each aggregated edge as one sample of exactly two
// locations, [callee, caller], weighted by its CPU nanoseconds, with the
// caller's Line - StartLine equal to the edge's CallSiteOffset. The callee
// frame's line is 0 (unknown): it is never a caller in these samples, and line
// 0 keeps its node distinct from every caller node, so a recursive edge at
// offset 0 is not mistaken for a self-edge and dropped.
//
// The data comes from default.profile_pgo_edges, which a materialized view
// fills from CPU samples that pkg/observe annotated with each frame's start
// line (ClickHouse's profile exporter does not store start lines). The view
// applies the node rules above to the exporter's frame-aligned arrays.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/google/pprof/profile"
)

// DefaultMaxPGOEdges is how many distinct call edges one PGO query reads
// unless PGOQuery.MaxEdges says otherwise. A window over it is refused, never
// truncated: a profile cut to its heaviest rows would still look complete.
const DefaultMaxPGOEdges = 200_000

// MaxPGOBytes bounds what ReadPGO reads: the file as stored (gzipped) and the
// pprof it decompresses to. Forge's own profiles are far smaller (a profile
// at DefaultMaxPGOEdges is a few MiB).
const MaxPGOBytes = 64 << 20

// MaxPGONameBytes is the longest function name a PGO edge may carry: `go
// tool preprofile` writes each name on its own line and the compiler reads
// them back with bufio.Scanner, whose token limit is bufio.MaxScanTokenSize
// including the newline.
const MaxPGONameBytes = bufio.MaxScanTokenSize - 1

// ErrTooManyEdges is ParsePGO's error for a window holding more call edges
// than the query's limit.
var ErrTooManyEdges = errors.New("too many call edges")

// PGOQuery selects the call edges to merge into one binary's PGO profile.
type PGOQuery struct {
	// Services are the OTEL service names to merge (required, at least one):
	// every workload that runs the binary.
	Services []string
	// Namespace filters on service.namespace (forge sets it to the project
	// name); "" reads every namespace. It keeps two projects that share a
	// service or env name apart.
	Namespace string
	// Env filters on deployment.environment.name; "" reads every env.
	Env string
	// From and To bound the window. The table is hourly, so the window is
	// widened to whole hours: every hour that overlaps [From, To) is read.
	From, To time.Time
	// MaxEdges is the most distinct edges the query may return; 0 means
	// DefaultMaxPGOEdges. More is ErrTooManyEdges, never a truncated profile.
	MaxEdges int
}

// Validate reports whether q can be run: at least one non-empty service, and
// From strictly before To.
func (q PGOQuery) Validate() error {
	if len(q.Services) == 0 {
		return errors.New("pgo query: no services named (name the workloads that run the binary)")
	}
	for _, s := range q.Services {
		if strings.TrimSpace(s) == "" {
			return errors.New("pgo query: empty service name")
		}
	}
	if q.From.IsZero() || q.To.IsZero() {
		return errors.New("pgo query: from and to are both required")
	}
	if !q.From.Before(q.To) {
		return fmt.Errorf("pgo query: from (%s) must be before to (%s)", q.From.UTC().Format(time.RFC3339), q.To.UTC().Format(time.RFC3339))
	}
	if q.MaxEdges < 0 {
		return fmt.Errorf("pgo query: max edges %d, want >= 0 (0 is the default, %d)", q.MaxEdges, DefaultMaxPGOEdges)
	}
	return nil
}

func (q PGOQuery) maxEdges() int {
	if q.MaxEdges > 0 {
		return q.MaxEdges
	}
	return DefaultMaxPGOEdges
}

// PGOEdgesSQL sums each (caller, callee, call-site offset) edge's weight
// across the named services and the hours that overlap the window, with the
// oldest and newest contributing hour (Unix seconds). It returns at most
// {limit} rows; SQL binds one more than the query's MaxEdges so ParsePGO can
// tell a window that fits from one that was cut. Output columns: caller,
// callee, offset, weight, oldest, newest.
const PGOEdgesSQL = `SELECT Caller AS caller, Callee AS callee, CallSiteOffset AS offset, sum(Weight) AS weight,
  toUInt32(min(Hour)) AS oldest, toUInt32(max(Hour)) AS newest
FROM default.profile_pgo_edges
WHERE has({services:Array(String)}, ServiceName)
  AND ({namespace:String} = '' OR ServiceNamespace = {namespace:String})
  AND ({env:String} = '' OR Env = {env:String})
  AND Hour >= toStartOfHour(toDateTime({from:UInt32})) AND Hour < toDateTime({to:UInt32})
  AND Caller != '' AND Callee != ''
GROUP BY caller, callee, offset
HAVING weight > 0
ORDER BY weight DESC, caller, callee, offset
LIMIT {limit:UInt32}
FORMAT JSONEachRow`

// SQL returns PGOEdgesSQL and the parameter values it binds. Callers pass the
// params to the client (clickhouse-client --param_<k>=<v>, or the driver's
// named parameters), never concatenate them. Run Validate first: SQL does not
// refuse an invalid query (one with no services matches no rows).
func (q PGOQuery) SQL() (string, map[string]string) {
	return PGOEdgesSQL, map[string]string{
		"services":  arrayParam(q.Services),
		"namespace": q.Namespace,
		"env":       q.Env,
		"from":      fmt.Sprint(q.From.Unix()),
		"to":        fmt.Sprint(q.To.Unix()),
		"limit":     fmt.Sprint(q.maxEdges() + 1),
	}
}

// arrayParam renders an Array(String) query parameter in ClickHouse's text
// format: ['a','b'], with backslashes and quotes escaped.
func arrayParam(vals []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range vals {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('\'')
		b.WriteString(strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v))
		b.WriteByte('\'')
	}
	b.WriteByte(']')
	return b.String()
}

// Edge is one call edge: Caller calls Callee at the line CallSiteOffset lines
// below Caller's `func` line, with Weight CPU nanoseconds of samples whose two
// leaf-most locations hold that call.
type Edge struct {
	Caller, Callee string
	CallSiteOffset int32
	Weight         int64
}

// PGOProfile is the merged edge set of one binary over a window.
type PGOProfile struct {
	Services  []string
	Namespace string
	Env       string
	From, To  time.Time
	// Edges, heaviest first (ties by caller, callee, offset).
	Edges []Edge
	// Dropped counts edges left out because a function name cannot travel
	// through `go tool preprofile` (a CR or LF in it, or longer than
	// MaxPGONameBytes). One odd symbol must not cost the whole profile.
	Dropped int
	// Oldest and Newest are the start of the oldest and newest hour that
	// contributed an edge; zero when there are no edges.
	Oldest, Newest time.Time
}

// TotalWeight is the sum of every edge's weight: CPU nanoseconds summed over
// call edges, the total Go's PGO measures each edge's share against. It is
// not the CPU time the window sampled: a sample counts once per distinct
// edge among its two leaf-most locations' frames, so one sample can count
// several times (inlined frames) or not at all (a leaf with no caller).
func (p PGOProfile) TotalWeight() int64 {
	var n int64
	for _, e := range p.Edges {
		n += e.Weight
	}
	return n
}

// pgoNameOK reports whether name survives `go tool preprofile`'s
// line-oriented serialization.
func pgoNameOK(name string) bool {
	return len(name) <= MaxPGONameBytes && !strings.ContainsAny(name, "\r\n")
}

// ParsePGO reads the JSONEachRow output of q's SQL. Rows with an empty name or
// no weight are skipped, rows with a name Go's toolchain cannot carry are
// dropped and counted (PGOProfile.Dropped), and duplicate edges are summed, so
// the result is the same however the rows were grouped. More rows than q's
// MaxEdges is ErrTooManyEdges; anything but well-formed rows up to the end of
// r is an error, so a truncated or corrupt answer never becomes a profile.
func ParsePGO(r io.Reader, q PGOQuery) (PGOProfile, error) {
	if err := q.Validate(); err != nil {
		return PGOProfile{}, err
	}
	p := PGOProfile{Services: append([]string(nil), q.Services...), Namespace: q.Namespace, Env: q.Env, From: q.From, To: q.To}
	type key struct {
		caller, callee string
		offset         int32
	}
	type pgoRow struct {
		Caller string      `json:"caller"`
		Callee string      `json:"callee"`
		Offset json.Number `json:"offset"`
		Weight json.Number `json:"weight"`
		Oldest json.Number `json:"oldest"`
		Newest json.Number `json:"newest"`
	}
	idx := map[key]int{}
	var oldest, newest int64
	limit := q.maxEdges()
	dec := json.NewDecoder(r)
	for rows := 1; ; rows++ {
		var row *pgoRow
		err := dec.Decode(&row)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return PGOProfile{}, fmt.Errorf("decode ClickHouse row %d: %w", rows, err)
		}
		if row == nil {
			return PGOProfile{}, fmt.Errorf("decode ClickHouse row %d: null, want an object", rows)
		}
		if rows > limit {
			return PGOProfile{}, fmt.Errorf("pgo query: %w: the window holds more than %d; narrow it — a shorter window (forge pgo pull --since) or fewer services merged into the binary (pgo.services)", ErrTooManyEdges, limit)
		}
		w, err := jsonInt(row.Weight)
		if err != nil {
			return PGOProfile{}, fmt.Errorf("decode weight: %w", err)
		}
		if row.Caller == "" || row.Callee == "" || w <= 0 {
			continue
		}
		if !pgoNameOK(row.Caller) || !pgoNameOK(row.Callee) {
			p.Dropped++
			continue
		}
		off, err := jsonInt(row.Offset)
		if err != nil {
			return PGOProfile{}, fmt.Errorf("decode offset: %w", err)
		}
		if off < -1<<31 || off > 1<<31-1 {
			return PGOProfile{}, fmt.Errorf("decode offset: %d out of range", off)
		}
		for _, h := range []struct {
			n   json.Number
			dst *int64
			min bool
		}{{row.Oldest, &oldest, true}, {row.Newest, &newest, false}} {
			if h.n == "" {
				continue
			}
			v, err := jsonInt(h.n)
			if err != nil {
				return PGOProfile{}, fmt.Errorf("decode hour: %w", err)
			}
			if v <= 0 {
				continue
			}
			if *h.dst == 0 || (h.min && v < *h.dst) || (!h.min && v > *h.dst) {
				*h.dst = v
			}
		}
		k := key{row.Caller, row.Callee, int32(off)}
		if i, ok := idx[k]; ok {
			p.Edges[i].Weight += w
			continue
		}
		idx[k] = len(p.Edges)
		p.Edges = append(p.Edges, Edge{Caller: row.Caller, Callee: row.Callee, CallSiteOffset: int32(off), Weight: w})
	}
	sortEdges(p.Edges)
	if len(p.Edges) > 0 {
		if oldest > 0 {
			p.Oldest = time.Unix(oldest, 0).UTC()
		}
		if newest > 0 {
			p.Newest = time.Unix(newest, 0).UTC()
		}
	}
	return p, nil
}

// jsonInt reads an exact integer. Every column PGOEdgesSQL selects is an
// integer type; a number written in float form ("500.0", "9e3") is accepted
// only when its value is exactly an int64. It is parsed as a rational, never
// through float64, so a fractional value (a corrupt answer) is refused rather
// than truncated into a different edge, and a large weight keeps every digit.
func jsonInt(n json.Number) (int64, error) {
	if n == "" {
		return 0, nil
	}
	if v, err := n.Int64(); err == nil {
		return v, nil
	}
	r, ok := new(big.Rat).SetString(n.String())
	if !ok {
		return 0, fmt.Errorf("%q is not a number", n)
	}
	if !r.IsInt() || !r.Num().IsInt64() {
		return 0, fmt.Errorf("%q is not an exact 64-bit integer", n)
	}
	return r.Num().Int64(), nil
}

func sortEdges(es []Edge) {
	sort.SliceStable(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if a.Weight != b.Weight {
			return a.Weight > b.Weight
		}
		if a.Caller != b.Caller {
			return a.Caller < b.Caller
		}
		if a.Callee != b.Callee {
			return a.Callee < b.Callee
		}
		return a.CallSiteOffset < b.CallSiteOffset
	})
}

// pgoProvenancePrefix starts the provenance comment WritePGO records: a
// version, then one JSON object (pgoProvenance), so any service name, env or
// namespace round-trips.
const (
	pgoProvenancePrefix = "forge pgo v1 "
	pgoVersionPrefix    = "forge pgo v"
)

// pgoProvenance is the JSON body of the provenance comment.
type pgoProvenance struct {
	Namespace   string   `json:"namespace,omitempty"`
	Env         string   `json:"env"`
	Services    []string `json:"services"`
	From        string   `json:"from,omitempty"`
	To          string   `json:"to,omitempty"`
	Edges       int      `json:"edges"`
	Dropped     int      `json:"dropped,omitempty"`
	Oldest      string   `json:"oldest,omitempty"`
	Newest      string   `json:"newest,omitempty"`
	TotalWeight int64    `json:"total_weight"`
}

// WritePGO writes p as a gzipped pprof CPU profile that `go build -pgo=` (and
// -pgo=auto's cmd/<bin>/default.pgo) and `go tool preprofile` accept; see the
// comment at the top of this file for the encoding and why. An edge whose
// function name `go tool preprofile` cannot carry is left out and added to
// the Dropped count. One comment records provenance:
//
//	forge pgo v1 {"namespace":…,"env":…,"services":[…],"from":…,"to":…,"edges":n,"dropped":n,"oldest":…,"newest":…,"total_weight":n}
//
// ReadPGO reads it back. A profile with no edges is written too (Go accepts
// and ignores it); whether that is worth writing is the caller's decision.
func WritePGO(w io.Writer, p PGOProfile) error {
	pp, err := pgoPprof(p)
	if err != nil {
		return err
	}
	return pp.Write(w)
}

func pgoPprof(p PGOProfile) (*profile.Profile, error) {
	var edges []Edge
	dropped := p.Dropped
	for _, e := range p.Edges {
		if e.Caller == "" || e.Callee == "" {
			return nil, fmt.Errorf("pgo profile: edge %+v has an empty function name", e)
		}
		if e.Weight <= 0 {
			return nil, fmt.Errorf("pgo profile: edge %s -> %s has weight %d, want > 0", e.Caller, e.Callee, e.Weight)
		}
		if !pgoNameOK(e.Caller) || !pgoNameOK(e.Callee) {
			dropped++
			continue
		}
		edges = append(edges, e)
	}
	kept := p
	kept.Edges, kept.Dropped = edges, dropped
	prov, err := json.Marshal(pgoProvenance{
		Namespace: p.Namespace, Env: p.Env, Services: append([]string{}, p.Services...),
		From: rfc3339(p.From), To: rfc3339(p.To), Edges: len(edges), Dropped: dropped,
		Oldest: rfc3339(p.Oldest), Newest: rfc3339(p.Newest), TotalWeight: kept.TotalWeight(),
	})
	if err != nil {
		return nil, fmt.Errorf("pgo profile: provenance: %w", err)
	}
	cpu := &profile.ValueType{Type: "cpu", Unit: "nanoseconds"}
	pp := &profile.Profile{
		SampleType: []*profile.ValueType{cpu},
		PeriodType: &profile.ValueType{Type: "cpu", Unit: "nanoseconds"},
		Period:     1,
		Comments:   []string{pgoProvenancePrefix + string(prov)},
	}
	if !p.From.IsZero() {
		pp.TimeNanos = p.From.UnixNano()
		if p.To.After(p.From) {
			pp.DurationNanos = p.To.Sub(p.From).Nanoseconds()
		}
	}

	// One Function per name. Its StartLine must be non-zero (Go refuses a
	// profile with none) and leave every caller line >= 1, so a negative
	// offset (a stale profile) still encodes.
	minOff := map[string]int64{}
	for _, e := range edges {
		if m, ok := minOff[e.Caller]; !ok || int64(e.CallSiteOffset) < m {
			minOff[e.Caller] = int64(e.CallSiteOffset)
		}
	}
	funcs := map[string]*profile.Function{}
	fn := func(name string) *profile.Function {
		if f, ok := funcs[name]; ok {
			return f
		}
		start := int64(1)
		if m := minOff[name]; m < 0 {
			start = 1 - m
		}
		f := &profile.Function{ID: uint64(len(pp.Function) + 1), Name: name, SystemName: name, StartLine: start}
		pp.Function = append(pp.Function, f)
		funcs[name] = f
		return f
	}
	type site struct {
		name string
		off  int32
	}
	callers := map[site]*profile.Location{}
	leaves := map[string]*profile.Location{}
	newLoc := func(f *profile.Function, line int64) *profile.Location {
		l := &profile.Location{ID: uint64(len(pp.Location) + 1), Line: []profile.Line{{Function: f, Line: line}}}
		pp.Location = append(pp.Location, l)
		return l
	}
	for _, e := range edges {
		callee, ok := leaves[e.Callee]
		if !ok {
			callee = newLoc(fn(e.Callee), 0)
			leaves[e.Callee] = callee
		}
		s := site{e.Caller, e.CallSiteOffset}
		caller, ok := callers[s]
		if !ok {
			f := fn(e.Caller)
			caller = newLoc(f, f.StartLine+int64(e.CallSiteOffset))
			callers[s] = caller
		}
		pp.Sample = append(pp.Sample, &profile.Sample{Location: []*profile.Location{callee, caller}, Value: []int64{e.Weight}})
	}
	if err := pp.CheckValid(); err != nil {
		return nil, fmt.Errorf("build pgo pprof: %w", err)
	}
	return pp, nil
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ReadPGO reads a profile WritePGO wrote back: its edges and the provenance
// its comments recorded. A pprof forge did not write (a profile captured by
// hand, say) is refused with ErrNotForgePGO, so a caller can report it as
// "not forge's" rather than as corrupt. It reads at most MaxPGOBytes, both as
// stored and decompressed.
func ReadPGO(r io.Reader) (PGOProfile, error) {
	data, err := readPGOBytes(r)
	if err != nil {
		return PGOProfile{}, err
	}
	pp, err := profile.ParseData(data)
	if err != nil {
		return PGOProfile{}, fmt.Errorf("parse pgo profile: %w", err)
	}
	var p PGOProfile
	found := false
	for _, c := range pp.Comments {
		switch {
		case strings.HasPrefix(c, pgoProvenancePrefix):
			var prov pgoProvenance
			if err := json.Unmarshal([]byte(strings.TrimPrefix(c, pgoProvenancePrefix)), &prov); err != nil {
				return PGOProfile{}, fmt.Errorf("parse pgo profile: provenance comment: %w", err)
			}
			found = true
			p.Namespace, p.Env, p.Dropped = prov.Namespace, prov.Env, prov.Dropped
			if len(prov.Services) > 0 {
				p.Services = prov.Services
			}
			p.From, p.To = parseRFC3339(prov.From), parseRFC3339(prov.To)
			p.Oldest, p.Newest = parseRFC3339(prov.Oldest), parseRFC3339(prov.Newest)
		case strings.HasPrefix(c, pgoVersionPrefix):
			v, _, _ := strings.Cut(strings.TrimPrefix(c, pgoVersionPrefix), " ")
			return PGOProfile{}, fmt.Errorf("parse pgo profile: written by a newer forge (provenance v%s, this forge reads v1); upgrade forge", v)
		}
	}
	if !found {
		return PGOProfile{}, ErrNotForgePGO
	}
	vi := -1
	for i, st := range pp.SampleType {
		if st.Type == "cpu" && st.Unit == "nanoseconds" {
			vi = i
			break
		}
	}
	if vi < 0 {
		return PGOProfile{}, errors.New("parse pgo profile: no cpu/nanoseconds sample type")
	}
	for _, s := range pp.Sample {
		if len(s.Location) != 2 || len(s.Location[0].Line) != 1 || len(s.Location[1].Line) != 1 {
			return PGOProfile{}, errors.New("parse pgo profile: a sample is not a [callee, caller] pair")
		}
		callee, caller := s.Location[0].Line[0], s.Location[1].Line[0]
		if callee.Function == nil || caller.Function == nil {
			return PGOProfile{}, errors.New("parse pgo profile: a sample frame has no function")
		}
		p.Edges = append(p.Edges, Edge{
			Caller:         caller.Function.Name,
			Callee:         callee.Function.Name,
			CallSiteOffset: int32(caller.Line - caller.Function.StartLine),
			Weight:         s.Value[vi],
		})
	}
	sortEdges(p.Edges)
	return p, nil
}

// readPGOBytes reads r whole, gunzipping it when it is gzipped, and refuses
// either form over MaxPGOBytes rather than holding it in memory.
func readPGOBytes(r io.Reader) ([]byte, error) {
	tooBig := func(what string) error {
		return fmt.Errorf("pgo profile is over %d MiB %s, far more than forge writes; it is not a forge profile, or it merges too much — pull it again with a shorter --since or for one --binary", MaxPGOBytes>>20, what)
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxPGOBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read pgo profile: %w", err)
	}
	if len(data) > MaxPGOBytes {
		return nil, tooBig("as stored")
	}
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		return data, nil
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse pgo profile: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(gz, MaxPGOBytes+1))
	if err != nil {
		return nil, fmt.Errorf("parse pgo profile: decompress: %w", err)
	}
	if len(raw) > MaxPGOBytes {
		return nil, tooBig("decompressed")
	}
	return raw, nil
}

// ErrNotForgePGO is ReadPGO's error for a pprof file without forge's
// provenance comment.
var ErrNotForgePGO = errors.New("not a forge pgo profile (no `forge pgo` provenance comment)")

func parseRFC3339(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
