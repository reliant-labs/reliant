package core

import "time"

// PRRecord is one pull request as Reliant last observed it from a provider's
// webhook deliveries (dev-docs/pr-records.md). It holds facts only: what a
// label means, or whose review counts, is the reader's policy.
type PRRecord struct {
	ID           string
	Provider     string
	AccountKey   string
	RepositoryID string
	Repository   string
	Number       int
	State        string
	Draft        bool
	HeadSHA      string
	HeadRef      string
	BaseRef      string
	BaseSHA      string
	AuthorID     string
	AuthorLogin  string
	Title        string
	Labels       []string
	MergedAt     *time.Time
	MergeCommit  string
	SnapshotAt   time.Time
	ObservedAt   time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// PR record states.
const (
	PRRecordOpen   = "open"
	PRRecordClosed = "closed"
	PRRecordMerged = "merged"
)

// PRRecordSnapshot is a pull request as one event carried it. Full is false
// for the slimmer pull request a review event carries, which omits whether it
// merged: such a snapshot only creates a record that does not exist yet.
type PRRecordSnapshot struct {
	ID           string // the record id to use when the record is new
	Provider     string
	AccountKey   string
	RepositoryID string
	Repository   string
	Number       int
	State        string
	Draft        bool
	HeadSHA      string
	HeadRef      string
	BaseRef      string
	BaseSHA      string
	AuthorID     string
	AuthorLogin  string
	Title        string
	Labels       []string
	MergedAt     *time.Time
	MergeCommit  string
	SnapshotAt   time.Time
	ObservedAt   time.Time
	Full         bool
	// LabelAdded / LabelRemoved is the label a labeled / unlabeled event
	// changed. When the snapshot is exactly as new as the record (two label
	// events in one second carry different full sets), only this delta is
	// applied.
	LabelAdded   string
	LabelRemoved string
}

// PRRecordHead is one head a pull request has had.
type PRRecordHead struct {
	ID              string
	RecordID        string
	HeadSHA         string
	PreviousHeadSHA string
	FirstSeenAt     time.Time
	SupersededAt    *time.Time
}

// PR record review marker kinds.
const (
	PRReviewPassed   = "passed"
	PRReviewChanges  = "changes"
	PRReviewClose    = "close"
	PRReviewRereview = "rereview"
)

// PRRecordReview is one pipeline marker a review carried.
type PRRecordReview struct {
	ID          string
	RecordID    string
	ReviewID    string
	CommitSHA   string
	MarkerSHA   string
	Kind        string
	OfReviewID  string
	AuthorID    string
	AuthorLogin string
	SubmittedAt time.Time
	ObservedAt  time.Time
}

// PRRecordCheck is the newest run of one check (name, app) on a commit.
type PRRecordCheck struct {
	ID           string
	Provider     string
	RepositoryID string
	HeadSHA      string
	Name         string
	AppSlug      string
	CheckRunID   int64
	Status       string
	Conclusion   string
	CompletedAt  *time.Time
	ObservedAt   time.Time
}

// PR record delivery outcomes.
const (
	PRDeliveryApplied = "applied"
	PRDeliveryStale   = "stale"
	PRDeliveryIgnored = "ignored"
)

// PRRecordDelivery marks one event of a delivery as applied.
type PRRecordDelivery struct {
	Provider    string
	DeliveryKey string
	EventIndex  int
	EventType   string
	RecordID    string
	Outcome     string
	AppliedAt   time.Time
}

// PRRecordChange is everything one event says about pull request records,
// decoded by internal/prrecords and applied by the repository in one
// transaction, once per (provider, delivery key, event index).
type PRRecordChange struct {
	Delivery PRRecordDelivery
	// Snapshot is the pull request the event carried, if any.
	Snapshot *PRRecordSnapshot
	// HeadID is the id for the snapshot's head row when it is new.
	HeadID string
	// PreviousHeadSHA is the head a push replaced (synchronize's before).
	PreviousHeadSHA string
	// Reviews are the markers a review carried; their RecordID is filled in
	// from the snapshot's record.
	Reviews []PRRecordReview
	// Check is a check run the event reported.
	Check *PRRecordCheck
}

// PRRecordFilter narrows a listing of one repository's records.
type PRRecordFilter struct {
	Provider   string
	Repository string
	// State, when set, is one of the PRRecord* states.
	State string
	// AfterNumber pages: only numbers above it, in number order.
	AfterNumber int
	Limit       int
}
