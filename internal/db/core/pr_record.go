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
	// HeadRepository is owner/name of the repository the head branch lives
	// in: a fork's for a fork's pull request, "" when the event did not say
	// (GitHub sends no head repository once it was deleted). Not stored on
	// the record: only the chat link inference reads it (chat_pr_links), to
	// tell the PR's own branch from a fork's branch of the same name.
	HeadRepository string
	// Delta is what this event's own action changed. GitHub's updated_at has
	// one-second resolution, so two events of one pull request can carry the
	// same snapshot time; then neither snapshot is newer, and each applies
	// only its delta.
	Delta PRRecordDelta
}

// PRRecordDelta is the transition one event made, applied on a snapshot-time
// tie. A zero field changes nothing.
type PRRecordDelta struct {
	// LabelAdded / LabelRemoved: labeled / unlabeled.
	LabelAdded   string
	LabelRemoved string
	// State: closed (or merged, with MergedAt and MergeCommit) / reopened.
	// A merged record never leaves merged.
	State       string
	MergedAt    *time.Time
	MergeCommit string
	// Draft: ready_for_review (false) / converted_to_draft (true).
	Draft *bool
	// FromHead -> HeadSHA: synchronize. The head moves only from FromHead,
	// so two pushes in one second applied out of order cannot move it back.
	FromHead string
	HeadSHA  string
	HeadRef  string
	// Title and BaseRef: edited.
	Title   string
	BaseRef string
}

// PRRecordHead is one head a pull request has had.
type PRRecordHead struct {
	ID              string
	RecordID        string
	HeadSHA         string
	PreviousHeadSHA string
	// PushedSnapshotAt is the snapshot time of the push that recorded
	// PreviousHeadSHA (nil when no push named it).
	PushedSnapshotAt *time.Time
	FirstSeenAt      time.Time
	SupersededAt     *time.Time
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

// PRRecordFilter narrows a listing of one repository's records, in
// (number, repository id) order.
type PRRecordFilter struct {
	Provider   string
	Repository string
	// RepositoryIDs, when set, are the only repository ids listed: a name
	// may have named another repository (deleted and re-created) before.
	RepositoryIDs []string
	// State, when set, is one of the PRRecord* states.
	State string
	// AfterNumber and AfterRepositoryID page: only records after that key.
	AfterNumber       int
	AfterRepositoryID string
	Limit             int
}

// PRRecordRepository is one repository identity a repository name has had.
type PRRecordRepository struct {
	AccountKey   string
	RepositoryID string
}
