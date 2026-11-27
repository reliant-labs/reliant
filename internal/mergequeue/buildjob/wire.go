// Copyright (c) 2025 Reliant Labs
package buildjob

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// The daemon commands that run the queue's code operations on a member of
// the repository's gate pool. One generic, idempotent protocol carries every
// kind: CommandStart starts a job (or answers for the one already running
// under its id), CommandStatus reads it, CommandCancel stops it. Start and
// status are safe to send again after a dropped connection (toolexec
// reissuableDaemonCommands lists them). A daemon older than these commands
// answers that it does not support them; the server treats that member as
// one that refused the job.
//
// The gate keeps its own commands (internal/mergequeue/gaterun,
// pipeline.gate.*): it runs pull-request code in an isolation boundary and
// has a credential posture of its own.
const (
	CommandStart  = "pipeline.job.start"
	CommandStatus = "pipeline.job.status"
	CommandCancel = "pipeline.job.cancel"
)

// Kind is what a job does.
type Kind string

const (
	// KindBuild builds a batch's candidate: a BuildRequest in, a
	// BuildResult out.
	KindBuild Kind = "build"
	// KindPaths lists the paths a head changes: a PathsRequest in, a
	// PathsResult out.
	KindPaths Kind = "paths"
	// KindConfig reads one file of the repository at a commit (its
	// .reliant/pipeline.yaml): a ConfigRequest in, a ConfigResult out. The
	// server parses and validates what it reads; the daemon reports bytes,
	// never a resolved config.
	KindConfig Kind = "config"
)

// ConfigRequest asks for one file at one commit.
type ConfigRequest struct {
	// Repository is owner/name.
	Repository string `json:"repository"`
	// SHA is the commit to read the file at.
	SHA string `json:"sha"`
	// Path is repository-relative, with no leading slash, under .reliant/.
	Path string `json:"path"`
}

// ConfigResult is the file, or that there is none at the commit.
type ConfigResult struct {
	Found bool `json:"found"`
	// Bytes are the file's contents, at most ConfigMaxBytes.
	Bytes []byte `json:"bytes,omitempty"`
}

// ConfigMaxBytes bounds a config job's file. A larger file is not read: the
// job reports an error, and the server records the config as unreadable.
const ConfigMaxBytes = 64 << 10

// ConfigDir is the directory a config job may read in.
const ConfigDir = ".reliant/"

// JobState is a job's state on its daemon.
type JobState string

const (
	JobRunning JobState = "running"
	JobDone    JobState = "done"
	// JobBusy: the member runs all the jobs it takes; try another.
	JobBusy JobState = "busy"
	// JobRefused: the member cannot run this job (an invalid request, or a
	// machine with no git).
	JobRefused JobState = "refused"
	// JobUnknown: the member does not know the job id (it restarted, or the
	// result was pruned).
	JobUnknown JobState = "unknown"
)

// StartRequest is CommandStart's payload: one job, whose kind names the one
// body it carries.
type StartRequest struct {
	// JobID names the job on the daemon and is the idempotency key of every
	// command for it. The server makes a new one per job it dispatches.
	JobID  string         `json:"job_id"`
	Kind   Kind           `json:"kind"`
	Build  *BuildRequest  `json:"build,omitempty"`
	Paths  *PathsRequest  `json:"paths,omitempty"`
	Config *ConfigRequest `json:"config,omitempty"`
	// MaxConcurrent is how many jobs the member runs at once (the pool's
	// max_runs_per_member); 0 is 1.
	MaxConcurrent int `json:"max_concurrent,omitempty"`
}

// StartReply is CommandStart's answer.
type StartReply struct {
	State  JobState `json:"state"`
	Reason string   `json:"reason,omitempty"`
}

// StatusRequest is CommandStatus's (and CommandCancel's) payload.
type StatusRequest struct {
	JobID string `json:"job_id"`
}

// StatusReply is CommandStatus's answer. When State is done, exactly one of
// the kind's result or Error is set: Error means the job reached no answer
// (a git or GitHub failure), and a new job with the same request is safe.
type StatusReply struct {
	State  JobState      `json:"state"`
	Build  *BuildResult  `json:"build,omitempty"`
	Paths  *PathsResult  `json:"paths,omitempty"`
	Config *ConfigResult `json:"config,omitempty"`
	Error  string        `json:"error,omitempty"`
}

// ErrInvalidRequest is a job request that cannot run as given.
var ErrInvalidRequest = errors.New("invalid pipeline job request")

var (
	jobIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)
	repoPattern    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
	batchIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
	shaPattern     = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
)

// ValidJobID reports whether id is a job id the protocol accepts.
func ValidJobID(id string) bool { return jobIDPattern.MatchString(id) }

// Repository is the owner/name the request's body names.
func (r StartRequest) Repository() string {
	switch {
	case r.Build != nil:
		return r.Build.Repository
	case r.Paths != nil:
		return r.Paths.Repository
	case r.Config != nil:
		return r.Config.Repository
	}
	return ""
}

// Validate checks a StartRequest: the job id, that the kind carries its one
// body, and the body's shape. The daemon's job checks the rest (a build's
// entries, say) before it acts.
func (r StartRequest) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, a...))
	}
	if !jobIDPattern.MatchString(r.JobID) {
		return bad("job id %q", r.JobID)
	}
	bodies := 0
	for _, set := range []bool{r.Build != nil, r.Paths != nil, r.Config != nil} {
		if set {
			bodies++
		}
	}
	if bodies != 1 {
		return bad("a job carries exactly one body, this one %d", bodies)
	}
	if !repoPattern.MatchString(r.Repository()) {
		return bad("repository %q is not owner/name", r.Repository())
	}
	switch r.Kind {
	case KindBuild:
		if r.Build == nil {
			return bad("a build job carries no build request")
		}
		return r.Build.validate()
	case KindPaths:
		if r.Paths == nil {
			return bad("a paths job carries no paths request")
		}
		if !shaPattern.MatchString(r.Paths.Base) || !shaPattern.MatchString(r.Paths.Head) {
			return bad("paths need two commit shas")
		}
		return nil
	case KindConfig:
		if r.Config == nil {
			return bad("a config job carries no config request")
		}
		return r.Config.validate()
	}
	return bad("kind %q", r.Kind)
}

func (b BuildRequest) validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, a...))
	}
	switch {
	case !batchIDPattern.MatchString(b.BatchID):
		return bad("batch id %q", b.BatchID)
	case !shaPattern.MatchString(b.Base):
		return bad("base %q is not a commit sha", b.Base)
	case len(b.Entries) == 0:
		return bad("a batch needs at least one entry")
	}
	for _, e := range b.Entries {
		if e.Number <= 0 || !shaPattern.MatchString(e.Head) {
			return bad("entry #%d at %q", e.Number, e.Head)
		}
	}
	return nil
}

func (c ConfigRequest) validate() error {
	switch {
	case !shaPattern.MatchString(c.SHA):
		return fmt.Errorf("%w: sha %q is not a commit sha", ErrInvalidRequest, c.SHA)
	case !ValidConfigPath(c.Path):
		return fmt.Errorf("%w: path %q is not a file under %s", ErrInvalidRequest, c.Path, ConfigDir)
	}
	return nil
}

// ValidConfigPath reports whether p is a path a config job may read: clean,
// relative, under ConfigDir, and no longer than 255 bytes.
func ValidConfigPath(p string) bool {
	return len(p) <= 255 && path.Clean(p) == p && strings.HasPrefix(p, ConfigDir) && len(p) > len(ConfigDir) &&
		!strings.Contains(p, "\\") && !strings.ContainsAny(p, "\x00\n")
}

// QueueRef is the ref a batch's candidate is pushed to.
func QueueRef(batchID string) string { return QueueRefPrefix + batchID }

// QueueRefPrefix is every candidate ref's prefix.
const QueueRefPrefix = "refs/heads/reliant-queue/"
