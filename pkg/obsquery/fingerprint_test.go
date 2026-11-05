package obsquery

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// rec builds an ERROR record logged at fn.
func rec(service, fn, body string, attrs ...string) ServerErrorRecord {
	a := map[string]string{LogKeyCodeFunction: fn, LogKeyCodeFile: "internal/x/x.go", "code.line.number": "42"}
	for i := 0; i+1 < len(attrs); i += 2 {
		a[attrs[i]] = attrs[i+1]
	}
	return ServerErrorRecord{ServiceName: service, ScopeName: "github.com/acme/app", Body: body, Attributes: a}
}

// The keys sfp1 reads are the keys forge's SDK writes.
func TestFingerprintKeysAreTheSDKs(t *testing.T) {
	for got, want := range map[string]string{
		LogKeyCodeFunction:     string(semconv.CodeFunctionNameKey),
		LogKeyCodeFile:         string(semconv.CodeFilePathKey),
		LogKeyExceptionType:    observe.LogKeyExceptionType,
		LogKeyExceptionMessage: observe.LogKeyExceptionMessage,
		LogKeyErrorGroup:       observe.LogKeyErrorGroup,
		LogKeyIssueSource:      observe.LogKeyIssueSource,
	} {
		if got != want {
			t.Errorf("key %q, SDK writes %q", got, want)
		}
	}
}

func TestServerFingerprintGrouping(t *testing.T) {
	const fn = "github.com/acme/app/internal/handlers/jobs.(*Service).ListJobs"
	pg := []string{LogKeyExceptionType, "*pgconn.PgError", LogKeyExceptionMessage, "ERROR: relation \"jobs\" does not exist (SQLSTATE 42P01)"}
	base := rec("api", fn, "list jobs failed", pg...)

	same := map[string]ServerErrorRecord{
		"another line number": func() ServerErrorRecord {
			r := rec("api", fn, "list jobs failed", pg...)
			r.Attributes["code.line.number"] = "99"
			return r
		}(),
		"another file path": func() ServerErrorRecord {
			r := rec("api", fn, "list jobs failed", pg...)
			r.Attributes[LogKeyCodeFile] = "moved/x.go"
			return r
		}(),
		"a non-generic error's message differs": rec("api", fn, "list jobs failed",
			LogKeyExceptionType, "*pgconn.PgError", LogKeyExceptionMessage, "something else entirely"),
	}
	for name, r := range same {
		if ServerFingerprint(r) != ServerFingerprint(base) {
			t.Errorf("%s: split into a second issue", name)
		}
	}

	// The same call site with ids that change on every event is one issue.
	a := rec("api", fn, "job 1234 for crew 7f3a9c21-0000-4000-8000-000000000001 failed at https://x.example/a?b=1", pg...)
	b := rec("api", fn, "job 98 for crew 11111111-2222-4333-8444-555555555555 failed at https://y.example/z", pg...)
	if ServerFingerprint(a) != ServerFingerprint(b) {
		t.Errorf("changing ids split one call site into two issues:\n%q\n%q", NormalizeMessage(a.Body), NormalizeMessage(b.Body))
	}

	different := map[string]ServerErrorRecord{
		"another service":  rec("worker", fn, "list jobs failed", pg...),
		"another function": rec("api", fn+"2", "list jobs failed", pg...),
		"another message":  rec("api", fn, "count jobs failed", pg...),
		"another type": rec("api", fn, "list jobs failed",
			LogKeyExceptionType, "context.deadlineExceededError"),
		"rpc failed in another procedure": rec("api", fn, "list jobs failed",
			append([]string{LogKeyProcedure, "/jobs.v1.JobService/ListJobs"}, pg...)...),
	}
	seen := map[string]string{ServerFingerprint(base): "base"}
	for name, r := range different {
		fp := ServerFingerprint(r)
		if prev, dup := seen[fp]; dup {
			t.Errorf("%s: same fingerprint as %s", name, prev)
		}
		seen[fp] = name
	}

	// errors.New / fmt.Errorf carry only their message, so the message decides.
	e1 := rec("api", fn, "reconcile failed", LogKeyExceptionType, "*errors.errorString", LogKeyExceptionMessage, "ledger drift")
	e2 := rec("api", fn, "reconcile failed", LogKeyExceptionType, "*errors.errorString", LogKeyExceptionMessage, "missing invoice")
	if ServerFingerprint(e1) == ServerFingerprint(e2) {
		t.Error("two errors.New messages at one site merged into one issue")
	}

	// error.group pins the issue whatever else changes, within a service.
	g1 := rec("api", fn, "a", LogKeyErrorGroup, "billing-drift")
	g2 := rec("api", fn+"X", "b", LogKeyErrorGroup, "billing-drift", LogKeyExceptionType, "*x.Y")
	if ServerFingerprint(g1) != ServerFingerprint(g2) {
		t.Error("error.group did not pin the issue")
	}
	if ServerFingerprint(g1) == ServerFingerprint(rec("worker", fn, "a", LogKeyErrorGroup, "billing-drift")) {
		t.Error("error.group merged two services")
	}

	// No source location: the instrumentation scope stands in for the function.
	n1 := ServerErrorRecord{ServiceName: "api", ScopeName: "otelcol", Body: "export failed"}
	n2 := ServerErrorRecord{ServiceName: "api", ScopeName: "temporal", Body: "export failed"}
	if ServerFingerprint(n1) == ServerFingerprint(n2) {
		t.Error("ScopeName is not the fallback for a record with no function")
	}
}

// The algorithm is frozen: stored issues are keyed by it. A change to this
// vector is a re-key of every existing issue; add a new algorithm instead.
func TestServerFingerprintIsStable(t *testing.T) {
	r := rec("api", "github.com/acme/app/internal/handlers/jobs.(*Service).ListJobs", "list jobs failed",
		LogKeyExceptionType, "*pgconn.PgError", LogKeyProcedure, "/jobs.v1.JobService/ListJobs")
	const want = "46eff145a9e76695"
	if got := ServerFingerprint(r); got != want {
		t.Fatalf("sfp1 changed: %s, want %s. That re-keys every stored issue; refine in a new version instead of editing it.", got, want)
	}
	if len(want) != 16 {
		t.Fatal("sfp1 is 8 bytes of hex")
	}
}

// sfp2 refines sfp1, it does not re-key it: every record it does not refine keeps
// the exact sfp1 fingerprint it had, so an upgrade splits no existing issue. The
// vectors were computed by an independent implementation (SHA-256 over the
// documented key), not by this package.
func TestServerFingerprintSfp2LeavesUnrefinedRecordsOnSfp1(t *testing.T) {
	const fn = "github.com/acme/app/internal/handlers/jobs.(*Service).ListJobs"
	const proc = "/jobs.v1.JobService/ListJobs"
	for name, c := range map[string]struct {
		r    ServerErrorRecord
		want string
	}{
		"typed error":               {rec("api", fn, "list jobs failed", LogKeyExceptionType, "*pgconn.PgError", LogKeyProcedure, proc), "46eff145a9e76695"},
		"no error attribute at all": {rec("api", fn, "Transaction failed", LogKeyProcedure, proc), "c6192961f4e147b4"},
		// A typed record that also carries the error text is not refined: its
		// type already says what kind of failure it is.
		"typed error that also logs error": {rec("api", fn, "list jobs failed", LogKeyExceptionType, "*pgconn.PgError", LogKeyProcedure, proc,
			LogKeyError, "ERROR: x (SQLSTATE 40001)"), "46eff145a9e76695"},
		// error.group pins the issue whatever the cause says.
		"pinned": {rec("api", fn, "a", LogKeyErrorGroup, "billing-drift", LogKeyError, "boom"),
			ServerFingerprint(rec("api", fn, "a", LogKeyErrorGroup, "billing-drift"))},
	} {
		if got := ServerFingerprint(c.r); got != c.want {
			t.Errorf("%s: fingerprint %s, want the sfp1 value %s", name, got, c.want)
		}
		if c.r.RefinedByErrorClass() {
			t.Errorf("%s: must not be refined by the error class", name)
		}
	}

	// The refined records, frozen. Same site, same template, different cause.
	for cause, want := range map[string]string{
		"ERROR: could not serialize access (SQLSTATE 40001)":                           "3804f8cb238dfd77",
		`ERROR: invalid byte sequence for encoding "UTF8": 0xe2 0x2e (SQLSTATE 22021)`: "c44875060c2e8923",
	} {
		r := rec("api", fn, "Transaction failed", LogKeyProcedure, proc, LogKeyError, cause)
		if !r.RefinedByErrorClass() {
			t.Fatalf("%q: not refined", cause)
		}
		if got := ServerFingerprint(r); got != want {
			t.Errorf("%q: sfp2 changed: %s, want %s. A different value is a re-key; add sfp3 instead of editing sfp2.", cause, got, want)
		}
	}
}

func TestErrorClass(t *testing.T) {
	for in, want := range map[string]string{
		"*pgconn.PgError: ERROR: could not serialize access due to concurrent update (SQLSTATE 40001)": "*pgconn.PgError SQLSTATE 40001",
		"ERROR: could not serialize access due to concurrent update (SQLSTATE 40001)":                  "SQLSTATE 40001",
		"begin tx: ERROR: prepared statement \"stmtcache_7f\" already exists (SQLSTATE 42P05)":         "SQLSTATE 42P05",
		"failed to connect to `host=db user=app`: dial tcp 10.0.0.5:5432: connect: connection refused": "failed to connect to `host=db user=app`: dial tcp <n>.<n>.<n>.<n>:<n>: connect: connection refused",
		"*net.OpError: read tcp 10.1.2.3:41234->10.0.0.5:5432: i/o timeout":                            "*net.OpError",
		"*errors.errorString: ledger drift on 12 rows":                                                 "*errors.errorString: ledger drift on <n> rows",
		"context deadline exceeded": "context deadline exceeded",
		"job 1234 failed":           "job <n> failed",
		"":                          "",
	} {
		if got := ErrorClass(in); got != want {
			t.Errorf("ErrorClass(%q) = %q, want %q", in, got, want)
		}
	}
}

// T7: reliant's slog lines carry the cause in `error` with no exception.type, so
// "Transaction failed" used to be ONE issue for every cause.
func TestServerFingerprintSeparatesTheCausesOfOneMessage(t *testing.T) {
	const fn = "github.com/acme/reliant/internal/store.(*DB).RunTx"
	tx := func(cause string) ServerErrorRecord {
		return rec("reliant-temporal-worker", fn, "Transaction failed", LogKeyError, cause)
	}

	causes := map[string]string{
		"utf8":          `ERROR: invalid byte sequence for encoding "UTF8": 0xe2 0x2e 0x2e (SQLSTATE 22021)`,
		"serialization": "ERROR: could not serialize access due to read/write dependencies among transactions (SQLSTATE 40001)",
		"prepared stmt": `ERROR: prepared statement "stmtcache_8c1f9e" already exists (SQLSTATE 42P05)`,
		"timeout":       "failed to begin transaction: timeout: context deadline exceeded",
	}
	seen := map[string]string{}
	for name, cause := range causes {
		fp := ServerFingerprint(tx(cause))
		if prev, dup := seen[fp]; dup {
			t.Errorf("%s and %s share fingerprint %s: one issue for two causes", name, prev, fp)
		}
		seen[fp] = name
	}
	if len(seen) != 4 {
		t.Fatalf("four causes gave %d fingerprints", len(seen))
	}
	// The control: sfp1 (no error attribute) lumps them, which is the defect.
	lump := func() string { return ServerFingerprint(rec("reliant-temporal-worker", fn, "Transaction failed")) }
	for name := range causes {
		if lump() == ServerFingerprint(tx(causes[name])) {
			t.Errorf("%s: the refined fingerprint equals the unrefined one", name)
		}
	}

	// The same cause with different ids, values and timings is one issue.
	a := tx(`ERROR: prepared statement "stmtcache_8c1f9e" already exists (SQLSTATE 42P05)`)
	b := tx(`ERROR: prepared statement "stmtcache_00aa11" already exists (SQLSTATE 42P05)`)
	if ServerFingerprint(a) != ServerFingerprint(b) {
		t.Error("the same prepared-statement clash with another statement name split into two issues")
	}
	c := tx("failed to begin transaction: timeout: context deadline exceeded after 30000 ms on conn 17")
	d := tx("failed to begin transaction: timeout: context deadline exceeded after 5 ms on conn 4")
	if ServerFingerprint(c) != ServerFingerprint(d) {
		t.Errorf("one timeout with different numbers split into two issues:\n%q\n%q", ErrorClass(c.Attributes[LogKeyError]), ErrorClass(d.Attributes[LogKeyError]))
	}
	// Two serialization failures of different transactions are one cause, even
	// though the human text differs after the code.
	e := tx("ERROR: could not serialize access due to concurrent update (SQLSTATE 40001)")
	f := tx("ERROR: could not serialize access due to read/write dependencies among transactions (SQLSTATE 40001)")
	if ServerFingerprint(e) != ServerFingerprint(f) {
		t.Error("two SQLSTATE 40001 failures at one site split into two issues")
	}
	// A service still scopes it.
	other := tx(causes["timeout"])
	other.ServiceName = "reliant-api-server"
	if ServerFingerprint(other) == ServerFingerprint(tx(causes["timeout"])) {
		t.Error("the same cause in two services merged into one issue")
	}
}

// T7 composes with the stack frame (D5): a record keeps grouping by the frame the
// SDK stamped, and the cause refines that frame; it never replaces it.
func TestServerFingerprintErrorClassRefinesTheOriginFrame(t *testing.T) {
	const listJobs = "github.com/acme/app/internal/handlers/jobs.(*Service).ListJobs"
	const getJob = "github.com/acme/app/internal/handlers/jobs.(*Service).GetJob"
	const cause = "ERROR: could not serialize access (SQLSTATE 40001)"
	at := func(frame, e string) string {
		return ServerFingerprint(rec("api", frame, "rpc failed", LogKeyError, e, "exception.stacktrace", "frame\n\t"+frame))
	}
	if at(listJobs, cause) == at(getJob, cause) {
		t.Error("the same cause from two origin frames merged: the class replaced the frame")
	}
	if at(listJobs, cause) == at(listJobs, "ERROR: deadlock detected (SQLSTATE 40P01)") {
		t.Error("two causes from one origin frame merged: the class did not refine the frame")
	}
	if at(listJobs, cause) != at(listJobs, "ERROR: could not serialize access (SQLSTATE 40001) on row 9") {
		t.Error("one cause from one frame split on its message")
	}
}

// The real SDK path: an error VALUE carries a stack and a type, so the SDK
// stamps both, sfp2 does not refine it, and its fingerprint is the sfp1 one that
// groups by the origin frame.
func TestServerFingerprintOfAStackBearingErrorIsUnchangedBySfp2(t *testing.T) {
	_, attrs := sfp1Of(t, svcerr.Wrap(faultAtListJobs()))
	r := ServerErrorRecord{ServiceName: "api", ScopeName: "github.com/acme/app", Body: "rpc failed", Attributes: attrs}
	if attrs[LogKeyError] == "" {
		t.Fatalf("expected the SDK line to carry the error text too: %v", attrs)
	}
	if r.RefinedByErrorClass() {
		t.Fatalf("a typed, stack-bearing record must not be refined: %v", attrs)
	}
	delete(attrs, LogKeyError)
	if ServerFingerprint(r) != ServerFingerprint(ServerErrorRecord{ServiceName: "api", ScopeName: "github.com/acme/app", Body: "rpc failed", Attributes: attrs}) {
		t.Error("the error text changed the fingerprint of a typed record")
	}
}

func TestServerCause(t *testing.T) {
	for name, c := range map[string]struct {
		attrs map[string]string
		want  string
	}{
		"sqlstate":        {map[string]string{LogKeyError: "ERROR: x (SQLSTATE 57P01)"}, "SQLSTATE 57P01"},
		"sqlstate in sdk": {map[string]string{LogKeyExceptionMessage: "failed to connect: ERROR: too many connections (SQLSTATE 53300)"}, "SQLSTATE 53300"},
		"refused":         {map[string]string{LogKeyError: "dial tcp 10.0.0.5:5432: connect: connection refused"}, "connection refused 10.0.0.5:5432"},
		"class":           {map[string]string{LogKeyError: "no usable database connection 7"}, "no usable database connection <n>"},
		"none":            {map[string]string{}, ""},
	} {
		if got := ServerCause(ServerErrorRecord{Attributes: c.attrs}); got != c.want {
			t.Errorf("%s: cause %q, want %q", name, got, c.want)
		}
	}
}

func TestNormalizeMessage(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                              "plain",
		"first\nsecond":                      "first",
		"GET https://a.example/x?y=1 failed": "GET <url> failed",
		"id 0b2f6f4e-5f6a-4c1b-9b1e-2a3b4c5d6e7f": "id <uuid>",
		`user 'bob' said "hi"`:                    "user <str> said <str>",
		"commit deadbeefcafe0123 and 42 rows":     "commit <hex> and <n> rows",
		"v2 retry3":                               "v2 retry3",
		strings.Repeat("x", 250):                  strings.Repeat("x", 200),
	} {
		if got := NormalizeMessage(in); got != want {
			t.Errorf("NormalizeMessage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestServerTitleAndCulprit(t *testing.T) {
	r := rec("api", "pkg.F", "boom 12", LogKeyExceptionType, "*pgconn.PgError")
	if got := ServerTitle(r); got != "*pgconn.PgError: boom <n>" {
		t.Errorf("title %q", got)
	}
	if got := ServerCulprit(r); got != "internal/x/x.go in pkg.F" {
		t.Errorf("culprit %q", got)
	}
	bare := ServerErrorRecord{ScopeName: "scope", Body: "x"}
	if ServerTitle(bare) != "x" || ServerCulprit(bare) != "scope" {
		t.Errorf("bare record: %q / %q", ServerTitle(bare), ServerCulprit(bare))
	}
}

// The SQL never reads a line number, and evaluates both normalizations.
func TestServerFingerprintSQLShape(t *testing.T) {
	if strings.Contains(ServerFingerprintSQL, "line") {
		t.Error("sfp1 reads a line number")
	}
	if n := strings.Count(ServerFingerprintSQL, "replaceRegexpAll(splitByChar"); n != 3 {
		t.Errorf("expected norm(Body), norm(exception.message) and norm(error), found %d normalizations", n)
	}
	if MessageNormSQL("Body") != sqlBodyNorm {
		t.Error("MessageNormSQL drifted from the expression sfp1 uses")
	}
}

// A server fault's issue is keyed by where it began, not by the function that
// logged it. Two internal errors with the same message, type and log site but
// different origins must be two issues; one origin is one issue however many
// requests hit it; and a client error is never given a stack.
//
//go:noinline
func faultAtListJobs() error { return svcerr.Internal("query failed") }

//go:noinline
func faultAtGetJob() error { return svcerr.Internal("query failed") }

// logAsInterceptor writes the record the way LoggingInterceptor does: every
// failure of every rpc is logged from this one function.
//
//go:noinline
func logAsInterceptor(l *slog.Logger, err error) { l.Error("rpc failed", "error", err) }

func sfp1Of(t *testing.T, err error) (string, map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(observe.NewLogHandler(slog.NewJSONHandler(&buf, nil), observe.Config{}))
	logAsInterceptor(logger, err)
	var line map[string]any
	if jerr := json.Unmarshal(buf.Bytes(), &line); jerr != nil {
		t.Fatalf("log line: %v: %s", jerr, buf.String())
	}
	attrs := map[string]string{}
	for k, v := range line {
		if s, ok := v.(string); ok {
			attrs[k] = s
		}
		if f, ok := v.(float64); ok {
			attrs[k] = strconv.Itoa(int(f))
		}
	}
	return ServerFingerprint(ServerErrorRecord{ServiceName: "api", ScopeName: "github.com/acme/app", Body: "rpc failed", Attributes: attrs}), attrs
}

func TestServerFingerprintUsesTheTopApplicationFrameOfTheStack(t *testing.T) {
	fpList, listAttrs := sfp1Of(t, svcerr.Wrap(faultAtListJobs()))
	fpGet, getAttrs := sfp1Of(t, svcerr.Wrap(faultAtGetJob()))

	if fpList == fpGet {
		t.Fatalf("two internal errors from different sites share fingerprint %s", fpList)
	}
	if !strings.HasSuffix(listAttrs[LogKeyCodeFunction], ".faultAtListJobs") || !strings.HasSuffix(getAttrs[LogKeyCodeFunction], ".faultAtGetJob") {
		t.Errorf("code.function.name is not the stack's top application frame: %q, %q",
			listAttrs[LogKeyCodeFunction], getAttrs[LogKeyCodeFunction])
	}
	if !strings.Contains(listAttrs["exception.stacktrace"], "faultAtListJobs") {
		t.Errorf("record has no stack: %v", listAttrs)
	}

	// Same origin, a new request: still one issue.
	again, _ := sfp1Of(t, svcerr.Wrap(faultAtListJobs()))
	if again != fpList {
		t.Errorf("one origin split into two issues: %s vs %s", again, fpList)
	}

	// The control: with no stack on either record they share the log site and
	// collapse into one issue, which is the defect this fixes.
	for _, attrs := range []map[string]string{listAttrs, getAttrs} {
		delete(attrs, "exception.stacktrace")
		attrs[LogKeyCodeFunction] = "github.com/reliant-labs/forge/pkg/obsquery.logAsInterceptor"
	}
	a := ServerFingerprint(ServerErrorRecord{ServiceName: "api", Body: "rpc failed", Attributes: listAttrs})
	b := ServerFingerprint(ServerErrorRecord{ServiceName: "api", Body: "rpc failed", Attributes: getAttrs})
	if a != b {
		t.Errorf("control: with the log site as the frame these should merge, got %s and %s", a, b)
	}
}

func TestServerFingerprintOfAClientErrorHasNoStack(t *testing.T) {
	_, attrs := sfp1Of(t, svcerr.Wrap(svcerr.InvalidArgument("bad field")))
	if _, ok := attrs["exception.stacktrace"]; ok {
		t.Errorf("a client error carries a stack: %v", attrs)
	}
	if !strings.HasSuffix(attrs[LogKeyCodeFunction], ".logAsInterceptor") {
		t.Errorf("a client error's site is the log call site, got %q", attrs[LogKeyCodeFunction])
	}
}
