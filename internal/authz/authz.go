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
// CanOwnChat, every CanWrite*, CanAccessPersonal, and the machine, trigger
// and saved workflow reads keep the owner rule. A viewer must hold a PERSON credential
// (WithCredentialKind, which auth.WithCredential installs at the edge): an
// agent's shell holds its user's CLI login, and a teammate's transcript is
// not something an agent may read into its context. Unset is not a person, so
// a path that never classified its credential refuses viewers. A read site
// admits viewers only once it opts in (WithViewerReads) by shaping its answer
// for them; ReadChatAccess / ReadProjectAccess decide, once, whether it is
// serving one (no composer, tool results elided), and every site that has not
// opted in stays owner-only.
//
// Only this package and internal/store read a row's Visibility (guard_test);
// a handler that shows it asks ChatVisibility / ProjectVisibility.
//
// ── CHAT MEMBERS ──────────────────────────────────────────────────────
//
// A chat's owner, and its members at manage, may grant a person a level on it
// (chat_members: view, reply, manage; control-plane
// docs/design/chat-collaboration.md). A grant admits its holder only while
// they are a live member of the chat's org, acting as a person, through no
// credential bound to another org, and is read uncached at every decision
// from the Grants the context or the process carries (grants.go), so a revoke
// bites at once. A grant at any level reads the chat, even a private one, at a
// site that serves viewers (ReadChatAccess answers its level); CanReplyChat
// and CanManageChat answer the levels above, and visibility never changes
// them. Every action on a chat asks CanOwnChat until a PR moves its site, with
// its test: a site nobody moves stays owner-only. Only this package and
// internal/store read a chat_members row (guard_test).
//
// The resource kinds that will belong to an org: chats, projects, machines,
// saved workflows (workflow_drafts), triggers and their events. A workflow
// run (the workflows table) is authorized through its chat, which every run
// has, and inherits the chat's org.
//
// Personal resources (attachments, scenarios, integration connections,
// connector grants, video jobs) are their owner's alone by design and never
// become org-visible through this package: CanAccessPersonal is the one
// spelling of that check. An org's own connections (owner_kind 'org') are a
// different thing, owned by no member: OrgMemberDenial is the rule for using
// one.
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
	// chatID is the chat's id, set only for a chat: the one kind that has
	// per-person grants (chat_members, grants.go).
	chatID string
	// visibility is VisibilityOrg for a row its owner shared with its org.
	// Only denyRead reads it; deny (the write rule) and memberDeny never do.
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
// through a credential of that org), or a reader who is not the owner: a live
// member of the row's org, holding a person credential (WithCredentialKind),
// through no credential bound to another org, on a read site that serves
// viewers (WithViewerReads), who either reads a row its owner shared with its
// org or holds a grant on the chat (chat_members, any level). nil allows. A
// row that is not shared, read by someone holding no grant, answers exactly
// what deny answers, so sharing and grants change nothing for anyone else. A
// non-owner's refusal is ErrDenied, or ErrMembershipUnavailable when
// membership or a grant could not be asked (fail closed, logged like deny's);
// never ErrNotOrgMember, which only an owner may be told.
func denyRead(ctx context.Context, userID string, r resource) error {
	_, err := readDecision(ctx, userID, r, false)
	return err
}

// readDecision is denyRead with the level the caller reads at, when level is
// asked for: the owner's, a member's (their grant's level), or a viewer's. A
// shared chat's org-wide viewer reads whether or not their grant can be read,
// because the org-wide view never depended on one: a grant that cannot be
// read (an outage, no Grants wired) adds nothing, and is logged. Without
// level, a shared row's read asks no grant at all.
func readDecision(ctx context.Context, userID string, r resource, level bool) (Access, error) {
	if !r.present || userID == "" || r.ownerUserID == userID {
		if err := deny(ctx, userID, r); err != nil {
			return AccessNone, err
		}
		return AccessOwner, nil
	}
	if r.orgID == "" || r.orgless {
		return AccessNone, ErrDenied
	}
	shared := r.visibility == core.VisibilityOrg
	if !shared && r.chatID == "" {
		return AccessNone, ErrDenied
	}
	if !servesViewers(ctx) || !personCredential(ctx) {
		return AccessNone, ErrDenied
	}
	if bound, ok := boundOrg(ctx); ok && bound != r.orgID {
		logging.InfoContext(ctx, "[authz] a credential bound to another org; denying a shared read", "user_id", userID, "credential_org", bound, "org_id", r.orgID)
		recordCredentialRefusal(ctx, r.orgID)
		return AccessNone, ErrDenied
	}
	var role core.ChatMemberRole
	if !shared {
		// A private chat reads only through a grant, asked first: a caller
		// who holds none is refused exactly as before grants existed, and
		// membership is never asked about them.
		var err error
		if role, err = chatGrant(ctx, userID, r); err != nil {
			return AccessNone, err
		}
		if role == "" {
			return AccessNone, ErrDenied
		}
	}
	if err := liveMember(ctx, userID, r.orgID, "a shared read"); err != nil {
		return AccessNone, err
	}
	if shared && level && r.chatID != "" {
		if got, err := chatGrant(ctx, userID, r); err == nil {
			role = got
		}
	}
	return accessFor(role), nil
}

// liveMember is nil when userID is a live member of orgID: the membership
// half of every decision about someone who is not the row's owner. It fails
// closed: no Membership on ctx (logged at ERROR: a path never wired) and an
// outage are ErrMembershipUnavailable; a non-member, and a deployment with no
// answer (no orgs, so no one but an owner), are ErrDenied. what names the
// decision in the log.
func liveMember(ctx context.Context, userID, orgID, what string) error {
	m := membershipOf(ctx)
	if m == nil {
		logging.ErrorContext(ctx, "[authz] no membership on the context; denying "+what+" (a path was never wired with authz.WithMembership)",
			"user_id", userID, "org_id", orgID)
		return ErrMembershipUnavailable
	}
	member, ok, err := m.IsMember(ctx, userID, orgID)
	if err != nil {
		logging.WarnContext(ctx, "[authz] could not check org membership; denying "+what, "user_id", userID, "org_id", orgID, "error", err)
		return ErrMembershipUnavailable
	}
	if !ok || !member {
		return ErrDenied
	}
	return nil
}

type viewerReadsKey struct{}

// WithViewerReads marks ctx as a read site that serves viewers: one that
// shapes what it returns for a viewer (ReadChatAccess: tool results elided, the
// owner's path, workspace, machine and composer choice left out) and changes
// nothing of the owner's state by being read. Only on such a context does
// CanReadChat / CanReadProject admit a viewer; every other read site keeps
// the owner rule, so a site nobody migrated cannot leak a shared row in the
// owner's shape. A site opts in deliberately, with the redaction and a viewer
// test beside it.
func WithViewerReads(ctx context.Context) context.Context {
	return context.WithValue(ctx, viewerReadsKey{}, true)
}

// servesViewers reports whether ctx's read site serves viewers.
func servesViewers(ctx context.Context) bool {
	v, _ := ctx.Value(viewerReadsKey{}).(bool)
	return v
}

// isViewer is denyRead allowing a caller who is not the owner.
func isViewer(ctx context.Context, userID string, r resource) bool {
	return r.present && userID != "" && r.ownerUserID != userID && denyRead(ctx, userID, r) == nil
}

// Access is how a caller reads a chat or project: as its owner, as a member
// holding a grant on a chat (manage, reply), or as a viewer (a view grant, or
// any live member of the org of a row its owner shared). A read site that
// serves viewers decides it ONCE (ReadChatAccess / ReadProjectAccess) and
// shapes its whole answer from that value. It never asks again: a second
// decision can differ from the first (a membership answer expiring, an
// outage, a revoke), and a viewer whose second answer came back "not a
// viewer" must not be handed the owner's shape.
type Access uint8

const (
	// AccessNone is no access; it is only ever returned with an error.
	AccessNone Access = iota
	// AccessOwner is the owner's read: the whole answer.
	AccessOwner
	// AccessViewer is a viewer's read: the read-only, redacted answer.
	AccessViewer
	// AccessReply is a reply member's read. Shaped as a viewer's
	// (Viewer); it says only that the caller holds reply on the chat.
	AccessReply
	// AccessManage is a manage member's read. Shaped as a viewer's
	// (Viewer); it says only that the caller holds manage on the chat.
	AccessManage
)

// Viewer reports whether an answer must be shaped for a viewer: every access
// but the owner's, so a zero value fails closed, and a member at any level
// reads what a viewer reads.
func (a Access) Viewer() bool { return a != AccessOwner }

// Role is the grant level the access was read through: view for a viewer
// (a view grant, or none on a shared row), reply or manage for a member, and
// empty for the owner and no access.
func (a Access) Role() core.ChatMemberRole {
	switch a {
	case AccessViewer:
		return core.ChatMemberView
	case AccessReply:
		return core.ChatMemberReply
	case AccessManage:
		return core.ChatMemberManage
	}
	return ""
}

// accessFor is a non-owner's Access for the grant they read through ("" for
// none: an org-wide viewer).
func accessFor(role core.ChatMemberRole) Access {
	switch role {
	case core.ChatMemberManage:
		return AccessManage
	case core.ChatMemberReply:
		return AccessReply
	}
	return AccessViewer
}

// readAccess is the read rule's verdict as an Access: the owner (decided by
// the row, which no membership answer changes), a member, or a viewer.
func readAccess(ctx context.Context, userID string, r resource) (Access, error) {
	return readDecision(ctx, userID, r, true)
}

// ReadChatAccess is CanReadChat's decision with how the caller reads: the
// owner, or a viewer of a shared chat (only on a WithViewerReads context).
// An error is a refusal (ErrDenied, ErrNotOrgMember, ErrMembershipUnavailable).
// Shape the answer from the returned Access; never decide again.
func ReadChatAccess[C chatRecord](ctx context.Context, userID string, chat C) (Access, error) {
	return readAccess(ctx, userID, chatResource(chat))
}

// ReadProjectAccess is ReadChatAccess for a project.
func ReadProjectAccess(ctx context.Context, userID string, project *core.Project) (Access, error) {
	return readAccess(ctx, userID, projectResource(project))
}

// chatResource is the chat as every decision reads it. A chat of a project
// scheduled for deletion is absent, as the project is (project_delete.go).
func chatResource[C chatRecord](chat C) resource {
	switch c := any(chat).(type) {
	case *core.Chat:
		if c != nil && !c.ProjectDeleted {
			return resource{present: true, ownerUserID: c.UserID, orgID: orgOf(c.OrgID), chatID: c.ID, visibility: c.Visibility}
		}
	case *core.ChatRow:
		if c != nil && !c.ProjectDeleted {
			return resource{present: true, ownerUserID: c.UserID, orgID: orgOf(c.OrgID), chatID: c.ID, visibility: c.Visibility}
		}
	}
	return resource{}
}

// CanReadChat reports whether userID may see chat and what hangs off it:
// its messages, runs, plans, tasks, questions and live stream: its owner, a
// member holding a grant on it at any level, or, once shared with its org, a
// viewer (see denyRead). nil is false.
func CanReadChat[C chatRecord](ctx context.Context, userID string, chat C) bool {
	return denyRead(ctx, userID, chatResource(chat)) == nil
}

// SharedChatsFor is the chats of chats userID reads as a viewer: shared with
// an org they are a live member of, and not their own. The shared list's
// filter; its rows are always answered in the viewer's shape.
func SharedChatsFor[C chatRecord](ctx context.Context, userID string, chats []C) []C {
	return Visible(ctx, userID, chats, func(ctx context.Context, userID string, c C) bool {
		return isViewer(ctx, userID, chatResource(c))
	})
}

// ChatVisibility is who besides its owner may read chat, for display (the
// owner's share toggle). Deciding from it is this package's alone.
func ChatVisibility[C chatRecord](chat C) core.Visibility {
	return chatResource(chat).visibility
}

// CanOwnChat reports whether userID is chat's owner, and may still act as
// one: a live member of its org, through a credential of that org. It is what
// every action on a chat asked before collaboration (send, branch, pause,
// resume, approve, answer, place its work), and every such site still asks
// it: a site moves to CanReplyChat or CanManageChat only deliberately, with its
// test (control-plane docs/design/chat-collaboration.md §4.1). nil is false.
func CanOwnChat[C chatRecord](ctx context.Context, userID string, chat C) bool {
	return decide(ctx, userID, chatResource(chat))
}

// CanReplyChat reports whether userID may act in chat as a reply member:
// its owner (CanOwnChat), or a member holding reply or manage on it who is a
// live member of its org, acting as a person through no credential bound to
// another org (memberDeny). The chat's visibility never changes the answer:
// an org-wide viewer never replies. nil is false.
func CanReplyChat[C chatRecord](ctx context.Context, userID string, chat C) bool {
	return memberDeny(ctx, userID, chatResource(chat), core.ChatMemberReply) == nil
}

// CanManageChat reports whether userID may manage chat: its owner
// (CanOwnChat), or a member holding manage on it, decided as CanReplyChat
// decides. nil is false.
func CanManageChat[C chatRecord](ctx context.Context, userID string, chat C) bool {
	return memberDeny(ctx, userID, chatResource(chat), core.ChatMemberManage) == nil
}

// projectResource is the project as every decision reads it. A project
// scheduled for deletion is absent (project_delete.go).
func projectResource(p *core.Project) resource {
	if p == nil || p.DeleteAfter != nil {
		return resource{}
	}
	return liveProjectResource(p)
}

// CanReadProject reports whether userID may see project: its owner, or, once
// shared with its org, a viewer (see denyRead). A viewer sees the project
// card and its shared chats, never an unshared chat in it. nil is false.
func CanReadProject(ctx context.Context, userID string, project *core.Project) bool {
	return denyRead(ctx, userID, projectResource(project)) == nil
}

// SharedProjectsFor is SharedChatsFor for projects.
func SharedProjectsFor(ctx context.Context, userID string, projects []*core.Project) []*core.Project {
	return Visible(ctx, userID, projects, func(ctx context.Context, userID string, p *core.Project) bool {
		return isViewer(ctx, userID, projectResource(p))
	})
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

// workflowResource is the saved workflow as every decision reads it. Its
// visibility is read only by the shared reads (ReadWorkflowAccess,
// SharedWorkflowDraftsFor) and WorkflowShareChange: CanReadWorkflow and
// CanWriteWorkflow decide with deny, which never reads it.
func workflowResource(w *core.WorkflowDraft) resource {
	if w == nil {
		return resource{}
	}
	return resource{present: true, ownerUserID: w.UserID, orgID: orgOf(w.OrgID), visibility: w.Visibility}
}

// CanReadWorkflow reports whether userID may see a saved workflow
// definition, or run it in a chat they may write: its owner alone, even once
// it is shared (a viewer reads a shared one only through ReadWorkflowAccess,
// never by slug, name or launch). nil is false.
func CanReadWorkflow(ctx context.Context, userID string, workflow *core.WorkflowDraft) bool {
	return decide(ctx, userID, workflowResource(workflow))
}

// ReadWorkflowAccess is ReadChatAccess for a saved workflow: the owner, or a
// viewer of one its owner shared with its org (only on a WithViewerReads
// context). A viewer reads its definition, read-only; it never runs it.
func ReadWorkflowAccess(ctx context.Context, userID string, workflow *core.WorkflowDraft) (Access, error) {
	return readAccess(ctx, userID, workflowResource(workflow))
}

// SharedWorkflowDraftsFor is SharedChatsFor for saved workflows.
func SharedWorkflowDraftsFor(ctx context.Context, userID string, workflows []*core.WorkflowDraft) []*core.WorkflowDraft {
	return Visible(ctx, userID, workflows, func(ctx context.Context, userID string, w *core.WorkflowDraft) bool {
		return isViewer(ctx, userID, workflowResource(w))
	})
}

// WorkflowVisibility is who besides its owner may read a saved workflow,
// for display.
func WorkflowVisibility(workflow *core.WorkflowDraft) core.Visibility {
	return workflowResource(workflow).visibility
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

// OrgMemberDenial is why userID may not use something orgID owns outright,
// with no member as its owner: an org connection (control-plane
// docs/design/org-sharing.md §7.3). nil when they are a live member of orgID.
//
// It is stricter than the owner rule in one way: a Membership with no answer
// (IsMember's ok=false: a deployment without orgs) is a refusal, because an
// org-owned thing has no owner to fall back to. Otherwise it denies as deny
// does: ErrDenied for an empty id or a credential bound to another org,
// ErrNotOrgMember for a non-member, ErrMembershipUnavailable for an outage or
// a context with no Membership (logged at ERROR: a path never wired).
func OrgMemberDenial(ctx context.Context, userID, orgID string) error {
	if userID == "" || orgID == "" {
		return ErrDenied
	}
	if bound, ok := boundOrg(ctx); ok && bound != orgID {
		logging.InfoContext(ctx, "[authz] a credential bound to another org; denying an org resource", "user_id", userID, "credential_org", bound, "org_id", orgID)
		recordCredentialRefusal(ctx, orgID)
		return ErrDenied
	}
	m := membershipOf(ctx)
	if m == nil {
		logging.ErrorContext(ctx, "[authz] no membership on the context; denying an org resource (a path was never wired with authz.WithMembership)",
			"user_id", userID, "org_id", orgID)
		return ErrMembershipUnavailable
	}
	member, ok, err := m.IsMember(ctx, userID, orgID)
	if err != nil {
		logging.WarnContext(ctx, "[authz] could not check org membership; denying", "user_id", userID, "org_id", orgID, "error", err)
		return ErrMembershipUnavailable
	}
	if !ok || !member {
		return ErrNotOrgMember
	}
	return nil
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

// orgRoles is a Membership that also names a member's role in an org, asked
// of control-plane now rather than from a cache. *orgs.Resolver is one. role
// is "owner", "admin" or "member" for a live member and "" otherwise; ok is
// false when the role is unknown (no orgs, or a control plane that predates
// the role), and err is an outage.
type orgRoles interface {
	RoleFresh(ctx context.Context, userID, orgID string) (role string, ok bool, err error)
}

// ErrNotOrgAdmin is the denial of an admin-only action to a caller who is not
// an owner or admin of the org: a member, a non-member, or a caller whose role
// control-plane could not name (an older control plane answers no role, and
// an unknown role is never guessed upward).
var ErrNotOrgAdmin = errors.New("authz: not an owner or admin of the organization")

// RequireOrgAdmin decides an action only an org's administrators may take
// (clearing a member's share, creating an org connection, adopting a departed
// member's automation; control-plane docs/design/org-sharing.md §3.4): nil when
// userID is an owner or admin of orgID right now.
//
// The role is asked fresh (RoleFresh), never from the membership cache, so a
// just-demoted admin cannot act on a stale answer. Only a person may
// administer (ErrPersonRequired for any other credential, WithCredentialKind).
// It fails closed: a credential bound to another org is ErrDenied, a context with no Membership
// or one that cannot name roles and an outage are ErrMembershipUnavailable,
// and anything but owner or admin (unknown included) is ErrNotOrgAdmin.
func RequireOrgAdmin(ctx context.Context, userID, orgID string) error {
	if userID == "" || orgID == "" {
		return ErrDenied
	}
	// Administering an org is a person's act (org-sharing.md §3.4): an
	// agent's shell holds its admin's CLI login, and must not wield it.
	if !personCredential(ctx) {
		return ErrPersonRequired
	}
	return RequireOrgAdminRole(ctx, userID, orgID)
}

// RequireOrgAdminRole is RequireOrgAdmin without its person check: nil when
// userID is an owner or admin of orgID right now, asked fresh, whatever
// credential the request came with. It is for the few admin actions whose
// caller decides for itself which credentials may administer (the pipeline's
// enrolment admits a personal access token, internal/handlers/pipeline);
// every other admin action is RequireOrgAdmin's. It fails closed as
// RequireOrgAdmin does.
func RequireOrgAdminRole(ctx context.Context, userID, orgID string) error {
	if userID == "" || orgID == "" {
		return ErrDenied
	}
	if bound, ok := boundOrg(ctx); ok && bound != orgID {
		logging.InfoContext(ctx, "[authz] a credential bound to another org; denying an admin action", "user_id", userID, "credential_org", bound, "org_id", orgID)
		recordCredentialRefusal(ctx, orgID)
		return ErrDenied
	}
	m := membershipOf(ctx)
	if m == nil {
		logging.ErrorContext(ctx, "[authz] no membership on the context; denying an admin action (a path was never wired with authz.WithMembership)",
			"user_id", userID, "org_id", orgID)
		return ErrMembershipUnavailable
	}
	roles, ok := m.(orgRoles)
	if !ok {
		logging.ErrorContext(ctx, "[authz] the membership on the context cannot name roles; denying an admin action",
			"user_id", userID, "org_id", orgID)
		return ErrMembershipUnavailable
	}
	role, known, err := roles.RoleFresh(ctx, userID, orgID)
	if err != nil {
		logging.WarnContext(ctx, "[authz] could not read the org role; denying an admin action", "user_id", userID, "org_id", orgID, "error", err)
		return ErrMembershipUnavailable
	}
	if known && (role == "owner" || role == "admin") {
		return nil
	}
	if !known {
		logging.InfoContext(ctx, "[authz] the org role is unknown (an older control plane?); denying an admin action", "user_id", userID, "org_id", orgID)
	}
	return ErrNotOrgAdmin
}

// orgScopes is a Membership that also names the org-administration scopes a
// member is granted org-wide, asked of control-plane now rather than from a
// cache. *orgs.Resolver is one. member is false for a non-member and when the
// deployment has no orgs; err is an outage.
type orgScopes interface {
	ScopesFresh(ctx context.Context, userID, orgID string) (scopes []string, member bool, err error)
}

// ErrNotOrgMemberRead is the denial of an org read to a caller who is not a
// live member of the org (another org's admin included).
var ErrNotOrgMemberRead = errors.New("authz: not a member of the organization")

// ErrMissingOrgScope is the denial of an org read to a member who does not
// hold the org-administration scope it needs (billing:read, ...).
var ErrMissingOrgScope = errors.New("authz: the member does not hold the organization grant this needs")

// RequireOrgScope decides a read an org gates on an explicit grant
// (billing:read for the org's usage, control-plane
// docs/design/org-provider-accounts.md §5): nil when userID is a live member
// of orgID holding scope there right now.
//
// The grant is asked fresh (ScopesFresh), never from the membership cache,
// so a revoked grant stops the next read. It fails closed: a credential bound
// to another org is ErrDenied, a non-member ErrNotOrgMemberRead, a member without
// the grant (or of a control plane that cannot name grants) ErrMissingOrgScope,
// and a context with no Membership, or one that cannot name scopes, and an
// outage are ErrMembershipUnavailable. Which credentials may ask at all is the
// caller's to decide (this package cannot see a machine token's own scopes).
func RequireOrgScope(ctx context.Context, userID, orgID, scope string) error {
	if userID == "" || orgID == "" || scope == "" {
		return ErrDenied
	}
	if bound, ok := boundOrg(ctx); ok && bound != orgID {
		logging.InfoContext(ctx, "[authz] a credential bound to another org; denying an org read", "user_id", userID, "credential_org", bound, "org_id", orgID)
		recordCredentialRefusal(ctx, orgID)
		return ErrDenied
	}
	m := membershipOf(ctx)
	if m == nil {
		logging.ErrorContext(ctx, "[authz] no membership on the context; denying an org read (a path was never wired with authz.WithMembership)",
			"user_id", userID, "org_id", orgID)
		return ErrMembershipUnavailable
	}
	sc, ok := m.(orgScopes)
	if !ok {
		logging.ErrorContext(ctx, "[authz] the membership on the context cannot name grants; denying an org read",
			"user_id", userID, "org_id", orgID)
		return ErrMembershipUnavailable
	}
	held, member, err := sc.ScopesFresh(ctx, userID, orgID)
	if err != nil {
		logging.WarnContext(ctx, "[authz] could not read the org grants; denying an org read", "user_id", userID, "org_id", orgID, "error", err)
		return ErrMembershipUnavailable
	}
	if !member {
		return ErrNotOrgMemberRead
	}
	for _, s := range held {
		if s == scope {
			return nil
		}
	}
	return ErrMissingOrgScope
}

// Refusals of a visibility change (ChatShareChange, ProjectShareChange,
// WorkflowShareChange)
// beyond ErrDenied (the call site's NotFound), ErrNotOrgAdmin and
// ErrMembershipUnavailable.
var (
	// ErrPersonRequired: sharing, and every admin action (RequireOrgAdmin),
	// are a person's acts, never an agent's (the credential is software's).
	ErrPersonRequired = errors.New("authz: only a person signed in to the app may do this")
	// ErrShareNeedsOrg: a resource that belongs to no organization has no
	// one to be shared with.
	ErrShareNeedsOrg = errors.New("authz: the resource belongs to no organization")
	// ErrOnlyOwnerShares: a viewer (an admin included) asked to share what
	// is not theirs. Disclosure is the owner's act alone.
	ErrOnlyOwnerShares = errors.New("authz: only the owner can share it")
	// ErrInvalidVisibility: a visibility this build does not know.
	ErrInvalidVisibility = errors.New("authz: unknown visibility")
)

// ChatShareChange decides userID setting chat's visibility to to
// (control-plane docs/design/org-sharing.md §2): the change to apply, with its
// action, or why it is refused.
//
//   - The owner (CanOwnChat) may make it private with any credential, and
//     may share it only as a person and only when it belongs to an org.
//   - Anyone who may not read it is ErrDenied (NotFound: it does not exist for
//     them).
//   - A viewer may never share it (ErrOnlyOwnerShares); a viewer who is an
//     admin or owner of its org, asked fresh and acting as a person, may make
//     it private (ShareActionAdminUnshare). Any other viewer is ErrNotOrgAdmin.
//
// The returned change carries the row's owner and org, so applying it after
// either moved writes nothing (store.ApplyShareChange). Whether it changes
// anything (already shared, already private) is the store's to say.
func ChatShareChange[C chatRecord](ctx context.Context, userID string, chat C, id string, to core.Visibility) (core.ShareChange, error) {
	return shareChange(ctx, userID, chatResource(chat), core.ShareKindChat, id, to)
}

// ProjectShareChange is ChatShareChange for a project.
func ProjectShareChange(ctx context.Context, userID string, project *core.Project, to core.Visibility) (core.ShareChange, error) {
	id := ""
	if project != nil {
		id = project.ID
	}
	return shareChange(ctx, userID, projectResource(project), core.ShareKindProject, id, to)
}

// WorkflowShareChange is ChatShareChange for a saved workflow
// (org-sharing.md §8.5).
func WorkflowShareChange(ctx context.Context, userID string, workflow *core.WorkflowDraft, to core.Visibility) (core.ShareChange, error) {
	id := ""
	if workflow != nil {
		id = workflow.ID
	}
	return shareChange(ctx, userID, workflowResource(workflow), core.ShareKindWorkflow, id, to)
}

func shareChange(ctx context.Context, userID string, r resource, kind core.ShareKind, id string, to core.Visibility) (core.ShareChange, error) {
	if !to.Valid() {
		return core.ShareChange{}, ErrInvalidVisibility
	}
	c := core.ShareChange{Kind: kind, ResourceID: id, OrgID: r.orgID, OwnerUserID: r.ownerUserID, ActorUserID: userID}
	if err := deny(ctx, userID, r); err == nil {
		if to == core.VisibilityPrivate {
			c.Action = core.ShareActionUnshare
			return c, nil
		}
		if !personCredential(ctx) {
			return core.ShareChange{}, ErrPersonRequired
		}
		if r.orgID == "" {
			return core.ShareChange{}, ErrShareNeedsOrg
		}
		c.Action = core.ShareActionShare
		return c, nil
	}
	if r.ownerUserID == userID || !isViewer(ctx, userID, r) {
		// The owner who may not write it (removed from its org) and every
		// non-reader get what they would get for any other change.
		return core.ShareChange{}, ErrDenied
	}
	if to == core.VisibilityOrg {
		return core.ShareChange{}, ErrOnlyOwnerShares
	}
	if !personCredential(ctx) {
		return core.ShareChange{}, ErrPersonRequired
	}
	if err := RequireOrgAdmin(ctx, userID, r.orgID); err != nil {
		return core.ShareChange{}, err
	}
	c.Action = core.ShareActionAdminUnshare
	return c, nil
}
