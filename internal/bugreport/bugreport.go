// Package bugreport files a defect in forge itself with the Reliant
// engineering team: `forge report-bug`.
//
// Inside Reliant an agent files with reliant's own report_bug tool, whose
// record the worker's log pipeline ships to our telemetry. A forge running in
// Claude Code, Codex or Cursor, or at a person's terminal, has no such
// pipeline, so it posts the report to the control plane's public ingest,
// controlplane.v1.ForgeReportService/FileBugReport, which writes the same kind
// of issue record (control-plane docs/design/forge-cli-reports.md).
//
// forge sends NOTHING unless someone files a report. There is no automatic
// crash or error reporting and no telemetry. What a report carries is listed
// in docs/report-bug.md; Payload is all of it.
//
// The report id is minted here, so it can be printed whatever the ingest
// answers, and a repeat of a report filed in the last 30 minutes returns the
// first report's id without sending anything (State).
//
//forge:exclude-contract: one concrete filer over one HTTP endpoint and one state file; tests substitute the endpoint and the state directory
package bugreport

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/redact"
)

const (
	// Procedure is the ingest rpc.
	Procedure = "controlplane.v1.ForgeReportService/FileBugReport"

	// EndpointEnv overrides the control plane reports go to (a dev or
	// staging control plane). Unset, reports go to cloud.DefaultEndpoint.
	EndpointEnv = "FORGE_REPORT_ENDPOINT"

	// DedupeWindow is how long a filed report answers a repeat of itself.
	DedupeWindow = 30 * time.Minute

	// sendTimeout bounds the one request a report makes.
	sendTimeout = 15 * time.Second

	// MaxTitleBytes bounds redacted UTF-8 title bytes including the marker.
	MaxTitleBytes = 200
	// MaxTextBytes bounds redacted UTF-8 text bytes including the marker.
	MaxTextBytes        = 4000
	maxCommandLineBytes = 2000
	maxVersionBytes     = 128
	maxHarnessBytes     = 64
	maxRuntimeBytes     = 32
	maxInstallIDBytes   = 64
)

// Severities are the accepted severities, most severe first.
func Severities() []string { return []string{"critical", "high", "medium", "low"} }

// Report is what the reporter writes. Its JSON form is what `forge report-bug
// --json` reads.
type Report struct {
	// Severity is critical (data loss, or nothing can proceed), high (it
	// blocks the task), medium (there is a workaround) or low (cosmetic).
	Severity string `json:"severity"`
	// Title is one line naming the defect; ids and numbers go in Evidence.
	Title    string `json:"title"`
	Summary  string `json:"summary,omitempty"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	// Evidence is how to reproduce it: commands, output, file paths.
	Evidence string `json:"evidence,omitempty"`
}

// Validate reports what is wrong with r, or nil.
func (r Report) Validate() error {
	var problems []string
	sev := strings.ToLower(strings.TrimSpace(r.Severity))
	known := false
	for _, s := range Severities() {
		known = known || sev == s
	}
	if !known {
		problems = append(problems, fmt.Sprintf("severity must be one of %s (got %q)", strings.Join(Severities(), ", "), r.Severity))
	}
	if strings.TrimSpace(r.Title) == "" {
		problems = append(problems, "title is required")
	} else if strings.ContainsAny(strings.TrimSpace(r.Title), "\r\n") {
		problems = append(problems, "title must be one line; put detail in summary or evidence")
	}
	if strings.TrimSpace(r.Summary+r.Expected+r.Actual+r.Evidence) == "" {
		problems = append(problems, "describe the defect: set at least one of summary, expected, actual or evidence")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// Context is what forge knows about where it ran. The caller supplies it;
// nothing here reads the environment beyond the OS and architecture.
type Context struct {
	// ForgeVersion is this binary's version (buildinfo.Version()).
	ForgeVersion string
	// ProjectForgeVersion is forge.yaml's forge_version of the project forge
	// ran in, "" outside one.
	ProjectForgeVersion string
	// Harness is the agent harness forge detected, "" for a person.
	Harness string
	// Args is the command line (os.Args). It is redacted by CommandLine.
	Args []string
}

// ClientContext is the wire form of Context.
type ClientContext struct {
	ForgeVersion        string `json:"forgeVersion,omitempty"`
	OS                  string `json:"os,omitempty"`
	Arch                string `json:"arch,omitempty"`
	CommandLine         string `json:"commandLine,omitempty"`
	ProjectForgeVersion string `json:"projectForgeVersion,omitempty"`
	Harness             string `json:"harness,omitempty"`
	InstallID           string `json:"installId,omitempty"`
	GoVersion           string `json:"goVersion,omitempty"`
}

// Payload is exactly what is sent: FileBugReportRequest in protojson.
type Payload struct {
	ReportID string        `json:"reportId"`
	Severity string        `json:"severity"`
	Title    string        `json:"title"`
	Summary  string        `json:"summary,omitempty"`
	Expected string        `json:"expected,omitempty"`
	Actual   string        `json:"actual,omitempty"`
	Evidence string        `json:"evidence,omitempty"`
	Client   ClientContext `json:"client"`
}

// BuildPayload redacts and bounds r and c into the payload a report sends,
// under reportID and installID.
func BuildPayload(reportID, installID string, r Report, c Context) Payload {
	return Payload{
		ReportID: reportID,
		Severity: strings.ToLower(strings.TrimSpace(r.Severity)),
		Title:    redact.Bounded(strings.TrimSpace(r.Title), MaxTitleBytes),
		Summary:  redact.Bounded(r.Summary, MaxTextBytes),
		Expected: redact.Bounded(r.Expected, MaxTextBytes),
		Actual:   redact.Bounded(r.Actual, MaxTextBytes),
		Evidence: redact.Bounded(r.Evidence, MaxTextBytes),
		Client: ClientContext{
			ForgeVersion:        redact.Bounded(c.ForgeVersion, maxVersionBytes),
			OS:                  redact.Bounded(runtime.GOOS, maxRuntimeBytes),
			Arch:                redact.Bounded(runtime.GOARCH, maxRuntimeBytes),
			CommandLine:         CommandLine(c.Args),
			ProjectForgeVersion: redact.Bounded(c.ProjectForgeVersion, maxVersionBytes),
			Harness:             redact.Bounded(c.Harness, maxHarnessBytes),
			InstallID:           redact.Bounded(installID, maxInstallIDBytes),
			GoVersion:           redact.Bounded(runtime.Version(), maxRuntimeBytes),
		},
	}
}

// secretFlagRE matches a flag whose value is a credential by its name.
var secretFlagRE = regexp.MustCompile(`(?i)(token|secret|password|passwd|api[_-]?key|credential|dsn|auth)`)

// reportFlagRE matches report-bug's own text flags: the report is already
// in the payload, so the command line says only that they were set.
var reportFlagRE = regexp.MustCompile(`^--?(title|summary|expected|actual|evidence)$`)

// CommandLine is args joined into one line with every value that could hold
// a secret removed: the value of a secret-named flag becomes redact.Marker,
// the value of a report text flag becomes "…", and every remaining argument
// is redact.Text'd (credential shapes, home directories). The program path
// is reduced to its base name.
func CommandLine(args []string) string {
	out := make([]string, 0, len(args))
	elideNext, redactNext := false, false
	for i, a := range args {
		switch {
		case i == 0:
			a = baseName(a)
		case elideNext:
			a, elideNext = "…", false
		case redactNext:
			a, redactNext = redact.Marker, false
		case strings.HasPrefix(a, "-"):
			name, _, hasValue := strings.Cut(a, "=")
			switch {
			case reportFlagRE.MatchString(name):
				if hasValue {
					a = name + "=…"
				} else {
					elideNext = true
				}
			case secretFlagRE.MatchString(name):
				if hasValue {
					a = name + "=" + redact.Marker
				} else {
					redactNext = true
				}
			default:
				a = redact.Text(a)
			}
		default:
			a = redact.Text(a)
		}
		out = append(out, a)
	}
	return redact.Bounded(strings.Join(out, " "), maxCommandLineBytes)
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// Filer files reports.
type Filer struct {
	// Endpoint is the control plane's base URL.
	Endpoint string
	// Token is a forge login to present, "" for none. It only adds the
	// caller's ids to the record; the ingest ignores one it cannot validate.
	Token string
	// State is where the install id and recent reports are kept.
	State *State
	// Now is the clock; tests pin it.
	Now func() time.Time
	// send replaces the HTTP call in tests.
	send func(ctx context.Context, p Payload) error
}

// Result is a filed report.
type Result struct {
	// ReportID is the report's id: this report's, or the earlier one a
	// duplicate was answered with.
	ReportID string `json:"report_id"`
	// Duplicate: the same report was filed within DedupeWindow, and nothing
	// was sent.
	Duplicate bool `json:"duplicate,omitempty"`
	// FiledAt is when ReportID was filed.
	FiledAt time.Time `json:"filed_at"`
}

// DefaultEndpoint is EndpointEnv when set, cloud.DefaultEndpoint otherwise.
func DefaultEndpoint() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv(EndpointEnv)), "/"); v != "" {
		return v
	}
	return cloud.DefaultEndpoint
}

// File validates r, answers a repeat with the first report's id, and
// otherwise sends it and records it. A report the ingest refused is not
// recorded, so filing it again retries.
func (f *Filer) File(ctx context.Context, r Report, c Context) (Result, error) {
	if err := r.Validate(); err != nil {
		return Result{}, err
	}
	now := f.now()
	key := Fingerprint(r.Title)
	installID, prior, ok := f.State.Lookup(key, now)
	if ok {
		return Result{ReportID: prior.ID, Duplicate: true, FiledAt: prior.FiledAt}, nil
	}
	p := BuildPayload(newUUID(), installID, r, c)
	if err := f.doSend(ctx, p); err != nil {
		return Result{}, err
	}
	f.State.Record(key, p.ReportID, now)
	return Result{ReportID: p.ReportID, FiledAt: now}, nil
}

// Preview is the payload File would send for r, under a placeholder id.
// Nothing is sent or recorded.
func (f *Filer) Preview(r Report, c Context) (Payload, error) {
	if err := r.Validate(); err != nil {
		return Payload{}, err
	}
	installID, _, _ := f.State.Lookup(Fingerprint(r.Title), f.now())
	return BuildPayload("00000000-0000-4000-8000-000000000000", installID, r, c), nil
}

func (f *Filer) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *Filer) doSend(ctx context.Context, p Payload) error {
	if f.send != nil {
		return f.send(ctx, p)
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	client := cloud.NewClient(cloud.Endpoint{URL: f.Endpoint}, cloud.Credential{Token: f.Token})
	if err := client.Call(ctx, Procedure, p, nil); err != nil {
		var apiErr *cloud.Error
		if errors.As(err, &apiErr) && apiErr.HasCode("resource_exhausted") {
			return fmt.Errorf("the report was not filed: too many reports from this machine or network; try again later")
		}
		return fmt.Errorf("the report was not filed: %w", err)
	}
	return nil
}

// maxFingerprintRunes bounds the normalized title a fingerprint keeps.
const maxFingerprintRunes = 120

// Fingerprint names the defect a title describes, for local deduplication:
// the title redacted, lowercased, its words kept and every word holding a
// digit dropped (ids, counts and ports vary between two sightings of one
// defect). reliant's report_bug deduplicates by the same rule.
func Fingerprint(title string) string {
	words := strings.FieldsFunc(strings.ToLower(redact.Secrets(title)), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	kept := words[:0]
	for _, w := range words {
		if !strings.ContainsFunc(w, unicode.IsDigit) {
			kept = append(kept, w)
		}
	}
	s := strings.Join(kept, " ")
	if utf8.RuneCountInString(s) > maxFingerprintRunes {
		s = strings.TrimSpace(string([]rune(s)[:maxFingerprintRunes]))
	}
	if s == "" {
		return "untitled"
	}
	return s
}

// newUUID is a random (version 4) UUID.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails (Go 1.24+)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
