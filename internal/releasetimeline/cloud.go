package releasetimeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/obsquery"
	"github.com/reliant-labs/forge/pkg/release"
)

// The control-plane procedures the timeline reads. Called by name with the
// local JSON structs below, the way cloud.ResolveOrganization does, so forge
// takes no dependency on the control plane's generated types.
//
// Scopes: every DeployService read is authorized by deploy:read; ListActivity
// by obs:read. Ask the credential helper for both.
const (
	procListEnvironments = "controlplane.v1.DeployService/ListEnvironments"
	procListPromotions   = "controlplane.v1.DeployService/ListPromotions"
	procListReleases     = "controlplane.v1.DeployService/ListReleases"
	procListConvergences = "controlplane.v1.DeployService/ListConvergences"
	procCompareReleases  = "controlplane.v1.DeployService/CompareReleases"
	procListActivity     = "controlplane.v1.ActivityService/ListActivity"
	procGetTrace         = "controlplane.v1.ObservabilityService/GetTrace"
	procGetReleaseHealth = "controlplane.v1.ObservabilityService/GetReleaseHealth"
)

// Scopes are the access-token scopes a cloud read needs.
var Scopes = []string{"deploy:read", "obs:read"}

// pageLimit is how many promotions one ListPromotions page asks for.
const pageLimit = 100

// Caller is the one method the cloud source needs; *cloud.Client satisfies it.
type Caller interface {
	Call(ctx context.Context, procedure string, req, out any) error
}

// Cloud reads a control plane's ledger.
type Cloud struct {
	Client  Caller
	Project string
}

type wireGate struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	URL     string `json:"url"`
	Summary string `json:"summary"`
}

type wirePromotion struct {
	ID                  string     `json:"id"`
	BundleID            string     `json:"bundleId"`
	ReleaseVersion      string     `json:"releaseVersion"`
	FromEnvironmentName string     `json:"fromEnvironmentName"`
	PromotedByUserID    string     `json:"promotedByUserId"`
	PromotedByActor     string     `json:"promotedByActor"`
	Gates               []wireGate `json:"gates"`
	RecordedGates       []wireGate `json:"recordedGates"`
	Targets             []string   `json:"targets"`
	Note                string     `json:"note"`
	SupersededInFlight  bool       `json:"supersededInFlight"`
	CreatedAt           time.Time  `json:"createdAt"`
	Run                 *struct {
		URL      string `json:"url"`
		Provider string `json:"provider"`
	} `json:"run"`
}

type wireCommit struct {
	SHA     string    `json:"sha"`
	Subject string    `json:"subject"`
	Author  string    `json:"author"`
	Time    time.Time `json:"time"`
}

type wireRelease struct {
	Version    string    `json:"version"`
	Project    string    `json:"project"`
	GitCommit  string    `json:"gitCommit"`
	CreatedAt  time.Time `json:"createdAt"`
	Provenance *struct {
		Branch string `json:"branch"`
	} `json:"provenance"`
	Commits *struct {
		BaseRelease string       `json:"baseRelease"`
		BaseCommit  string       `json:"baseCommit"`
		Commits     []wireCommit `json:"commits"`
		Total       int          `json:"total"`
		Truncated   bool         `json:"truncated"`
	} `json:"commits"`
}

type wireConvergence struct {
	ID             string    `json:"id"`
	PromotionID    string    `json:"promotionId"`
	State          string    `json:"state"`
	Reason         string    `json:"reason"`
	Message        string    `json:"message"`
	Cluster        string    `json:"cluster"`
	ObservedAt     time.Time `json:"observedAt"`
	FirstAttemptAt time.Time `json:"firstAttemptAt"`
	HealthyAt      time.Time `json:"healthyAt"`
}

type wireActivity struct {
	At    time.Time `json:"at"`
	Actor struct {
		Name string `json:"name"`
	} `json:"actor"`
	Verb   string `json:"verb"`
	Target struct {
		ID string `json:"id"`
	} `json:"target"`
	Via    string            `json:"via"`
	Detail map[string]string `json:"detail"`
}

// NewCloud reads a control plane's ledger for one forge project ("" for every
// project's releases).
func NewCloud(client Caller, project string) Cloud {
	return Cloud{Client: client, Project: project}
}

// Load reads env's ledger through the control plane. An environment the
// control plane has never heard of has no history, and says so in a warning
// rather than failing: a typo and a never-deployed env look the same here.
func (c Cloud) Load(ctx context.Context, env string, since time.Time) (Data, error) {
	d := Data{Env: env}
	id, err := c.envID(ctx, env)
	if err != nil {
		return d, err
	}
	if id == "" {
		d.Warnings = append(d.Warnings, fmt.Sprintf("the control plane has no environment named %q", env))
		return d, nil
	}

	var newestFirst []wirePromotion
	before := ""
	for {
		req := map[string]any{"environmentId": id, "limit": pageLimit}
		if before != "" {
			req["beforePromotionId"] = before
		}
		var resp struct {
			Promotions []wirePromotion `json:"promotions"`
			Next       string          `json:"nextBeforePromotionId"`
		}
		if err := c.Client.Call(ctx, procListPromotions, req, &resp); err != nil {
			return d, fmt.Errorf("list promotions of %s: %w", env, err)
		}
		newestFirst = append(newestFirst, resp.Promotions...)
		before = resp.Next
		if before == "" {
			break
		}
		last := resp.Promotions[len(resp.Promotions)-1]
		if len(newestFirst) >= maxPromotions || (!since.IsZero() && last.CreatedAt.Before(since)) {
			d.Truncated = true
			break
		}
	}
	for i := len(newestFirst) - 1; i >= 0; i-- {
		d.Promotions = append(d.Promotions, promotionFromWire(env, newestFirst[i]))
	}

	var rels struct {
		Releases []wireRelease `json:"releases"`
	}
	relReq := map[string]any{"limit": 1000}
	if c.Project != "" {
		relReq["project"] = c.Project
	}
	if err := c.Client.Call(ctx, procListReleases, relReq, &rels); err != nil {
		d.Warnings = append(d.Warnings, fmt.Sprintf("releases unreadable, directions fall back to unknown: %v", err))
	}
	for _, r := range rels.Releases {
		d.Releases = append(d.Releases, releaseFromWire(r))
	}

	var convs struct {
		Convergences []wireConvergence `json:"convergences"`
	}
	if err := c.Client.Call(ctx, procListConvergences, map[string]any{"environmentId": id, "limit": 200}, &convs); err != nil {
		d.Warnings = append(d.Warnings, fmt.Sprintf("convergences unreadable, rollouts and failed deploys are not shown: %v", err))
	}
	for _, v := range convs.Convergences {
		d.Attempts = append(d.Attempts, attemptFromWire(v))
	}

	d.Activity, err = c.activity(ctx, id, since)
	if err != nil {
		d.Warnings = append(d.Warnings, fmt.Sprintf("activity unreadable, who/via comes from the ledger only: %v", err))
	}
	return d, nil
}

// CompareReleases asks the control plane what changed between two releases,
// for the environment's bundles. The caller decodes the part of the answer it
// shows into out.
func CompareReleases(ctx context.Context, c Cloud, from, to, envName string, out any) error {
	id, err := c.envID(ctx, envName)
	if err != nil {
		return err
	}
	req := map[string]any{"fromVersion": from, "toVersion": to, "project": c.Project}
	if id != "" {
		req["environmentId"] = id
	}
	return c.Client.Call(ctx, procCompareReleases, req, out)
}

func (c Cloud) envID(ctx context.Context, env string) (string, error) {
	req := map[string]any{"search": env}
	if c.Project != "" {
		req["project"] = c.Project
	}
	var resp struct {
		Environments []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Project string `json:"project"`
		} `json:"environments"`
	}
	if err := c.Client.Call(ctx, procListEnvironments, req, &resp); err != nil {
		return "", fmt.Errorf("resolve environment %q: %w", env, err)
	}
	var ids []string
	for _, e := range resp.Environments {
		if e.Name == env && (e.Project == "" || c.Project == "" || e.Project == c.Project) {
			ids = append(ids, e.ID)
		}
	}
	switch len(ids) {
	case 0:
		return "", nil
	case 1:
		return ids[0], nil
	}
	return "", fmt.Errorf("the control plane returned %d environments named %q; refusing to guess", len(ids), env)
}

func (c Cloud) activity(ctx context.Context, envID string, since time.Time) ([]Activity, error) {
	org, err := cloud.ResolveOrganization(ctx, c.Client)
	if err != nil {
		return nil, err
	}
	scope := map[string]any{"orgId": org, "envIds": []string{envID}}
	if !since.IsZero() {
		scope["timeRange"] = map[string]any{"start": since.UTC().Format(time.RFC3339)}
	}
	var resp struct {
		Events []wireActivity `json:"events"`
	}
	req := map[string]any{"scope": scope, "verb": "promote", "pageSize": 200}
	if err := c.Client.Call(ctx, procListActivity, req, &resp); err != nil {
		return nil, err
	}
	out := make([]Activity, 0, len(resp.Events))
	for _, e := range resp.Events {
		out = append(out, Activity{
			At: e.At, Verb: e.Verb, TargetID: e.Target.ID,
			ActorName: e.Actor.Name, Via: e.Via, Detail: e.Detail,
		})
	}
	return out, nil
}

func promotionFromWire(env string, w wirePromotion) release.Promotion {
	p := release.Promotion{
		ID: w.ID, BundleID: w.BundleID, Env: env, Release: w.ReleaseVersion, Kind: release.KindPromote,
		FromEnv:    w.FromEnvironmentName,
		PromotedBy: release.Actor{User: w.PromotedByUserID, Actor: w.PromotedByActor},
		Gates:      gatesFromWire(w.Gates), RecordedGates: gatesFromWire(w.RecordedGates),
		Targets: w.Targets, Note: w.Note, SupersededInFlight: w.SupersededInFlight,
		PromotedAt: w.CreatedAt,
	}
	if w.Run != nil {
		p.Run = release.Run{URL: w.Run.URL, Provider: w.Run.Provider}
	}
	return p
}

func gatesFromWire(ws []wireGate) []release.Gate {
	var out []release.Gate
	for _, w := range ws {
		status, raw := release.GateStatusFromStored(w.Status)
		out = append(out, release.Gate{Name: w.Name, Status: status, RawStatus: raw, URL: w.URL, Summary: w.Summary})
	}
	return out
}

func releaseFromWire(w wireRelease) release.Release {
	r := release.Release{Version: w.Version, Project: w.Project, CreatedAt: w.CreatedAt, Git: release.Git{Commit: w.GitCommit}}
	if w.Provenance != nil {
		r.Provenance = &release.Provenance{Branch: w.Provenance.Branch}
	}
	if w.Commits != nil {
		cr := &release.CommitRange{
			BaseRelease: w.Commits.BaseRelease, BaseCommit: w.Commits.BaseCommit,
			Commits: []release.Commit{}, Total: w.Commits.Total, Truncated: w.Commits.Truncated,
		}
		for _, c := range w.Commits.Commits {
			cr.Commits = append(cr.Commits, release.Commit{SHA: c.SHA, Subject: c.Subject, Author: c.Author, Time: c.Time})
		}
		r.Commits = cr
	}
	return r
}

// attemptFromWire reads a convergence: the control plane's own observation
// that an environment did, or did not, reach the intent a promotion names.
func attemptFromWire(w wireConvergence) Attempt {
	a := Attempt{ID: w.ID, PromotionID: w.PromotionID, Cluster: w.Cluster, StartedAt: w.FirstAttemptAt}
	if w.State == "failed" {
		a.Status = StatusFailed
		a.FinishedAt = w.ObservedAt
		a.Reason = firstNonEmpty(w.Reason, w.Message)
		if w.Reason != "" && w.Message != "" {
			a.Reason = w.Reason + ": " + w.Message
		}
		return a
	}
	a.Status = StatusSucceeded
	a.FinishedAt = w.HealthyAt
	if a.FinishedAt.IsZero() {
		a.FinishedAt = w.ObservedAt
	}
	return a
}

// ErrTraceNotFound is returned by TraceStart when the control plane holds no
// span of the trace in its retention window.
var ErrTraceNotFound = errors.New("no such trace in the last 7 days")

// traceWindow is how far back a trace is looked for: the traces TTL, and the
// most GetTrace accepts. A minute short of it, so the boundary itself is never
// the reason a request is refused.
const traceWindow = 7*24*time.Hour - time.Minute

// TraceStart returns when a trace began (its earliest span), which is the
// instant `env releases at` asks "which release was live" about.
func TraceStart(ctx context.Context, c Cloud, env, traceID string, now time.Time) (time.Time, error) {
	tid, err := obsquery.NormalizeTraceID(traceID)
	if err != nil {
		return time.Time{}, err
	}
	scope, err := c.obsScope(ctx, env)
	if err != nil {
		return time.Time{}, err
	}
	scope["timeRange"] = map[string]any{
		"start": now.Add(-traceWindow).UTC().Format(time.RFC3339),
		"end":   now.UTC().Format(time.RFC3339),
	}
	var resp struct {
		Spans []struct {
			StartTime time.Time `json:"startTime"`
		} `json:"spans"`
	}
	if err := c.Client.Call(ctx, procGetTrace, map[string]any{"scope": scope, "traceId": tid}, &resp); err != nil {
		return time.Time{}, fmt.Errorf("get trace %s: %w", tid, err)
	}
	var first time.Time
	for _, s := range resp.Spans {
		if !s.StartTime.IsZero() && (first.IsZero() || s.StartTime.Before(first)) {
			first = s.StartTime
		}
	}
	if first.IsZero() {
		return time.Time{}, fmt.Errorf("%w: %s", ErrTraceNotFound, tid)
	}
	return first.UTC(), nil
}

// ReleaseHealth asks the observability service whether a promotion made
// things worse. Callers treat a failure as "no verdict", not as a failed read
// of the timeline: a control plane that predates the RPC answers Unimplemented.
func ReleaseHealth(ctx context.Context, c Cloud, env, promotionID string) (Health, error) {
	scope, err := c.obsScope(ctx, env)
	if err != nil {
		return Health{}, err
	}
	var resp struct {
		Verdict       string `json:"verdict"`
		AfterComplete bool   `json:"afterComplete"`
		Findings      []struct {
			Message string `json:"message"`
		} `json:"findings"`
	}
	req := map[string]any{"scope": scope, "promotionId": promotionID}
	if err := c.Client.Call(ctx, procGetReleaseHealth, req, &resp); err != nil {
		return Health{}, fmt.Errorf("get release health of %s: %w", promotionID, err)
	}
	verdict := strings.ToLower(strings.TrimPrefix(resp.Verdict, "RELEASE_VERDICT_"))
	if verdict == "" || verdict == "unspecified" {
		return Health{}, fmt.Errorf("the control plane returned no verdict for %s", promotionID)
	}
	h := Health{Verdict: verdict}
	switch {
	case len(resp.Findings) > 0:
		h.Detail = resp.Findings[0].Message
		if n := len(resp.Findings); n > 1 {
			h.Detail += fmt.Sprintf(" (+%d more)", n-1)
		}
	case !resp.AfterComplete:
		h.Detail = "the after window is still open"
	}
	return h, nil
}

// obsScope is the QueryScope fields every observability read carries: the
// org, the one environment, and the project when there is one.
func (c Cloud) obsScope(ctx context.Context, env string) (map[string]any, error) {
	id, err := c.envID(ctx, env)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("the control plane has no environment named %q", env)
	}
	org, err := cloud.ResolveOrganization(ctx, c.Client)
	if err != nil {
		return nil, err
	}
	scope := map[string]any{"orgId": org, "envIds": []string{id}}
	if c.Project != "" {
		scope["projectId"] = c.Project
	}
	return scope, nil
}
