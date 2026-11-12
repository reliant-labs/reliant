// Copyright (c) 2025 Reliant Labs

// Package authz is the one place reliant decides whether a caller may read or
// change a resource. Every handler, tool and placement check asks it instead
// of comparing user ids itself, so the rule can change in one place.
//
// The rule today is ownership: the caller is the resource's owner
// (`user_id`). A missing resource and one the caller may not see are the same
// answer, false, and every call site turns false into NotFound (or the
// PermissionDenied it already returned), so a caller cannot probe which ids
// exist.
//
// Read and Write are separate questions even though they have the same
// answer today. Organizations (control-plane docs/design/orgs-and-members.md
// §5) make the resource's org part of the decision, and read-only sharing
// (§6) lets a member read what they may not write, so each call site asks the
// question it means:
//
//   - Read: show the resource or anything derived from it (messages, runs,
//     plans, questions, the stream).
//   - Write: change it, act on it, or spend through it (send, pause, approve,
//     place work, wake or rename a machine).
//
// ── THE ORG HALF ──────────────────────────────────────────────────────
//
// A resource that belongs to an org (its org_id is set) is the caller's only
// while they are a LIVE member of that org, so a removed member loses the
// org's chats, projects, saved workflows and triggers even though they created
// them (control-plane docs/design/orgs-and-members.md §5). Membership is
// control-plane's: the decision asks the Membership the context carries
// (WithMembership; *orgs.Resolver, cached for orgs.CacheTTL), which every
// process installs once, at its edge: the API and daemon listeners for every
// request, the worker for every activity.
//
// It FAILS CLOSED. An error asking is a denial (the call site's NotFound), and
// so is an org resource decided on a context that carries no Membership at all,
// logged at ERROR because it means a path was never wired. A process's
// background (loops and consumers no listener or activity wraps) decides
// against the membership the process installed at boot
// (SetProcessMembership). A Membership that
// has no answer (a deployment with no control plane, AUTH_MODE=apikey, or a
// control plane without the rpc; IsMember's ok=false) keeps today's rule:
// owner only. Rows with no org (written before orgs) are owner-only too.
//
// A machine credential (an access token) is bound to one org and acts only
// there (WithCredentialOrg, which auth.WithMachineToken installs): it is
// refused an org resource of any other org, and a row with no org yet unless
// its org is the caller's default, the org the backfill stamps it with. A row
// whose org it would inherit (a chat or trigger of an org's project) is
// stamped with that org when it is created or backfilled, so the row's own org
// is what is compared. Kinds with no org yet (machines, trigger events) bind
// nothing: a credential reaches its owner's machines from any org.
//
// ── READ-ONLY SHARING ─────────────────────────────────────────────────
//
// A chat or project its owner shared with its org (visibility 'org';
// control-plane docs/design/org-sharing.md) is readable by every live member
// of that org: a VIEWER. Sharing widens CanReadChat and CanReadProject only;
// every CanWrite*, CanAccessPersonal, and the machine, trigger and saved
// workflow reads keep the owner rule. A viewer must hold a PERSON credential
// (WithCredentialKind, which auth.WithCredential installs at the edge): an
// agent's shell holds its user's CLI login, and a teammate's transcript is
// not something an agent may read into its context. Unset is not a person, so
// a path that never classified its credential refuses viewers. IsViewer tells
// a read site it is serving a viewer (no composer, tool results elided).
//
// Only this package and internal/store read a row's Visibility (guard_test);
// a handler that shows it asks ChatVisibility / ProjectVisibility.
//
// The resource kinds that will belong to an org: chats, projects, machines,
// saved workflows (workflow_drafts), triggers and their events. A workflow
// run (the workflows table) is authorized through its chat, which every run
// has, and inherits the chat's org.
//
// Personal resources (attachments, scenarios, integration connections,
// connector grants, video jobs) are their owner's alone by design and never
// become org-visible through this package: CanAccessPersonal is the one
// spelling of that check.
//
// Store queries that filter `user_id = ?` in SQL (lists, and the
// *WithUserCheck reads) are visibility predicates, not decisions; the
// *WithUserCheck reads in internal/db confirm their row through this package
// too, so the seam sees every single-resource read. Owner-filtered lists keep
// the rows of an org their owner has left, so a list a caller sees passes
// through Visible as well, whether or not the request named an org.
//
//forge:exclude-contract: pure decisions over rows the caller already read; no state, no I/O, nothing to mock
package authz

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Membership answers whether userID is a live member of orgID. ok is false
// when the deployment has no answer (no orgs); err is an outage.
// *orgs.Resolver is one.
type Membership interface {
	IsMember(ctx context.Context, userID, orgID string) (member, ok bool, err error)
}

type membershipKey struct{}

// processMembership is the membership of a decision whose context carries
// none (SetProcessMembership).
var processMembership atomic.Pointer[Membership]

// SetProcessMembership installs m as the membership of every decision whose
// context carries none: the background of a process (its loops, consumers
// and reconcilers, which no listener or activity wraps) once it has built its
// org resolver at boot. A request's or an activity's own membership
// (WithMembership) always wins. nil clears it, and a decision with neither
// fails closed.
func SetProcessMembership(m Membership) {
	if m == nil {
		processMembership.Store(nil)
		return
	}
	processMembership.Store(&m)
}

// membershipOf is the membership a decision on ctx asks: ctx's own, else the
// process's, else nil.
func membershipOf(ctx context.Context) Membership {
	if m, ok := ctx.Value(membershipKey{}).(Membership); ok && m != nil {
		return m
	}
	if m := processMembership.Load(); m != nil {
		return *m
	}
	return nil
}

// WithMembership returns ctx carrying m, which every decision about an org
// resource on that context asks. A process installs it once, at its edge.
func WithMembership(ctx context.Context, m Membership) context.Context {
	return context.WithValue(ctx, membershipKey{}, m)
}

// NoOrgs is the Membership of a deployment without orgs: it never has an
// answer, so every decision is owner-only.
type NoOrgs struct{}

// IsMember implements Membership.
func (NoOrgs) IsMember(context.Context, string, string) (bool, bool, error) { return false, false, nil }

// DefaultOrg names no default org: there are no orgs.
func (NoOrgs) DefaultOrg(context.Context, string) (string, error) { return "", nil }

// defaultOrgs is a Membership that also names a user's default org ("" for
// none, or a deployment with none). *orgs.Resolver is one. A credential bound
// to an org reaches a row with no org yet only through it.
type defaultOrgs interface {
	DefaultOrg(ctx context.Context, userID string) (string, error)
}

type credentialOrgKey struct{}

// credentialBinding is the org a credential is bound to, and the first other
// org it was refused on this request (see CredentialRefusal).
type credentialBinding struct {
	org     string
	mu      sync.Mutex
	refused string
}

// WithCredentialOrg returns ctx acting through a credential bound to orgID:
// every decision on it refuses another org's resources. auth.WithMachineToken
// installs it with the token's org; "" binds nothing.
func WithCredentialOrg(ctx context.Context, orgID string) context.Context {
	if orgID == "" {
		return ctx
	}
	return context.WithValue(ctx, credentialOrgKey{}, &credentialBinding{org: orgID})
}

// boundOrg is the org ctx's credential is bound to, if any.
func boundOrg(ctx context.Context) (string, bool) {
	b, ok := ctx.Value(credentialOrgKey{}).(*credentialBinding)
	if !ok || b == nil {
		return "", false
	}
	return b.org, true
}

// CredentialRefusal reports whether a decision on ctx refused its credential
// a resource of another org: the org the credential is bound to and the org
// it was refused. The call site answers such a refusal as it answers any
// (NotFound); the edge that installed the credential reads this to tell the
// caller why, and how to act in the other org (interceptors.AuthInterceptor).
func CredentialRefusal(ctx context.Context) (bound, refused string, ok bool) {
	b, isBound := ctx.Value(credentialOrgKey{}).(*credentialBinding)
	if !isBound || b == nil {
		return "", "", false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.org, b.refused, b.refused != ""
}

func recordCredentialRefusal(ctx context.Context, org string) {
	if b, ok := ctx.Value(credentialOrgKey{}).(*credentialBinding); ok && b != nil {
		b.mu.Lock()
		if b.refused == "" {
			b.refused = org
		}
		b.mu.Unlock()
	}
}

// CredentialKind is what kind of holder a request's credential has, as far as
// a shared read cares: a person, or software (an agent, a CLI login, a daemon,
// an OAuth client). The zero value is unknown, which is not a person.
type CredentialKind int

const (
	// CredentialKindUnknown is a context no edge classified: not a person.
	CredentialKindUnknown CredentialKind = iota
	// CredentialKindPerson is a credential only a person signed in to the
	// app holds (the class auth.RequirePerson admits).
	CredentialKindPerson
	// CredentialKindSoftware is any credential software can hold.
	CredentialKindSoftware
)

type credentialKindKey struct{}

// WithCredentialKind returns ctx acting through a credential of kind k. The
// edge that authenticates a request installs it (auth.WithCredential), next
// to WithCredentialOrg; this package cannot import auth (auth imports it).
// Only a person may read what another member shared (denyRead).
func WithCredentialKind(ctx context.Context, k CredentialKind) context.Context {
	return context.WithValue(ctx, credentialKindKey{}, k)
}

// personCredential reports whether ctx acts through a person's credential.
// Unset is not: a path that never classified its credential refuses viewers.
func personCredential(ctx context.Context) bool {
	k, _ := ctx.Value(credentialKindKey{}).(CredentialKind)
	return k == CredentialKindPerson
}

// chatRecord is either read of a chat: the activity view (core.Chat) or the
// bare row (core.ChatRow). Both carry the same ownership.
type chatRecord interface {
	*core.Chat | *core.ChatRow
}

// resource is what a decision reads from a row: whether there is one, who
// owns it, the org it belongs to ("" for none), and who besides the owner may
// read it.
type resource struct {
	present     bool
	ownerUserID string
	orgID       string
	// visibility is VisibilityOrg for a row its owner shared with its org.
	// Only denyRead reads it; deny (the write rule) never does.
	visibility core.Visibility
	// orgless marks a kind that does not belong to an org yet (machines,
	// trigger events): no org to bind a credential to, so owner only.
	orgless bool
}

func orgOf(org *string) string {
	if org == nil {
		return ""
	}
	return *org
}

// ErrDenied is a denial with nothing more to say: no such resource, another
// owner's, or a credential bound to another org.
var ErrDenied = errors.New("authz: denied")

// ErrNotOrgMember is the denial of a resource's own owner who is no longer a
// live member of the resource's org: control-plane answered, and the answer
// is no. Only the owner can be told it, so it discloses nothing.
var ErrNotOrgMember = errors.New("authz: the owner is not a live member of the resource's organization")

// ErrMembershipUnavailable is a denial because membership could not be
// checked (an outage, or a path never wired with WithMembership). It fails
// closed like any denial, but it is not an answer: nothing permanent may be
// decided from it.
var ErrMembershipUnavailable = errors.New("authz: could not check organization membership")

// decide is the rule as a yes or no (deny is the rule).
func decide(ctx context.Context, userID string, r resource) bool {
	return deny(ctx, userID, r) == nil
}

// deny is the rule: the owner, and, for a resource in an org, only while
// they are a live member of it. A caller with no id never owns anything, so
// an empty caller and an unowned row are not a match. nil allows; otherwise
// the denial is ErrDenied, ErrNotOrgMember or ErrMembershipUnavailable.
func deny(ctx context.Context, userID string, r resource) error {
	if !r.present || userID == "" || r.ownerUserID != userID {
		return ErrDenied
	}
	if bound, ok := boundOrg(ctx); ok && !r.orgless && !credentialReaches(ctx, userID, bound, r.orgID) {
		return ErrDenied
	}
	if r.orgID == "" {
		return nil
	}
	m := membershipOf(ctx)
	if m == nil {
		logging.ErrorContext(ctx, "[authz] no membership on the context; denying an org resource (a path was never wired with authz.WithMembership)",
			"user_id", userID, "org_id", r.orgID)
		return ErrMembershipUnavailable
	}
	member, ok, err := m.IsMember(ctx, userID, r.orgID)
	if err != nil {
		logging.WarnContext(ctx, "[authz] could not check org membership; denying", "user_id", userID, "org_id", r.orgID, "error", err)
		return ErrMembershipUnavailable
	}
	if !ok || member {
		return nil
	}
	return ErrNotOrgMember
}

// credentialReaches reports whether a credential bound to org bound may act
// on a row of org (or, "", of none yet: its owner's default org). Fails
// closed: a default that cannot be read is a refusal.
func credentialReaches(ctx context.Context, userID, bound, org string) bool {
	if org == "" {
		defaults, ok := membershipOf(ctx).(defaultOrgs)
		if !ok {
			logging.ErrorContext(ctx, "[authz] a credential bound to an org, with no way to read the default org; denying a row with none",
				"user_id", userID, "credential_org", bound)
			return false
		}
		def, err := defaults.DefaultOrg(ctx, userID)
		if err != nil {
			logging.WarnContext(ctx, "[authz] could not read the default org; denying", "user_id", userID, "error", err)
			return false
		}
		if def == "" {
			return true // no orgs here: nothing to bind
		}
		org = def
	}
	if org != bound {
		logging.InfoContext(ctx, "[authz] a credential bound to another org; denying", "user_id", userID, "credential_org", bound, "org_id", org)
		recordCredentialRefusal(ctx, org)
		return false
	}
	return true
}

// denyRead is the read rule: deny (the owner, a live member of the row's org,
// through a credential of that org), or, for a row its owner shared with its
// org, a viewer: a live member of the row's org, holding a person credential
// (WithCredentialKind), through no credential bound to another org. nil
// allows. A row that is not shared answers exactly what deny answers, so
// sharing changes nothing for private rows. A viewer's refusal is ErrDenied,
// or ErrMembershipUnavailable when membership could not be asked (fail
// closed, logged like deny's); never ErrNotOrgMember, which only an owner may
// be told.
func denyRead(ctx context.Context, userID string, r resource) error {
	if !r.present || userID == "" || r.ownerUserID == userID {
		return deny(ctx, userID, r)
	}
	if r.visibility != core.VisibilityOrg || r.orgID == "" || r.orgless {
		return ErrDenied
	}
	if !personCredential(ctx) {
		return ErrDenied
	}
	if bound, ok := boundOrg(ctx); ok && bound != r.orgID {
		logging.InfoContext(ctx, "[authz] a credential bound to another org; denying a shared read", "user_id", userID, "credential_org", bound, "org_id", r.orgID)
		recordCredentialRefusal(ctx, r.orgID)
		return ErrDenied
	}
	m := membershipOf(ctx)
	if m == nil {
		logging.ErrorContext(ctx, "[authz] no membership on the context; denying a shared read (a path was never wired with authz.WithMembership)",
			"user_id", userID, "org_id", r.orgID)
		return ErrMembershipUnavailable
	}
	member, ok, err := m.IsMember(ctx, userID, r.orgID)
	if err != nil {
		logging.WarnContext(ctx, "[authz] could not check org membership; denying a shared read", "user_id", userID, "org_id", r.orgID, "error", err)
		return ErrMembershipUnavailable
	}
	// A deployment with no answer (no orgs) has no viewers: owner only.
	if !ok || !member {
		return ErrDenied
	}
	return nil
}

// isViewer is denyRead allowing a caller who is not the owner.
func isViewer(ctx context.Context, userID string, r resource) bool {
	return r.present && userID != "" && r.ownerUserID != userID && denyRead(ctx, userID, r) == nil
}

func chatResource[C chatRecord](chat C) resource {
	switch c := any(chat).(type) {
	case *core.Chat:
		if c != nil {
			return resource{present: true, ownerUserID: c.UserID, orgID: orgOf(c.OrgID), visibility: c.Visibility}
		}
	case *core.ChatRow:
		if c != nil {
			return resource{present: true, ownerUserID: c.UserID, orgID: orgOf(c.OrgID), visibility: c.Visibility}
		}
	}
	return resource{}
}

// CanReadChat reports whether userID may see chat and what hangs off it:
// its messages, runs, plans, tasks, questions and live stream: its owner, or,
// once shared with its org, a viewer (see denyRead). nil is false.
func CanReadChat[C chatRecord](ctx context.Context, userID string, chat C) bool {
	return denyRead(ctx, userID, chatResource(chat)) == nil
}

// IsViewer reports whether userID reads chat as a viewer: CanReadChat allows
// it and userID is not its owner. A read site uses it to serve the read-only
// shape (no composer, tool results elided, the owner's path omitted).
func IsViewer[C chatRecord](ctx context.Context, userID string, chat C) bool {
	return isViewer(ctx, userID, chatResource(chat))
}

// ChatVisibility is who besides its owner may read chat, for display (the
// owner's share toggle). Deciding from it is this package's alone.
func ChatVisibility[C chatRecord](chat C) core.Visibility {
	return chatResource(chat).visibility
}

// CanWriteChat reports whether userID may change chat or act in it: send,
// branch, pause, resume, approve, answer, place its work. nil is false.
func CanWriteChat[C chatRecord](ctx context.Context, userID string, chat C) bool {
	return decide(ctx, userID, chatResource(chat))
}

func projectResource(p *core.Project) resource {
	if p == nil {
		return resource{}
	}
	return resource{present: true, ownerUserID: p.UserID, orgID: orgOf(p.OrgID), visibility: p.Visibility}
}

// CanReadProject reports whether userID may see project: its owner, or, once
// shared with its org, a viewer (see denyRead). A viewer sees the project
// card and its shared chats, never an unshared chat in it. nil is false.
func CanReadProject(ctx context.Context, userID string, project *core.Project) bool {
	return denyRead(ctx, userID, projectResource(project)) == nil
}

// IsProjectViewer reports whether userID reads project as a viewer:
// CanReadProject allows it and userID is not its owner.
func IsProjectViewer(ctx context.Context, userID string, project *core.Project) bool {
	return isViewer(ctx, userID, projectResource(project))
}

// ProjectVisibility is who besides its owner may read project, for display.
func ProjectVisibility(project *core.Project) core.Visibility {
	return projectResource(project).visibility
}

// CanWriteProject reports whether userID may change project or run work in
// it: its worktrees, terminals, checkouts and chats. nil is false.
func CanWriteProject(ctx context.Context, userID string, project *core.Project) bool {
	return decide(ctx, userID, projectResource(project))
}

// WriteProjectDenial is why CanWriteProject is false: nil when it is true,
// else ErrDenied, ErrNotOrgMember or ErrMembershipUnavailable. A call site
// that must act on the difference (an unattended launch, which pauses a
// trigger whose creator left its org but must not on an outage) asks this.
func WriteProjectDenial(ctx context.Context, userID string, project *core.Project) error {
	return deny(ctx, userID, projectResource(project))
}

func machineResource(d *core.Daemon) resource {
	if d == nil {
		return resource{}
	}
	return resource{present: true, ownerUserID: d.UserID, orgless: true}
}

// CanReadMachine reports whether userID may see machine's registration.
// nil is false.
func CanReadMachine(ctx context.Context, userID string, machine *core.Daemon) bool {
	return decide(ctx, userID, machineResource(machine))
}

// CanWriteMachine reports whether userID may act on machine: run work or a
// terminal on it, browse its files, wake, rename or clean it. nil is false.
func CanWriteMachine(ctx context.Context, userID string, machine *core.Daemon) bool {
	return decide(ctx, userID, machineResource(machine))
}

func workflowResource(w *core.WorkflowDraft) resource {
	if w == nil {
		return resource{}
	}
	return resource{present: true, ownerUserID: w.UserID, orgID: orgOf(w.OrgID)}
}

// CanReadWorkflow reports whether userID may see a saved workflow
// definition, or run it in a chat they may write. nil is false.
func CanReadWorkflow(ctx context.Context, userID string, workflow *core.WorkflowDraft) bool {
	return decide(ctx, userID, workflowResource(workflow))
}

// CanWriteWorkflow reports whether userID may change, rename, publish or
// delete a saved workflow definition. nil is false.
func CanWriteWorkflow(ctx context.Context, userID string, workflow *core.WorkflowDraft) bool {
	return decide(ctx, userID, workflowResource(workflow))
}

func triggerResource(t *core.Trigger) resource {
	if t == nil {
		return resource{}
	}
	return resource{present: true, ownerUserID: t.UserID, orgID: orgOf(t.OrgID)}
}

// CanReadTrigger reports whether userID may see trigger. nil is false.
func CanReadTrigger(ctx context.Context, userID string, trigger *core.Trigger) bool {
	return decide(ctx, userID, triggerResource(trigger))
}

// CanWriteTrigger reports whether userID may change, enable, fire, replay or
// delete trigger: anything that makes it launch runs. nil is false.
func CanWriteTrigger(ctx context.Context, userID string, trigger *core.Trigger) bool {
	return decide(ctx, userID, triggerResource(trigger))
}

// WriteTriggerDenial is why CanWriteTrigger is false: nil when it is true,
// else ErrDenied, ErrNotOrgMember or ErrMembershipUnavailable. A firing asks
// it for the trigger's own creator, whose identity its runs execute as.
func WriteTriggerDenial(ctx context.Context, userID string, trigger *core.Trigger) error {
	return deny(ctx, userID, triggerResource(trigger))
}

// CanReadTriggerEvent reports whether userID may see one firing of a trigger
// (or an ad hoc launch event). nil is false.
func CanReadTriggerEvent(ctx context.Context, userID string, event *core.TriggerEvent) bool {
	if event == nil {
		return decide(ctx, userID, resource{})
	}
	return decide(ctx, userID, resource{present: true, ownerUserID: event.UserID, orgless: true})
}

// Visible returns the rows of a list that can lets userID read, in order: a
// collection shows exactly what the single-resource read would. Membership
// is cached per org (orgs.CacheTTL), so a list costs one lookup per distinct
// org, not per row. rows is not modified.
func Visible[T any](ctx context.Context, userID string, rows []T, can func(context.Context, string, T) bool) []T {
	out := make([]T, 0, len(rows))
	for _, row := range rows {
		if can(ctx, userID, row) {
			out = append(out, row)
		}
	}
	return out
}

// CanReadOwn reports whether userID may still see a row of their own that
// belongs to org (nil: none): the org half of every decision, for a row a
// store read already scoped to its owner (a run log row, a user update),
// which carries the org of the resource it is about but not that resource.
func CanReadOwn(ctx context.Context, userID string, org *string) bool {
	return decide(ctx, userID, resource{present: true, ownerUserID: userID, orgID: orgOf(org)})
}

// CanAccessPersonal reports whether userID owns a personal resource whose
// owner is ownerUserID. Personal resources are never shared, so there is no
// read/write split and no org: only the owner. An empty caller owns nothing.
//
// The caller checks presence (a nil row) itself, since these rows have no
// common type.
func CanAccessPersonal(userID, ownerUserID string) bool {
	return userID != "" && ownerUserID == userID
}
