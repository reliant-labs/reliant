package obsquery

import (
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/profilequery"
)

// MaxStacks caps the distinct stacks one flamegraph returns (the heaviest are
// kept); the statement fetches one more to detect the cut.
const MaxStacks = 5000

const profileFormatSuffix = "FORMAT JSONEachRow"

// Flamegraph is a GetFlamegraph request.
type Flamegraph struct {
	Scope      Scope
	Service    string
	SampleType string
	TraceID    string
}

// Compile builds the stack statement from forge's profilequery (the SQL forge
// itself reads profiles back with), pointed at the scope's database and bounded
// to MaxStacks. Columns: frames (root to leaf), unit, value.
//
// profilequery's statements name the tables `default.*` (LocalDatabase) and end
// in FORMAT JSONEachRow, which is a client-side directive; both are rewritten
// here, and a statement that still names `default.` afterwards is refused
// rather than run — unless the scope is Local, where `default` is the database.
func (q Flamegraph) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeProfiles); err != nil {
		return Stmt{}, err
	}
	if q.Service == "" || len(q.Service) > 256 {
		return Stmt{}, invalid("service must be 1-256 characters")
	}
	if q.SampleType == "" || len(q.SampleType) > 128 {
		return Stmt{}, invalid("profile_type must be 1-128 characters")
	}
	pq := profilequery.Query{Service: q.Service, SampleType: q.SampleType, From: q.Scope.Start, To: q.Scope.End}
	if q.TraceID != "" {
		id, err := NormalizeTraceID(q.TraceID)
		if err != nil {
			return Stmt{}, err
		}
		pq.TraceID = id
	}
	sql, params := pq.SQL()
	sql = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sql), profileFormatSuffix))
	sql = strings.ReplaceAll(sql, "default.", q.Scope.Database+".")
	if (!q.Scope.Local && strings.Contains(sql, "default.")) || strings.Contains(sql, "FORMAT") {
		return Stmt{}, invalid("internal: profile statement was not rewritten to the scope's database")
	}
	return Stmt{SQL: sql + "\nLIMIT " + strconv.Itoa(MaxStacks+1), Params: params}, nil
}

// ProfileTypes lists the sample types a service has in the range. Columns:
// SampleType.
func (q Flamegraph) ProfileTypes() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeProfiles); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	conds := []string{b.rangeCondSec("Minute", q.Scope), "ServiceName = " + b.str(q.Service)}
	return b.stmt("SELECT DISTINCT SampleType FROM " + b.table(q.Scope, "profile_samples_1m") + "\n" +
		where(conds) + "\nORDER BY SampleType LIMIT 50"), nil
}

// ProfileRange is the half-open range a profile covers, for the Profile value.
func (q Flamegraph) ProfileRange() (time.Time, time.Time) { return q.Scope.Start, q.Scope.End }
