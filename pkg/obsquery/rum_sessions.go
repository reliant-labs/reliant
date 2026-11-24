package obsquery

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Browser sessions: the list, one session's summary and its timeline. They read
// the control plane's per-org browser tables (rum_sessions, otel_traces,
// error_events, error_stacks), like the sessions panel in panel_browser.go, and
// share its scope rules: the database, the environments the caller may see and
// the services. Replay bytes are rum_replay.go's.
//
// Every function here is pure. Compile builds a Stmt, Parse reads the rows the
// caller's connection returned, and ListSessions, GetSession and ReadTimeline
// run both through the caller's Runner. Nothing opens a connection.
//
// A read of ONE named session (GetSession, ReadTimeline, ReadReplay) is bounded
// by the database, the environments and the services of the scope, and by its
// time range. Scope.Filters narrow the session LIST only: a session id is a key,
// and a filter that could hide the session it names would make the three reads
// disagree about whether it exists.

// Session limits.
const (
	DefaultSessionLimit = 25
	MaxSessionLimit     = 100

	DefaultTimelineLimit = 200
	MaxTimelineLimit     = 2000

	// MaxRangeTimeline is the longest window a timeline reads: its spans come
	// from otel_traces, whose retention is the trace one.
	MaxRangeTimeline = MaxRangeTraces

	maxRumValueLen = 256
	maxSessionID   = 128
)

// ErrSessionNotFound is GetSession's answer for a session the scope cannot see
// (no such session, or one in another environment or service).
var ErrSessionNotFound = errors.New("session not found")

var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// CheckSessionID refuses anything that is not the shape of an rum.sessionId (a
// 32-digit hex id from the SDK; the pattern is wider so a custom id survives).
func CheckSessionID(id string) error {
	if !sessionIDRe.MatchString(id) {
		return invalid("session_id must be 1-%d characters of letters, digits, _ and -", maxSessionID)
	}
	return nil
}

func checkRumValue(what, v string) error {
	if len(v) > maxRumValueLen {
		return invalid("%s is longer than %d characters", what, maxRumValueLen)
	}
	return nil
}

func encodeCursor(v any) string {
	raw, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(tok string, into any) error {
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil || json.Unmarshal(raw, into) != nil {
		return invalid("page_token is malformed")
	}
	return nil
}

func envNames(envs []Env) map[string]string {
	m := make(map[string]string, len(envs))
	for _, e := range envs {
		m[e.ID] = e.Name
	}
	return m
}

// ── Session summary and list ────────────────────────────────────────────

// SessionSummary is one browser session, merged from its daily partial rows.
type SessionSummary struct {
	SessionID    string
	Service      string
	Version      string
	EnvID        string
	EnvName      string
	UserID       string // the userId global attribute, empty when never identified
	EntryPath    string // path of the first page, no query or fragment
	Started      time.Time
	Ended        time.Time
	PageViews    uint64
	Interactions uint64
	Errors       uint64
	HasReplay    bool
	// Where the session's browser was, derived at ingest by the platform from the
	// address it connected from (resource attributes geo.country.iso_code,
	// geo.region.name and geo.locality.name), kept together as one tuple so the
	// three always come from the same report. Empty when the platform derived
	// none, and always without Scope.SessionGeo.
	Country, Region, City string
	// ClientAddress is the browser's address (client.address). Empty unless the
	// platform is configured to keep it; by default it derives the location and
	// drops the address.
	ClientAddress string
	// Client is which client the session ran in. Zero without
	// Scope.SessionClient.
	Client ClientInfo
}

// ClientInfo is which client a browser session ran in. The browser, OS and
// device type are the platform's, derived at ingest from the request's own
// User-Agent and client hints (user_agent.*, os.*, reliant.device.type); the
// desktop versions and the viewport are the page's (app.desktop.*,
// app.viewport.*, app.screen.*). A zero field is unknown.
type ClientInfo struct {
	// BrowserName and BrowserVersion: Chrome 156.0.0, Mobile Safari 26.6.1,
	// Electron 41.10.7 (a desktop app's renderer).
	BrowserName, BrowserVersion string
	OSName, OSVersion           string
	// DeviceType is desktop, mobile, tablet or bot.
	DeviceType string
	// ChromiumVersion is a Chromium-based client's engine (Electron's included).
	ChromiumVersion string
	// DesktopVersion is the desktop app's own version; ElectronVersion the
	// Electron it runs on. Both empty outside a desktop app.
	DesktopVersion, ElectronVersion string
	// The viewport and screen in CSS pixels: the widest viewport the session
	// reported, and its screen.
	ViewportWidth, ViewportHeight, ScreenWidth, ScreenHeight uint32
}

// clientTupleCols reads the rum_sessions.Client and Viewport tuples, in the
// order scanSession reads them. Without the columns they are constants.
func clientTupleCols(s Scope) string {
	if !s.SessionClient {
		return "'' AS browser_name, '' AS browser_version, '' AS os_name, '' AS os_version, '' AS device_type,\n" +
			"       '' AS chromium_version, '' AS desktop_version, '' AS desktop_electron_version,\n" +
			"       toUInt32(0) AS viewport_width, toUInt32(0) AS viewport_height, toUInt32(0) AS screen_width, toUInt32(0) AS screen_height"
	}
	var b strings.Builder
	for i, name := range []string{"browser_name", "browser_version", "os_name", "os_version", "device_type", "chromium_version", "desktop_version", "desktop_electron_version"} {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("tupleElement(max(Client), " + strconv.Itoa(i+1) + ") AS " + name)
	}
	b.WriteString(",\n       ")
	for i, name := range []string{"viewport_width", "viewport_height", "screen_width", "screen_height"} {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("tupleElement(max(Viewport), " + strconv.Itoa(i+1) + ") AS " + name)
	}
	return b.String()
}

// Duration is how long the session was observed.
func (s SessionSummary) Duration() time.Duration { return s.Ended.Sub(s.Started) }

// sessionCols is the select list of the session statements, in the order
// scanSession reads: session_id, service, version, env_id, user_id, entry_path,
// started, ended, page_views, interactions, errors, has_replay, country, region,
// city, client_address, then the client (clientTupleCols). Without
// Scope.SessionGeo the location four are constants, and without
// Scope.SessionClient the client ones: the tables predate them, and the columns
// are named only when they exist.
func sessionCols(s Scope) string {
	geo := "'' AS country, '' AS region, '' AS city, '' AS client_address"
	if s.SessionGeo || s.SessionClient {
		geo = "tupleElement(max(Geo), 1) AS country, tupleElement(max(Geo), 2) AS region, tupleElement(max(Geo), 3) AS city,\n" +
			"       max(ClientAddress) AS client_address"
	}
	return "SessionId, ServiceName, max(ServiceVersion) AS version, max(EnvId) AS env_id, max(UserId) AS user_id,\n" +
		"       argMinMerge(EntryPath) AS entry_path, min(Start) AS started, max(End) AS ended,\n" +
		"       sum(PageViews) AS page_views, sum(Interactions) AS interactions, sum(Errors) AS errors, max(HasReplay) AS has_replay,\n" +
		"       " + geo + ",\n" +
		"       " + clientTupleCols(s)
}

func scanSession(rows Rows, names map[string]string) (SessionSummary, error) {
	var (
		s                     SessionSummary
		pv, ia, er            uint64
		replay                uint8
		started, ended        time.Time
		id, svc, ver, env, us string
		entry                 string
		country, region, city string
		addr                  string
		c                     ClientInfo
	)
	if err := rows.Scan(&id, &svc, &ver, &env, &us, &entry, &started, &ended, &pv, &ia, &er, &replay, &country, &region, &city, &addr,
		&c.BrowserName, &c.BrowserVersion, &c.OSName, &c.OSVersion, &c.DeviceType, &c.ChromiumVersion, &c.DesktopVersion, &c.ElectronVersion,
		&c.ViewportWidth, &c.ViewportHeight, &c.ScreenWidth, &c.ScreenHeight); err != nil {
		return s, err
	}
	// The Electron a desktop renderer runs on is in its User-Agent, which the
	// platform parsed; the page's own report is the fallback.
	if c.BrowserName == "Electron" && c.BrowserVersion != "" {
		c.ElectronVersion = c.BrowserVersion
	}
	return SessionSummary{
		SessionID: id, Service: svc, Version: ver, EnvID: env, EnvName: names[env], UserID: us, EntryPath: entry,
		Started: started, Ended: ended, PageViews: pv, Interactions: ia, Errors: er, HasReplay: replay != 0,
		Country: country, Region: region, City: city, ClientAddress: addr, Client: c,
	}, nil
}

// SessionQuery is a ListSessions request: the sessions that STARTED in the
// scope's range, newest first.
type SessionQuery struct {
	Scope Scope
	// HasError keeps sessions with at least one browser error; HasReplay those
	// with recorded replay.
	HasError, HasReplay bool
	// UserID keeps sessions of that user id; empty keeps all.
	UserID string
	// PathContains keeps sessions whose entry path contains it, case-insensitive.
	PathContains string
	// Browser, OS and DeviceType keep sessions of that client
	// (ClientInfo.BrowserName, OSName, DeviceType; exact). Without
	// Scope.SessionClient no session has a client, so a set one matches none.
	// Version keeps sessions of that frontend build (SessionSummary.Version).
	// Empty keeps all.
	Browser, OS, DeviceType, Version string
	Limit                            int // 0 = DefaultSessionLimit; the statement fetches one more
	Cursor                           *SessionCursor
}

// SessionCursor is the keyset position of the last session of a page.
type SessionCursor struct {
	TsMs      int64  `json:"t"`
	Service   string `json:"s"`
	SessionID string `json:"i"`
	Finger    string `json:"f"`
}

// Fingerprint identifies the filters a cursor belongs to. The time range is
// deliberately not part of it.
func (q SessionQuery) Fingerprint() string {
	parts := append([]string{"sessions"}, q.Scope.Fingerprint()...)
	parts = append(parts, strconv.FormatBool(q.HasError), strconv.FormatBool(q.HasReplay), q.UserID, q.PathContains)
	// Appended only when set, so a token of a query without them is unchanged.
	for _, f := range [][2]string{{"browser", q.Browser}, {"os", q.OS}, {"device", q.DeviceType}, {"version", q.Version}} {
		if f[1] != "" {
			parts = append(parts, f[0]+":"+f[1])
		}
	}
	return fingerprint(parts...)
}

// NextCursor builds the page token that continues after the session that
// started at started.
func (q SessionQuery) NextCursor(started time.Time, service, sessionID string) string {
	return encodeCursor(SessionCursor{TsMs: started.UnixMilli(), Service: service, SessionID: sessionID, Finger: q.Fingerprint()})
}

// DecodeSessionCursor parses a page token; "" is no cursor.
func DecodeSessionCursor(tok string) (*SessionCursor, error) {
	if tok == "" {
		return nil, nil
	}
	var c SessionCursor
	if err := decodeCursor(tok, &c); err != nil || c.Finger == "" {
		return nil, invalid("page_token is malformed")
	}
	return &c, nil
}

func (q SessionQuery) limit() int {
	if q.Limit == 0 {
		return DefaultSessionLimit
	}
	return q.Limit
}

// sessionDays bounds rum_sessions by day: the range's days and the one before,
// so a session that began the day before the range is not clipped.
func (b *builder) sessionDays(s Scope) []string {
	return []string{
		"Day >= subtractDays(toDate(" + b.nanoTime(s.Start) + "), 1)",
		"Day <= toDate(" + b.nanoTime(s.End) + ")",
	}
}

// Compile builds the list statement. Columns: sessionCols. At most limit+1 rows.
func (q SessionQuery) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeSessions); err != nil {
		return Stmt{}, err
	}
	if q.Limit < 0 || q.Limit > MaxSessionLimit {
		return Stmt{}, invalid("page size must be 1-%d", MaxSessionLimit)
	}
	if err := checkRumValue("user_id", q.UserID); err != nil {
		return Stmt{}, err
	}
	if err := checkRumValue("path_contains", q.PathContains); err != nil {
		return Stmt{}, err
	}
	for _, f := range [][2]string{{"browser", q.Browser}, {"os", q.OS}, {"device_type", q.DeviceType}, {"version", q.Version}} {
		if err := checkRumValue(f[0], f[1]); err != nil {
			return Stmt{}, err
		}
	}
	if q.Cursor != nil && q.Cursor.Finger != q.Fingerprint() {
		return Stmt{}, invalid("page_token does not belong to this query")
	}
	b := newBuilder(q.Scope)
	where, having, err := b.browserConds(tblSessions, PanelQuery{Scope: q.Scope, Panel: Panel{Kind: "sessions", Signal: "traces"}})
	if err != nil {
		return Stmt{}, err
	}
	having = append([]string{b.rangeCond("started", q.Scope)}, having...)
	if q.HasError {
		having = append(having, "sum(Errors) > 0")
	}
	if q.HasReplay {
		having = append(having, "max(HasReplay) = 1")
	}
	if q.UserID != "" {
		having = append(having, "max(UserId) = "+b.str(q.UserID))
	}
	if q.PathContains != "" {
		having = append(having, "positionCaseInsensitive(argMinMerge(EntryPath), "+b.str(q.PathContains)+") > 0")
	}
	if q.Version != "" {
		having = append(having, "max(ServiceVersion) = "+b.str(q.Version))
	}
	for i, v := range []string{q.Browser, q.OS, q.DeviceType} {
		if v == "" {
			continue
		}
		if !q.Scope.SessionClient {
			having = append(having, "0") // the tables carry no client: none matches
			continue
		}
		// Client is (browser, version, os, version, device type, ...).
		having = append(having, "tupleElement(max(Client), "+strconv.Itoa([]int{1, 3, 5}[i])+") = "+b.str(v))
	}
	if c := q.Cursor; c != nil {
		having = append(having, "(started, ServiceName, SessionId) < (fromUnixTimestamp64Milli("+b.i64(c.TsMs)+"), "+b.str(c.Service)+", "+b.str(c.SessionID)+")")
	}
	sql := "SELECT " + sessionCols(q.Scope) + "\n" +
		"FROM " + b.table(q.Scope, tblSessions) + "\n" + where2(append(b.sessionDays(q.Scope), where...)) + "\n" +
		"GROUP BY ServiceName, SessionId\n" +
		"HAVING " + strings.Join(having, "\n  AND ") + "\n" +
		"ORDER BY started DESC, ServiceName DESC, SessionId DESC\nLIMIT " + strconv.Itoa(q.limit()+1)
	return b.stmt(sql), nil
}

// SessionPage is one page of ListSessions.
type SessionPage struct {
	Sessions []SessionSummary
	// NextCursor continues after the last returned session; "" on the last page.
	NextCursor string
}

// Parse reads the rows of Compile's statement. The caller closes rows.
func (q SessionQuery) Parse(rows Rows) (SessionPage, error) {
	names := envNames(q.Scope.Envs)
	limit := q.limit()
	page := SessionPage{Sessions: []SessionSummary{}}
	for rows.Next() {
		s, err := scanSession(rows, names)
		if err != nil {
			return SessionPage{}, err
		}
		if len(page.Sessions) == limit {
			last := page.Sessions[limit-1]
			page.NextCursor = q.NextCursor(last.Started, last.Service, last.SessionID)
			break
		}
		page.Sessions = append(page.Sessions, s)
	}
	if err := rows.Err(); err != nil {
		return SessionPage{}, err
	}
	return page, nil
}

// ListSessions compiles q, runs it on run and parses the page.
func ListSessions(ctx context.Context, run Runner, q SessionQuery) (SessionPage, error) {
	st, err := q.Compile()
	if err != nil {
		return SessionPage{}, err
	}
	rows, err := run(ctx, st)
	if err != nil {
		return SessionPage{}, err
	}
	defer rows.Close()
	return q.Parse(rows)
}

// SessionLookup is a GetSession request: one session's summary.
type SessionLookup struct {
	// Scope bounds the read; its range should cover the session (the days it
	// touched are read, and the day before the range start).
	Scope     Scope
	SessionID string
}

// Compile builds the statement. Columns: sessionCols. At most one row.
func (q SessionLookup) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeSessions); err != nil {
		return Stmt{}, err
	}
	if err := CheckSessionID(q.SessionID); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	where := append(b.sessionDays(q.Scope), "SessionId = "+b.str(q.SessionID))
	if sv := b.serviceIn(q.Scope.Services); sv != "" {
		where = append(where, sv)
	}
	if e := b.browserInstanceCond(q.Scope); e != "" {
		where = append(where, e)
	}
	sql := "SELECT " + sessionCols(q.Scope) + "\nFROM " + b.table(q.Scope, tblSessions) + "\n" + where2(where) + "\nGROUP BY ServiceName, SessionId\n"
	if e := b.browserEnvCond("max(EnvId)", q.Scope); e != "" {
		sql += "HAVING " + e + "\n"
	}
	return b.stmt(sql + "ORDER BY started DESC\nLIMIT 1"), nil
}

// ParseSession reads the rows of SessionLookup.Compile. envs resolve the
// environment's name. A session the statement did not find is ErrSessionNotFound.
func ParseSession(rows Rows, envs []Env) (SessionSummary, error) {
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return SessionSummary{}, err
		}
		return SessionSummary{}, ErrSessionNotFound
	}
	s, err := scanSession(rows, envNames(envs))
	if err != nil {
		return SessionSummary{}, err
	}
	return s, rows.Err()
}

// GetSession compiles q, runs it on run and parses the summary.
func GetSession(ctx context.Context, run Runner, q SessionLookup) (SessionSummary, error) {
	st, err := q.Compile()
	if err != nil {
		return SessionSummary{}, err
	}
	rows, err := run(ctx, st)
	if err != nil {
		return SessionSummary{}, err
	}
	defer rows.Close()
	return ParseSession(rows, q.Scope.Envs)
}

// ── Timeline ────────────────────────────────────────────────────────────

// TimelineKind is what a timeline entry is.
type TimelineKind string

const (
	TimelineNavigation    TimelineKind = "navigation"     // a document load or route change
	TimelineClick         TimelineKind = "click"          // one physical click or tap
	TimelineRequestFailed TimelineKind = "request_failed" // a fetch or XHR with status >= 400 or an error status
	TimelineError         TimelineKind = "error"          // a browser error (an error_events row)
	TimelineEvent         TimelineKind = "event"          // a custom event (track / HyperDX.addAction)
)

// TimelineEntry is one line of a session's timeline.
//
// Nothing in it is a raw URL or element text. Paths have their query and
// fragment cut (they carry tokens); a click carries the element's tag and its
// xpath (tags and ids), never its text, which the recorder masks as well; an
// error carries the message with its volatile parts masked (MessageNorm).
type TimelineEntry struct {
	Time time.Time
	Kind TimelineKind
	// Name: the span name of a navigation (documentLoad, routeChange), "click",
	// the HTTP method of a failed request, the span name of an error
	// (onerror, unhandledrejection, ...), the event name of a custom event.
	Name string
	// Path is the page (navigation, click, error, event) or the request's path.
	Path     string
	Method   string
	Status   int    // HTTP status of a failed request; 0 for a network failure
	Selector string // click: the element's xpath
	Element  string // click: the element's tag name
	// ErrorType is the exception type of an error, or the SDK's error.type of a
	// failed request. Message is the masked error message, or a failed
	// request's status text.
	ErrorType string
	Message   string
	// Fingerprint is the issue fingerprint of an error (error_stacks): empty
	// until the enrichment worker has seen its stack. The control plane maps it
	// to an issue id.
	Fingerprint string
	TraceID     string
	SpanID      string
	DurationMs  float64
	// Props: a custom event's properties (at most 32, values cut at 256
	// characters); a navigation's "from" (the previous path).
	Props   map[string]string
	Service string
}

// timelineCols is the select list of the timeline statement, in the order
// Parse scans it.
const timelineCols = "ts, kind, name, path, method, status, selector, element, error_type, message, fingerprint, trace_id, span_id, duration_ms, props, service"

// SessionTimeline is a ReadTimeline request: one session's navigations,
// clicks, failed requests, errors and custom events in time order.
type SessionTimeline struct {
	// Scope bounds the read; its range (at most MaxRangeTimeline) should cover
	// the session, e.g. its start minus a minute to its end plus a minute.
	Scope     Scope
	SessionID string
	Limit     int // 0 = DefaultTimelineLimit; the statement fetches one more
	Cursor    *TimelineCursor
}

// TimelineCursor is the keyset position of the last entry of a page.
type TimelineCursor struct {
	TsNano int64  `json:"t"`
	Kind   string `json:"k"`
	SpanID string `json:"i"`
	Finger string `json:"f"`
}

// Fingerprint identifies the session a cursor belongs to. The time range is
// deliberately not part of it.
func (q SessionTimeline) Fingerprint() string {
	return fingerprint(append(append([]string{"timeline"}, q.Scope.Fingerprint()...), q.SessionID)...)
}

// NextCursor builds the page token that continues after the entry at ts.
func (q SessionTimeline) NextCursor(ts time.Time, kind, spanID string) string {
	return encodeCursor(TimelineCursor{TsNano: ts.UnixNano(), Kind: kind, SpanID: spanID, Finger: q.Fingerprint()})
}

// DecodeTimelineCursor parses a page token; "" is no cursor.
func DecodeTimelineCursor(tok string) (*TimelineCursor, error) {
	if tok == "" {
		return nil, nil
	}
	var c TimelineCursor
	if err := decodeCursor(tok, &c); err != nil || c.Finger == "" {
		return nil, invalid("page_token is malformed")
	}
	return &c, nil
}

func (q SessionTimeline) limit() int {
	if q.Limit == 0 {
		return DefaultTimelineLimit
	}
	return q.Limit
}

const (
	pathCap     = "512"
	stringMap   = "'Map(String, String)'"
	spanSession = "ResourceAttributes['rum.sessionId']"
)

// Compile builds the timeline statement: five branches over the session's rows,
// merged and ordered by (ts, kind, span_id). Columns: timelineCols. At most
// limit+1 rows.
//
//	navigation       otel_traces: documentLoad, routeChange
//	click            otel_traces: mousedown (the SDK emits click two or three times per click)
//	request_failed   otel_traces: fetch / xml-http-request with status >= 400 or an error status
//	event            otel_traces: scope custom-action
//	error            error_events, left-joined to error_stacks for the fingerprint
//
// A console.error(string) has no exception and is no error row, so it is no
// timeline entry either; the error spans of otel_traces are not read, since
// error_events holds them.
func (q SessionTimeline) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeTimeline); err != nil {
		return Stmt{}, err
	}
	if err := CheckSessionID(q.SessionID); err != nil {
		return Stmt{}, err
	}
	if q.Limit < 0 || q.Limit > MaxTimelineLimit {
		return Stmt{}, invalid("page size must be 1-%d", MaxTimelineLimit)
	}
	if q.Cursor != nil && q.Cursor.Finger != q.Fingerprint() {
		return Stmt{}, invalid("page_token does not belong to this session")
	}
	b := newBuilder(q.Scope)
	sid := b.str(q.SessionID)

	spanWhere := func(extra ...string) string {
		c := []string{b.rangeCond("Timestamp", q.Scope), spanSession + " = " + sid}
		c = append(c, b.scopeBase(q.Scope)...)
		return where(append(c, extra...))
	}
	errConds := []string{b.rangeCond("Timestamp", q.Scope), "SessionId = " + sid}
	if e := b.browserEnvCond("EnvId", q.Scope); e != "" {
		errConds = append(errConds, e)
	}
	if e := b.browserInstanceCond(q.Scope); e != "" {
		errConds = append(errConds, e)
	}
	if sv := b.serviceIn(q.Scope.Services); sv != "" {
		errConds = append(errConds, sv)
	}

	emptyMap := "CAST(map(), " + stringMap + ")"
	span := func(kind, name, path, method, status, selector, element, errType, message, props string) string {
		return "SELECT Timestamp AS ts, " + kind + " AS kind, " + name + " AS name, " + path + " AS path, " + method + " AS method, " +
			status + " AS status, " + selector + " AS selector, " + element + " AS element, " + errType + " AS error_type, " +
			message + " AS message, '' AS fingerprint, TraceId AS trace_id, SpanId AS span_id, " +
			"toFloat64(Duration) / 1000000 AS duration_ms, " + props + " AS props, toString(ServiceName) AS service\n" +
			"  FROM " + b.table(q.Scope, "otel_traces") + "\n  "
	}
	pathOf := func(attr string) string { return "substring(path(" + attr + "), 1, " + pathCap + ")" }
	href := "SpanAttributes['location.href']"
	zero := "toUInt16(0)"

	nav := span("'navigation'", "toString(SpanName)", pathOf(href), "''", zero, "''", "''", "''", "''",
		"if(SpanAttributes['prev.href'] != '', CAST(map('from', "+pathOf("SpanAttributes['prev.href']")+"), "+stringMap+"), "+emptyMap+")") +
		spanWhere("SpanName IN ('documentLoad', 'routeChange')")

	click := span("'click'", "'click'", pathOf(href), "''", zero,
		"substring(SpanAttributes['target_xpath'], 1, 256)", "substring(SpanAttributes['target_element'], 1, 32)", "''", "''", emptyMap) +
		spanWhere("SpanName = 'mousedown'", "SpanAttributes['component'] = 'user-interaction'")

	reqURL := "if(SpanAttributes['http.url'] != '', SpanAttributes['http.url'], SpanAttributes['url.full'])"
	reqStatus := "toUInt16OrZero(if(SpanAttributes['http.response.status_code'] != '', SpanAttributes['http.response.status_code'], SpanAttributes['http.status_code']))"
	failed := "WITH " + reqStatus + " AS req_status\n" +
		span("'request_failed'", "if(SpanAttributes['http.request.method'] != '', SpanAttributes['http.request.method'], SpanAttributes['http.method'])",
			pathOf(reqURL), "if(SpanAttributes['http.request.method'] != '', SpanAttributes['http.request.method'], SpanAttributes['http.method'])",
			"req_status", "''", "''", "substring(SpanAttributes['error.type'], 1, 64)", "substring(SpanAttributes['http.status_text'], 1, 128)", emptyMap) +
		spanWhere("SpanAttributes['component'] IN ('fetch', 'xml-http-request')", "("+spanErrorExpr+" OR req_status >= 400)")

	event := "WITH mapFilter((k, v) -> k NOT IN ('location.href', 'userId', 'app.client', 'visitor.id'), SpanAttributes) AS ev_props,\n" +
		"     arraySlice(arraySort(mapKeys(ev_props)), 1, 32) AS ev_keys\n" +
		span("'event'", "if(startsWith(SpanName, 'product.'), substring(SpanName, 9), toString(SpanName))", pathOf(href), "''", zero, "''", "''", "''", "''",
			"CAST(mapFromArrays(ev_keys, arrayMap(k -> substringUTF8(ev_props[k], 1, 256), ev_keys)), "+stringMap+")") +
		spanWhere("ScopeName = 'custom-action'")

	errEvents := "SELECT e.Timestamp AS ts, 'error' AS kind, toString(e.SpanName) AS name, substring(e.UrlPath, 1, " + pathCap + ") AS path, '' AS method, toUInt16(0) AS status,\n" +
		"         '' AS selector, '' AS element, toString(e.ExceptionType) AS error_type, substring(e.MessageNorm, 1, 200) AS message, s.fp AS fingerprint,\n" +
		"         e.TraceId AS trace_id, e.SpanId AS span_id, toFloat64(0) AS duration_ms, " + emptyMap + " AS props, toString(e.ServiceName) AS service\n" +
		"  FROM (SELECT * FROM " + b.table(q.Scope, "error_events") + " " + where(errConds) + ") AS e\n" +
		"  LEFT JOIN (\n" +
		"    SELECT ServiceName, ServiceVersion, RawStackHash, argMax(Fingerprint, UpdatedAt) AS fp\n" +
		"    FROM " + b.table(q.Scope, "error_stacks") + "\n" +
		"    WHERE (ServiceName, ServiceVersion, RawStackHash) IN (SELECT ServiceName, ServiceVersion, RawStackHash FROM " + b.table(q.Scope, "error_events") + " " + where(errConds) + ")\n" +
		"    GROUP BY ServiceName, ServiceVersion, RawStackHash\n" +
		"  ) AS s ON e.ServiceName = s.ServiceName AND e.ServiceVersion = s.ServiceVersion AND e.RawStackHash = s.RawStackHash"

	var outer []string
	if c := q.Cursor; c != nil {
		outer = append(outer, "(ts, kind, span_id) > (fromUnixTimestamp64Nano("+b.i64(c.TsNano)+"), "+b.str(c.Kind)+", "+b.str(c.SpanID)+")")
	}
	sql := "SELECT " + timelineCols + " FROM (\n" +
		strings.Join([]string{nav, click, failed, event, errEvents}, "\nUNION ALL\n") + "\n)\n" +
		where(outer) + "\nORDER BY ts, kind, span_id\nLIMIT " + strconv.Itoa(q.limit()+1)
	return b.stmt(sql), nil
}

// TimelinePage is one page of ReadTimeline.
type TimelinePage struct {
	Entries []TimelineEntry
	// NextCursor continues after the last returned entry; "" on the last page.
	NextCursor string
}

// Parse reads the rows of Compile's statement. The caller closes rows.
func (q SessionTimeline) Parse(rows Rows) (TimelinePage, error) {
	limit := q.limit()
	page := TimelinePage{Entries: []TimelineEntry{}}
	for rows.Next() {
		var (
			e                                                 TimelineEntry
			kind                                              string
			status                                            uint16
			dur                                               float64
			props                                             map[string]string
			ts                                                time.Time
			name, path, method, selector, element, etype, msg string
			fp, trace, span, service                          string
		)
		if err := rows.Scan(&ts, &kind, &name, &path, &method, &status, &selector, &element, &etype, &msg, &fp, &trace, &span, &dur, &props, &service); err != nil {
			return TimelinePage{}, err
		}
		if len(page.Entries) == limit {
			last := page.Entries[limit-1]
			page.NextCursor = q.NextCursor(last.Time, string(last.Kind), last.SpanID)
			break
		}
		if len(props) == 0 {
			props = nil
		}
		e = TimelineEntry{
			Time: ts, Kind: TimelineKind(kind), Name: name, Path: path, Method: method, Status: int(status), Selector: selector,
			Element: element, ErrorType: etype, Message: msg, Fingerprint: fp, TraceID: trace, SpanID: span, DurationMs: dur,
			Props: props, Service: service,
		}
		page.Entries = append(page.Entries, e)
	}
	if err := rows.Err(); err != nil {
		return TimelinePage{}, err
	}
	return page, nil
}

// ReadTimeline compiles q, runs it on run and parses the page.
func ReadTimeline(ctx context.Context, run Runner, q SessionTimeline) (TimelinePage, error) {
	st, err := q.Compile()
	if err != nil {
		return TimelinePage{}, err
	}
	rows, err := run(ctx, st)
	if err != nil {
		return TimelinePage{}, err
	}
	defer rows.Close()
	return q.Parse(rows)
}
