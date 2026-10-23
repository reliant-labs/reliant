package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
)

// Options configure a Reconciler.
type Options struct {
	// BaseURL is the address this process DIALS. Required.
	BaseURL string
	// Host overrides the HTTP Host header. Zitadel resolves which instance a
	// request belongs to from the Host, so when the address you dial (a
	// Service DNS name, a compose service) differs from the domain the
	// instance is registered under, set Host to the latter.
	Host string
	// Issuer pins the issuer string the published outputs carry and the JWT
	// audience for machine-key exchange. Empty reads it from the instance's
	// discovery document, which is right unless discovery is not served at the
	// dialled address.
	Issuer string
	// Credential authenticates the library; it must hold IAM_OWNER. Required.
	Credential Credential
	// OrgID scopes the content to one organization. Empty resolves it: OrgName
	// if set, else the credential's own organization.
	OrgID   string
	OrgName string

	// Keys remembers generated credentials. Required when the Spec generates
	// any (machine keys, tokens, web client secrets, an SMTP password).
	Keys KeyStore
	// Outputs receives the Output at the end of every successful pass.
	Outputs Outputs

	Rotation Rotation

	// Adopt converges an instance that already holds content: the project and
	// every application must already exist and are never created. Objects are
	// matched by natural key (or by a pinned id) and kept whatever their
	// internal ids.
	Adopt bool
	// DryRun computes the Report without writing anything.
	DryRun bool
	// AllowSeedUsers permits Spec.SeedUsers. Local and ephemeral instances only.
	AllowSeedUsers bool
	// SignInCodes mints a one-time password-reset code, returned in the Report,
	// for every declared member who has no credential. Use it where nothing can
	// deliver mail.
	SignInCodes bool
	// HeldSignInCode, when set, reports whether the caller still holds an
	// unexpired code it delivered for a member. Such a member is not minted
	// another: minting invalidates the code they hold, so a converge loop that
	// minted on every pass would change the code under someone about to use it,
	// and no pass would ever be quiet. Nil: every credential-less member gets a
	// fresh code on every pass. Consulted only with SignInCodes.
	HeldSignInCode func(email string) bool
	// KeepSuperseded disables deleting credentials that a newer one has
	// replaced, once the overlap has passed. They are then only reported.
	KeepSuperseded bool

	// ReadyTimeout bounds the wait for the instance to answer its readiness
	// probe (default 2m). RequestTimeout bounds one API call (default 15s) and
	// MaxAttempts the tries one call gets (default 4).
	ReadyTimeout   time.Duration
	RequestTimeout time.Duration
	MaxAttempts    int

	HTTPClient *http.Client
	Logger     *slog.Logger
	// Now is the clock credential lifetimes are judged against.
	Now func() time.Time
}

// Reconciler converges one instance. It is safe to reuse across passes but not
// for concurrent passes against the same instance.
type Reconciler struct {
	opts Options
	c    *client
}

// New builds a Reconciler. It does not contact the instance.
func New(opts Options) (*Reconciler, error) {
	if strings.TrimSpace(opts.BaseURL) == "" {
		return nil, errors.New("identity: Options.BaseURL is required")
	}
	if opts.Credential == nil {
		return nil, errors.New("identity: Options.Credential is required")
	}
	if opts.HTTPClient == nil {
		// No client timeout: RequestTimeout bounds each call.
		opts.HTTPClient = observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: -1})
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = defaultRequestTimeout
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = defaultMaxAttempts
	}
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = 2 * time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	opts.Rotation = opts.Rotation.withDefaults()
	return &Reconciler{opts: opts, c: &client{
		base:        opts.BaseURL,
		host:        opts.Host,
		issuer:      strings.TrimRight(opts.Issuer, "/"),
		http:        opts.HTTPClient,
		cred:        opts.Credential,
		orgID:       opts.OrgID,
		maxAttempts: opts.MaxAttempts,
		timeout:     opts.RequestTimeout,
		log:         opts.Logger,
		now:         opts.Now,
		sleep:       sleepCtx,
	}}, nil
}

// Action is what a pass did (or, in a dry run, would do) to an object.
type Action string

const (
	Created Action = "created"
	Updated Action = "updated"
	Granted Action = "granted"
	Revoked Action = "revoked"
	Issued  Action = "issued"
	Pruned  Action = "pruned"
)

// Change is one write.
type Change struct {
	Action Action
	Kind   string
	Name   string
	Detail string
}

func (c Change) String() string {
	s := fmt.Sprintf("%s %s %q", c.Action, c.Kind, c.Name)
	if c.Detail != "" {
		s += " (" + c.Detail + ")"
	}
	return s
}

// Retiring is a superseded credential that still works until DeleteAfter.
type Retiring struct {
	Username    string
	Kind        string // "machine key" or "token"
	ID          string
	DeleteAfter time.Time
}

// SignInCodeLifetime is how long a minted sign-in code stays redeemable:
// Zitadel's default PasswordVerificationCode expiry (cmd/defaults.yaml,
// SecretGenerators.PasswordVerificationCode.Expiry). An instance configured
// otherwise makes SignInCode.ExpiresAt an estimate.
const SignInCodeLifetime = time.Hour

// SignInCode is a one-time code that lets a credential-less member set a first
// password. It is single-use and short-lived; treat it as a secret.
type SignInCode struct {
	Email string
	Code  string
	// ExpiresAt is Options.Now at minting plus SignInCodeLifetime.
	ExpiresAt time.Time
}

// Report is what a pass did.
type Report struct {
	// Changes lists every write. A converged instance yields none.
	Changes []Change
	// Warnings are things left alone on purpose: a role nobody declared, a
	// credential that cannot be recovered.
	Warnings    []string
	Retiring    []Retiring
	SignInCodes []SignInCode
	// Credentialed lists the declared members this pass CONFIRMED hold a
	// credential (filled only with Options.SignInCodes): any code a caller
	// holds for them is spent or moot. A member whose check failed is in
	// neither this list nor SignInCodes.
	Credentialed []string
	// Output is the published view, filled even when Options.Outputs is nil.
	Output Output
	DryRun bool
}

// Changed reports whether the pass wrote (or, in a dry run, would write)
// anything.
func (r *Report) Changed() bool { return len(r.Changes) > 0 }

func (r *Report) change(a Action, kind, name, detail string) {
	r.Changes = append(r.Changes, Change{Action: a, Kind: kind, Name: name, Detail: detail})
}

func (r *Report) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// run is the state of one pass.
type run struct {
	r   *Reconciler
	c   *client
	o   Options
	rep *Report

	spec      Spec
	force     []RotateTarget
	projCur   *projectObj
	projectID string
	roles     map[string]Role
	// users maps a machine username or a lowercased human email to its user id.
	users   map[string]string
	apps0   []appObj // applications present when the pass began
	machine map[string]MachineOutput
	apps    map[string]AppOutput
}

func (p *run) dry() bool { return p.o.DryRun }

// write reports whether a write may be performed (it may not in a dry run).
func (p *run) write() bool { return !p.o.DryRun }

func (p *run) logf(msg string, args ...any) { p.c.log.Info("identity: "+msg, args...) }

// requiresKeys reports whether the spec generates credentials that must be
// remembered.
func (s Spec) requiresKeys() bool {
	for _, a := range s.Applications {
		if a.Kind == AppNativeBroker || a.Kind == AppWeb {
			return true
		}
	}
	for _, m := range s.MachineUsers {
		if m.Key || m.Token {
			return true
		}
	}
	return s.SMTP != nil && s.SMTP.Password != ""
}

// Reconcile makes the instance match spec and reports what it changed.
//
// Steps run in dependency order. The project is foundational and its failure
// ends the pass; every other step records its error and the pass continues, so
// one broken object does not hide the state of the rest. All errors are joined
// and each names its object (see ObjectError). The Output is published only
// when nothing failed.
func (r *Reconciler) Reconcile(ctx context.Context, spec Spec) (*Report, error) {
	return r.reconcile(ctx, spec, nil)
}

func (r *Reconciler) reconcile(ctx context.Context, spec Spec, force []RotateTarget) (*Report, error) {
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("identity: invalid spec: %w", err)
	}
	if len(spec.SeedUsers) > 0 && !r.opts.AllowSeedUsers {
		return nil, errors.New("identity: the spec declares seed users but Options.AllowSeedUsers is not set; seed users are for local and ephemeral instances only")
	}
	if spec.requiresKeys() && r.opts.Keys == nil {
		return nil, errors.New("identity: the spec generates credentials (machine keys, tokens, client secrets or an SMTP password) but Options.Keys is nil; there would be nowhere to keep them")
	}

	rep := &Report{DryRun: r.opts.DryRun}
	p := &run{
		r: r, c: r.c, o: r.opts, rep: rep, spec: spec, force: force,
		roles: map[string]Role{}, users: map[string]string{},
		machine: map[string]MachineOutput{}, apps: map[string]AppOutput{},
	}
	for _, t := range force {
		if t.Application == "" && t.MachineUser == "" {
			return nil, errors.New("identity: a RotateTarget must name an Application or a MachineUser")
		}
	}
	for _, role := range spec.Roles {
		p.roles[role.Key] = role
	}

	readyCtx, cancel := context.WithTimeout(ctx, r.opts.ReadyTimeout)
	err := r.c.waitReady(readyCtx)
	cancel()
	if err != nil {
		return rep, err
	}
	if err := p.resolveOrg(ctx); err != nil {
		return rep, err
	}
	// Everything that can refuse the pass reads first; the first write comes
	// only after the whole declaration is known to be satisfiable.
	if err := p.locateProject(ctx); err != nil {
		return rep, err
	}
	if err := p.preflightAdoption(ctx); err != nil {
		return rep, err
	}
	if err := p.applyProject(ctx); err != nil {
		return rep, err
	}

	var errs []error
	step := func(f func(context.Context) error) {
		if err := f(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	step(p.ensureRoles)
	step(p.ensurePolicies)
	step(p.ensureSMTP)
	// Machine users precede applications: a broker account is a machine user.
	step(p.ensureMachineUsers)
	step(p.ensureApplications)
	step(p.ensureMembers)
	step(p.ensureSeedUsers)

	if len(errs) > 0 {
		return rep, errors.Join(errs...)
	}
	if err := p.buildOutput(ctx); err != nil {
		return rep, err
	}
	if r.opts.Outputs != nil && p.write() {
		if err := r.opts.Outputs.Publish(ctx, rep.Output); err != nil {
			return rep, objErr("outputs", "publish", err)
		}
	}
	return rep, nil
}

// resolveOrg fixes the organization every later call is scoped to.
//
// A system-created instance owner has no organization of its own to default
// to, and a WRITE issued without x-zitadel-orgid fails as "Resource Owner
// Organisation missing" rather than anything about credentials, so this
// resolves first and doubles as the credential's first real test.
func (p *run) resolveOrg(ctx context.Context) error {
	if p.c.orgID != "" {
		return nil
	}
	if p.o.OrgName != "" {
		orgs, err := pageAll[struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}](ctx, p.c, "/v2/organizations/_search", map[string]any{
			"queries": []any{map[string]any{"nameQuery": map[string]any{"name": p.o.OrgName, "method": "TEXT_QUERY_METHOD_EQUALS"}}},
		}, noOrg)
		if err != nil {
			return objErr("organization", p.o.OrgName, err)
		}
		for _, o := range orgs {
			if o.Name == p.o.OrgName {
				p.c.orgID = o.ID
				return nil
			}
		}
		return objErr("organization", p.o.OrgName, errors.New("not found on the instance"))
	}
	var me struct {
		Org struct {
			ID string `json:"id"`
		} `json:"org"`
	}
	err := p.c.call(ctx, http.MethodGet, "/management/v1/orgs/me", nil, &me, noOrg)
	if err == nil && me.Org.ID != "" {
		p.c.orgID = me.Org.ID
		return nil
	}
	if ae := asAPI(err); ae != nil && (ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden) {
		return objErr("credential", p.c.base, fmt.Errorf("the instance rejected it (is it an IAM_OWNER credential for this instance?): %w", err))
	}
	// A credential with no organization of its own cannot answer "me". If the
	// instance has exactly one organization there is nothing to choose between.
	orgs, serr := pageAll[struct {
		ID string `json:"id"`
	}](ctx, p.c, "/v2/organizations/_search", map[string]any{}, noOrg)
	if serr != nil {
		return objErr("organization", "current", errors.Join(err, serr))
	}
	switch len(orgs) {
	case 1:
		p.c.orgID = orgs[0].ID
		return nil
	case 0:
		return objErr("organization", "current", errors.New("the instance reports no organizations; its first-instance setup did not complete"))
	}
	return objErr("organization", "current", fmt.Errorf("the instance has %d organizations; set Options.OrgID or Options.OrgName", len(orgs)))
}
