package cli

// Which dev instance a `forge obs … <env>` reads (control-plane
// docs/design/dev-telemetry-to-hosted.md §1, §6). A local env whose
// `forge env up` sent its telemetry to the hosted store (the recorded
// .forge/obs/backend-<env>.json) shares that env with every other stack of it:
// ten worktrees on one machine, and every teammate's. So a read defaults to
// THIS checkout's instance, the one the record names:
//
//	(default)                 this checkout's instance
//	--instance <id|label>     that instance (a teammate's, or an earlier run)
//	--all-instances           the whole env
//	--user <user>             every instance of one user
//
// An instance id is the control plane's: the deploy_local_sessions row id the
// mint returned (a UUID; rows from before the server-minted ids keep the
// client's sess-… id), which the token carries as `sid` and the gateway stamps
// as reliant.instance.id. It is NOT forge's own session id (sessionIDFor),
// which repeats across a worktree's runs. A label resolves through the records
// `forge env up` wrote on this machine; the control plane's Live view reports
// only the client's session id, so it cannot name another machine's instance.
//
// The narrowing is the backend's (obsbackend.Instances), so every read a verb
// makes carries it, and the document's scope.instance says which it was.

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/pflag"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/devtelemetry"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
)

// obsInstanceFlags are --instance, --all-instances and --user.
type obsInstanceFlags struct {
	instance string
	all      bool
	user     string
	// userFlag names --user; a verb whose own --user means something else
	// (sessions: the app's user id) registers it as --instance-user.
	userFlag string

	// carry is the non-default narrowing a suggested `forge obs` read repeats
	// so it reads what this one read; set by resolve.
	carry instanceCarry
}

// instanceCarry is a narrowing by what it means, not by the flags that spelt
// it: each destination verb spells the user selection its own way.
type instanceCarry struct {
	all  bool
	id   string
	user string
}

func (f *obsInstanceFlags) register(fs *pflag.FlagSet) {
	if f.userFlag == "" {
		f.userFlag = "user"
	}
	fs.StringVar(&f.instance, "instance", "", "read this dev `instance` of a hosted-dev env: its id (scope.instance.id, the control plane's) or its label (alice/fix-login, or the branch alone) as forge env up recorded it on this machine (default: this checkout's)")
	fs.BoolVar(&f.all, "all-instances", false, "read every dev instance of the env, not only this checkout's")
	fs.StringVar(&f.user, f.userFlag, "", "read every dev instance of one `user` (the reliant.instance.user the gateway stamps; the control plane resolves an email)")
}

func (f *obsInstanceFlags) flagUser() string {
	if f.userFlag == "" {
		return "user"
	}
	return f.userFlag
}

// instanceIDRe is the shape of a control-plane instance id: the server-minted
// row id (a UUID), or the client id a row from before server-minted ids kept.
var instanceIDRe = regexp.MustCompile(`^([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|sess-[0-9a-f]{24})$`)

// instanceRecords reads the hosted-dev records of env on this machine.
type instanceRecords func(ctx context.Context, env string) ([]devtelemetry.Record, error)

// resolve turns the flags into the narrowing a backend applies. rec is the
// hosted-dev record the env resolved through (nil otherwise); records resolve
// a label that is not the record's own.
func (f *obsInstanceFlags) resolve(ctx context.Context, env string, rec *devtelemetry.Record, records instanceRecords) (obsbackend.Instances, error) {
	f.carry = instanceCarry{}
	if f.all && (f.instance != "" || f.user != "") {
		return obsbackend.Instances{}, obsbackend.Wrong("--all-instances reads every instance: drop --instance and --%s, or drop it", f.flagUser())
	}
	if f.all {
		f.carry.all = rec != nil
		return obsbackend.Instances{}, nil
	}
	var out obsbackend.Instances
	if f.user != "" {
		out.Users, out.Owner = []string{f.user}, f.user
		f.carry.user = f.user
	}
	switch v := strings.TrimSpace(f.instance); {
	case v == "":
		// The default: this checkout's instance, unless --user widened it.
		if f.user == "" && rec != nil {
			out = recordInstance(*rec)
		}
	case rec != nil && (v == rec.InstanceID || v == rec.InstanceLabel):
		out = withInstance(out, recordInstance(*rec))
		if f.user != "" {
			f.carry.id = rec.InstanceID
		}
	case instanceIDRe.MatchString(v):
		out.IDs = []string{v}
		f.carry.id = v
	default:
		r, err := instanceByLabel(ctx, env, v, records)
		if err != nil {
			return obsbackend.Instances{}, err
		}
		out = withInstance(out, recordInstance(r))
		f.carry.id = r.InstanceID
	}
	return out, nil
}

func recordInstance(rec devtelemetry.Record) obsbackend.Instances {
	return obsbackend.Instances{IDs: []string{rec.InstanceID}, Label: rec.InstanceLabel, Owner: rec.InstanceUser}
}

// withInstance narrows out to in's instance, keeping out's user selection.
func withInstance(out, in obsbackend.Instances) obsbackend.Instances {
	out.IDs, out.Label = in.IDs, in.Label
	if out.Owner == "" {
		out.Owner = in.Owner
	}
	return out
}

// instanceByLabel finds the one hosted-dev record of env on this machine
// whose label is label. An instance label is "<user>/<worktree label>", and a
// worktree label may itself hold a "/": a label matches in full, or the
// worktree part alone (everything after the user) matches a bare one. A
// qualified label never matches another user's instance.
func instanceByLabel(ctx context.Context, env, label string, records instanceRecords) (devtelemetry.Record, error) {
	if records == nil {
		return devtelemetry.Record{}, obsbackend.Wrong("--instance %q: pass the instance's id (scope.instance.id)", label)
	}
	recs, err := records(ctx, env)
	if err != nil {
		return devtelemetry.Record{}, obsbackend.Undetermined("--instance %q: cannot read this machine's records of env %s to resolve the label; pass the instance's id (scope.instance.id)", label, env).Cause(err)
	}
	var exact, bare []devtelemetry.Record
	var known []string
	for _, r := range recs {
		if r.InstanceID == "" || r.InstanceLabel == "" {
			continue
		}
		known = append(known, r.InstanceLabel)
		if r.InstanceLabel == label {
			exact = append(exact, r)
		}
		if _, wt, ok := strings.Cut(r.InstanceLabel, "/"); ok && wt == label {
			bare = append(bare, r)
		}
	}
	hits := exact
	if len(hits) == 0 {
		hits = bare
	}
	hits = slices.CompactFunc(hits, func(a, b devtelemetry.Record) bool { return a.InstanceID == b.InstanceID })
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		hint := "none is recorded here"
		if len(known) > 0 {
			slices.Sort(known)
			hint = "recorded here: " + strings.Join(slices.Compact(known), ", ")
		}
		return devtelemetry.Record{}, obsbackend.Wrong("--instance %q names no instance of env %s that `forge env up` recorded on this machine (%s); another machine's instance is read by its id (scope.instance.id)", label, env, hint)
	}
	ids := make([]string, len(hits))
	for i, r := range hits {
		ids[i] = r.InstanceID + " (" + r.InstanceLabel + ")"
	}
	return devtelemetry.Record{}, obsbackend.Wrong("--instance %q names %d instances of env %s; pick one by id: %s", label, len(hits), env, strings.Join(ids, ", "))
}

// machineInstanceRecords reads env's hosted-dev records from this checkout
// and every other worktree of its repository: the instances `forge env up`
// started on this machine.
func machineInstanceRecords(ctx context.Context, env string) ([]devtelemetry.Record, error) {
	projectDir := projectDirForKCL()
	dirs := []string{projectDir}
	if top, err := checkoutGit(ctx, projectDir, "rev-parse", "--show-toplevel"); err == nil {
		rel, rerr := filepath.Rel(strings.TrimSpace(top), projectDir)
		if wts, werr := listWorktrees(ctx, projectDir); werr == nil && rerr == nil {
			for _, wt := range wts {
				dirs = append(dirs, filepath.Join(wt.path, rel))
			}
		}
	}
	var out []devtelemetry.Record
	seen := map[string]bool{}
	for _, d := range dirs {
		abs, err := filepath.Abs(d)
		if err != nil || seen[abs] {
			continue
		}
		seen[abs] = true
		// An unreadable record names nothing; this checkout's own is warned
		// about where the env resolves (readDevTelemetry).
		rec, ok, err := devtelemetry.Read(abs, env)
		if err == nil && ok && rec.Backend == "cloud" {
			out = append(out, rec)
		}
	}
	return out, nil
}

// carrySuggested repeats a non-default instance narrowing on every suggested
// `forge obs` telemetry read, spelt as that read's verb takes it, so following
// a suggestion reads the same instance.
func (f *obsInstanceFlags) carrySuggested(cmds []string) []string {
	if f.carry == (instanceCarry{}) {
		return cmds
	}
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = c
		if verb, ok := instanceCarryVerb(c); ok {
			out[i] = c + " " + obsout.Command(f.carry.args(verb)...)
		}
	}
	return out
}

// args spells the narrowing for verb: `sessions` takes the instance's user as
// --instance-user (its --user is the app's user id), every other verb as --user.
func (c instanceCarry) args(verb string) []string {
	if c.all {
		return []string{"--all-instances"}
	}
	var a []string
	if c.user != "" {
		flag := "--user"
		if verb == "sessions" {
			flag = "--instance-user"
		}
		a = append(a, flag, c.user)
	}
	if c.id != "" {
		a = append(a, "--instance", c.id)
	}
	return a
}

// instanceCarryVerb is the verb of a suggested command that is a telemetry
// read taking the instance flags; ok is false for anything else.
func instanceCarryVerb(cmd string) (string, bool) {
	rest, ok := strings.CutPrefix(cmd, "forge obs ")
	if !ok || strings.Contains(cmd, " --help") || strings.Contains(cmd, "--instance") || strings.Contains(cmd, "--all-instances") {
		return "", false
	}
	verb, _, _ := strings.Cut(rest, " ")
	switch verb {
	case "changes", "check", "dashboards", "alerts", "profile":
		return "", false
	}
	return verb, true
}

// obsScopeInstance is the document's scope.instance for i; nil for a read of
// the whole env.
func obsScopeInstance(i obsbackend.Instances) *obsout.ScopeInstance {
	if !i.Narrows() {
		return nil
	}
	s := &obsout.ScopeInstance{Label: i.Label, User: i.Owner}
	if len(i.IDs) > 0 {
		s.ID = i.IDs[0]
	}
	return s
}

// openObsHostedDev builds the cloud backend for a hosted-dev env from its
// record: the env's control plane and the caller's credential, bound to the
// org and environment the record names. No hosted-env lookup runs: a local
// env is not a deploy target, and the record already says which it is.
func openObsHostedDev(ctx context.Context, env string, rec devtelemetry.Record) (obsbackend.Cloud, error) {
	t, err := resolveObsTarget(ctx, env)
	if err != nil {
		return obsbackend.Cloud{}, obsbackend.Undetermined("env %s: cannot read its declaration", env).Cause(err).
			With(obsbackend.Cmd("forge", "env", "list"))
	}
	ep, cred, err := resolveCloudTargetFor(env, t.Declaration(), "")
	if err != nil {
		return obsbackend.Cloud{}, obsbackend.Undetermined("env %s: cannot reach its control plane", env).Cause(err)
	}
	return obsbackend.Cloud{Env: env, EnvID: rec.EnvironmentID, OrgID: rec.OrgID, Caller: cloud.NewClient(ep, cred)}, nil
}

// readDevTelemetry reads env's recorded telemetry decision. A record that
// cannot be read is reported and ignored: the env then resolves as if `forge
// env up` had recorded nothing.
func readDevTelemetry(projectDir, env string, warn func(string)) *devtelemetry.Record {
	rec, ok, err := devtelemetry.Read(projectDir, env)
	if err != nil {
		warn(fmt.Sprintf("ignoring %s: %v", devtelemetry.Path(projectDir, env), err))
		return nil
	}
	if !ok {
		return nil
	}
	return &rec
}

// hostedDevBackend is a hosted-dev env's backend: its telemetry is read from
// the control plane, its changes from this machine's ledger, as for any local
// env (a local env has no promotions on the control plane).
type hostedDevBackend struct {
	obsbackend.Cloud
	ledger obsbackend.ChangesReader
}

// Changes implements obsbackend.ChangesReader.
func (h hostedDevBackend) Changes(ctx context.Context, req obsbackend.ChangesRequest) (obsbackend.ChangesResult, error) {
	if h.ledger == nil {
		return obsbackend.ChangesResult{}, obsbackend.Undetermined("no ledger of env %s's changes on this machine", h.Env)
	}
	return h.ledger.Changes(ctx, req)
}

// instanceName is how a text header names an instance: its label and id, or
// whichever it has, and its user.
func instanceName(in obsout.ScopeInstance) string {
	name := in.ID
	switch {
	case in.Label != "" && in.ID != "":
		name = in.Label + " (" + in.ID + ")"
	case in.Label != "":
		name = in.Label
	case in.ID == "":
		name = "every instance"
	}
	if in.User != "" && !strings.HasPrefix(in.Label, in.User) {
		name += " of " + in.User
	}
	return name
}
