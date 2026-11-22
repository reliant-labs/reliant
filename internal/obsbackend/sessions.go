package obsbackend

// Browser sessions: the list, one session's summary and its timeline, read the
// same way on both backends. The local backend runs pkg/obsquery's session
// compilers against the ClickStack; the cloud backend reads the control plane's
// RumService, which runs the same compilers against the org's database. Replay
// bytes are not part of this reader: an agent reads the timeline, and the
// player in the console reads the bytes.

import "time"

// SessionSummary is one browser session.
type SessionSummary struct {
	ID      string
	Service string
	// Version is the service.version of the frontend build the session ran.
	Version string
	// UserID is the userId the app identified the session with; "" when it
	// never did.
	UserID string
	// EntryPath is the path of the first page, with no query or fragment.
	EntryPath    string
	Started      time.Time
	Ended        time.Time
	PageViews    int64
	Interactions int64
	Errors       int64
	HasReplay    bool
}

// SessionEntry is one line of a session's timeline. It carries no raw URL,
// query string or element text: paths have their query and fragment cut and a
// click is its tag and xpath.
type SessionEntry struct {
	Time time.Time
	// Kind is an obsquery.TimelineKind: navigation, click, request_failed,
	// error or event.
	Kind string
	// Name is a navigation's span name, "click", a failed request's method, an
	// error's span name or a custom event's name.
	Name string
	// Path is the page, or a failed request's path.
	Path   string
	Method string
	// Status is a failed request's HTTP status; 0 for a network failure.
	Status   int
	Selector string
	Element  string
	// ErrorType is an error's exception type, or a failed request's SDK error
	// type. Message is the error's masked message.
	ErrorType string
	Message   string
	// Fingerprint is the issue fingerprint of an error; IssueID the issue it
	// belongs to, which only the control plane assigns.
	Fingerprint string
	IssueID     string
	TraceID     string
	SpanID      string
	DurationMs  float64
	Props       map[string]string
	Service     string
}

// ListSessionsRequest lists the sessions that started in the narrowed range,
// newest first.
type ListSessionsRequest struct {
	Narrow
	HasError  bool
	HasReplay bool
	// UserID keeps one user's sessions; PathContains those whose entry path
	// contains it, case-insensitively.
	UserID       string
	PathContains string
	Limit        int
	PageToken    string
}

// ListSessionsResult is a page of sessions.
type ListSessionsResult struct {
	Sessions      []SessionSummary
	NextPageToken string
	Statements    []Statement
}

// GetSessionRequest reads one session and a page of its timeline. A zero
// Narrow.Range leaves the search window to the backend: the control plane finds
// the session within 30 days, the local backend looks back 30 days.
type GetSessionRequest struct {
	Narrow
	ID string
	// Limit is the timeline page size; PageToken resumes it.
	Limit     int
	PageToken string
}

// GetSessionResult is a session's summary and one page of its timeline, oldest
// entry first.
type GetSessionResult struct {
	Session       SessionSummary
	Timeline      []SessionEntry
	NextPageToken string
	Statements    []Statement
}
