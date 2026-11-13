package release

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// ROLLBACK FLOORS — "never bind this environment to anything older than X".
//
// A floor is a constraint on ONE environment: from the moment it is set, a
// release that does not satisfy it can never become that environment's
// binding. "Roll forward only from here" is a floor at the release the
// environment runs now. An environment can hold several floors at once, and a
// target must satisfy every active one ([AdmitOverFloors]).
//
// There are two kinds, because the two real hazards need different rules:
//
//   - ORDER ([FloorOrder]): the target must not have been cut before the
//     floor's release. A person's statement about time — "v1.8 corrupts
//     invoices; never again" — set by hand (`forge env floor set`) or at a
//     promote (`forge env deploy --floor`).
//   - DECLARED ([FloorDeclared]): the target must carry every declaration the
//     floor requires. Raised AUTOMATICALLY when an environment is promoted to
//     a release that carries a declaration ([FloorDeclaration]) no active
//     floor already requires: a `forge:breaks-rollback` migration, or a
//     checked-in declaration file (deploy/rollback-floors/<id>.yaml) of kind
//     api, feature or data.
//
// WHY CONTAINMENT, NOT ORDER, FOR DECLARATIONS. Order loses both ways. prod
// goes v1 → v3 and v2 introduced breaking migration M: an order floor at v3
// refuses v2, which carries M and runs fine (over-refusal). And a hotfix
// v1.9.1 cut AFTER v3 from a branch that predates M is "newer" by cut time,
// so an order floor admits it, and its pods meet a schema they cannot read
// (under-refusal — the dangerous direction). "Does the target carry M?" is
// the hazard itself, so that is the rule.
//
// WHY CUT ORDER, NOT SEMVER OR LEDGER POSITION, FOR ORDER. Version labels do
// not share one scheme (rc suffixes, forge's timestamp versions), and a
// release never promoted to this environment has no ledger position. Cut time
// is stamped once per release and never changes; (CreatedAt, Version) is a
// total order within a project. A client with git may additionally refuse a
// target whose commit is an ancestor of the floor's ([Floor.Commit]); the
// server, which has no git, enforces cut order alone.
//
// THE INVARIANT. An environment's current binding always satisfies every
// active floor: the promote checks it, a new floor its environment's current
// release does not satisfy is refused ([CheckNewFloor]), a declared floor is
// raised FROM the promoted release (which carries what it requires by
// definition), and a lift only relaxes. So where the check sits relative to
// [Decide]'s idempotent no-op does not change any answer.
//
// THE ESCAPE IS A LIFT, NEVER A FORCE FLAG. A floor is a recorded decision; an
// override flag on deploy would be a second, cheaper way to undo it, and the
// cheapest path is the one a pipeline wires in. A lift is its own audited act
// with a reason, and a lift of a declared floor names every declaration it
// vouches for ([CheckLift]).
//
// One implementation, every backend: the control plane calls these under its
// environment row lock, forge's machine ledger under its file lock, and
// forge's deploy plan to refuse before anything is built or written.

// FloorKind is what a floor constrains.
type FloorKind string

const (
	// FloorOrder requires the target to be cut no earlier than the floor's
	// release.
	FloorOrder FloorKind = "order"
	// FloorDeclared requires the target to carry every declaration in
	// [Floor.Requires].
	FloorDeclared FloorKind = "declared"
)

// Valid reports whether k is a floor kind.
func (k FloorKind) Valid() bool { return k == FloorOrder || k == FloorDeclared }

// FloorSource is what set a floor.
type FloorSource string

const (
	// FloorSourceManual is an order floor a person set at a named release
	// (`forge env floor set <env> <release>`, `floor lower`).
	FloorSourceManual FloorSource = "manual"
	// FloorSourceRollForwardOnly is an order floor at the release the
	// environment ran when it was set (`floor set --current`) or at the
	// release a promote bound (`env deploy --floor`).
	FloorSourceRollForwardOnly FloorSource = "roll_forward_only"
	// FloorSourceDeclared is a declared floor, raised by a promote of a
	// release that carried declarations.
	FloorSourceDeclared FloorSource = "declared"
)

// Valid reports whether s is a floor source.
func (s FloorSource) Valid() bool {
	switch s {
	case FloorSourceManual, FloorSourceRollForwardOnly, FloorSourceDeclared:
		return true
	}
	return false
}

// kindOf is the one kind each source makes: a declared source makes a
// declared floor, every other source an order floor.
func (s FloorSource) kindOf() FloorKind {
	if s == FloorSourceDeclared {
		return FloorDeclared
	}
	return FloorOrder
}

// DeclarationKind is what a floor declaration says changed.
type DeclarationKind string

const (
	// DeclarationSchema is a migration that declares
	// `-- forge:breaks-rollback`: the previous release cannot run on the
	// schema it leaves. Its ID is the migration file name and its Source
	// the migrations directory ([MigrationSet.Source]).
	DeclarationSchema DeclarationKind = "schema"
	// DeclarationAPI is an API change older releases' callers or servers
	// cannot live with.
	DeclarationAPI DeclarationKind = "api"
	// DeclarationFeature is a feature whose state older releases mishandle.
	DeclarationFeature DeclarationKind = "feature"
	// DeclarationData is a data rewrite or backfill older releases cannot
	// read.
	DeclarationData DeclarationKind = "data"
)

// Valid reports whether k is a declaration kind.
func (k DeclarationKind) Valid() bool {
	switch k {
	case DeclarationSchema, DeclarationAPI, DeclarationFeature, DeclarationData:
		return true
	}
	return false
}

// FloorDeclarationsDir is where a project declares its non-migration floor
// declarations, relative to the project root: one `<id>.yaml` per
// declaration. Outside `.forge/`, which forge projects gitignore: a
// declaration git silently ignored would be captured by the one cut that ran
// on the author's machine and missing from every later one, which would
// refuse every later release. Exported so the cut, the linter and the docs
// name the same path.
const FloorDeclarationsDir = "deploy/rollback-floors"

const (
	// MaxFloorDeclarations bounds the declarations one release records.
	MaxFloorDeclarations = 1000
	// MaxFloorReasonBytes bounds one reason (a declaration's, a floor's, a
	// lift's).
	MaxFloorReasonBytes = 1024
	// MaxFloorDeclarationIDBytes bounds a declaration file's id.
	MaxFloorDeclarationIDBytes = 128
)

// declarationIDRe is a declaration file's id: its base name without
// `.yaml`. Lower-case so two checkouts on case-insensitive and
// case-sensitive filesystems agree on one id.
var declarationIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// FloorDeclaration is one thing a release says no older release can survive.
// (Source, ID) is its identity across releases: a later release that still
// carries it satisfies a floor that requires it, a hotfix cut from before it
// existed does not.
type FloorDeclaration struct {
	// Source is the directory the declaration lives in, in the
	// "<canonical repo>/<dir>" form [MigrationSet.Source] uses: the
	// migrations directory for a schema declaration, the
	// [FloorDeclarationsDir] of that repository for every other kind.
	Source string `json:"source"`
	// ID is the migration file name (schema) or the declaration file's id.
	ID string `json:"id"`
	// Kind is what the declaration says changed.
	Kind DeclarationKind `json:"kind"`
	// Reason is why, as the author wrote it. A schema declaration's reason
	// is the text after `forge:breaks-rollback`, and may be empty; every
	// other kind requires one.
	Reason string `json:"reason,omitempty"`
}

// Ref is the declaration's identity.
func (d FloorDeclaration) Ref() DeclarationRef { return DeclarationRef{Source: d.Source, ID: d.ID} }

// DeclarationRef names one declaration: what a lift vouches for.
type DeclarationRef struct {
	Source string `json:"source"`
	ID     string `json:"id"`
}

// String is "<source>:<id>", the spelling the CLI takes and prints.
func (r DeclarationRef) String() string { return r.Source + ":" + r.ID }

// ParseDeclarationRef reads "<source>:<id>". The id never contains ":", so
// the last one splits.
func ParseDeclarationRef(s string) (DeclarationRef, error) {
	i := strings.LastIndex(s, ":")
	if i <= 0 || i == len(s)-1 {
		return DeclarationRef{}, fmt.Errorf("%w: declaration %q is not <source>:<id>", ErrInvalid, s)
	}
	return DeclarationRef{Source: s[:i], ID: s[i+1:]}, nil
}

// floorDeclarationFile is the YAML a declaration file holds.
type floorDeclarationFile struct {
	Kind   string `yaml:"kind"`
	Reason string `yaml:"reason"`
}

// ParseFloorDeclarationFile reads one declaration file — name is its base
// name (`<id>.yaml`), data its bytes — into a declaration with no Source
// (the caller knows which repository it read). The file is two keys:
//
//	kind: api            # api | feature | data
//	reason: "v2 webhook payloads; v1 receivers were removed"
//
// schema is refused here: a schema declaration is a migration's
// `-- forge:breaks-rollback` line, where the reviewer of the migration sees
// it, and a second spelling of it could disagree with the migration.
func ParseFloorDeclarationFile(name string, data []byte) (FloorDeclaration, error) {
	id, ok := strings.CutSuffix(name, ".yaml")
	if !ok {
		return FloorDeclaration{}, fmt.Errorf("%w: %s: a rollback-floor declaration is a <id>.yaml file", ErrInvalid, name)
	}
	if err := validDeclarationID(id); err != nil {
		return FloorDeclaration{}, fmt.Errorf("%s: %w", name, err)
	}
	var f floorDeclarationFile
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return FloorDeclaration{}, fmt.Errorf("%w: %s: %v (want `kind:` and `reason:`)", ErrInvalid, name, err)
	}
	d := FloorDeclaration{ID: id, Kind: DeclarationKind(strings.TrimSpace(f.Kind)), Reason: strings.TrimSpace(f.Reason)}
	switch {
	case d.Kind == DeclarationSchema:
		return FloorDeclaration{}, fmt.Errorf("%w: %s: kind schema is declared by a migration's `-- forge:breaks-rollback <why>` line, not a file", ErrInvalid, name)
	case !d.Kind.Valid():
		return FloorDeclaration{}, fmt.Errorf("%w: %s: kind %q is not one of api, feature, data", ErrInvalid, name, f.Kind)
	}
	if err := validReason(d.Reason, true); err != nil {
		return FloorDeclaration{}, fmt.Errorf("%s: %w", name, err)
	}
	return d, nil
}

func validDeclarationID(id string) error {
	if len(id) > MaxFloorDeclarationIDBytes || !declarationIDRe.MatchString(id) {
		return fmt.Errorf("%w: declaration id %q must match %s and be at most %d bytes", ErrInvalid, id, declarationIDRe, MaxFloorDeclarationIDBytes)
	}
	return nil
}

func validReason(reason string, required bool) error {
	switch {
	case required && strings.TrimSpace(reason) == "":
		return fmt.Errorf("%w: a reason is required", ErrInvalid)
	case len(reason) > MaxFloorReasonBytes || !utf8.ValidString(reason):
		return fmt.Errorf("%w: reason is not valid UTF-8 of at most %d bytes", ErrInvalid, MaxFloorReasonBytes)
	}
	return nil
}

// validateDeclarations checks a manifest's declarations against its sets:
// bounds, kinds, and that a schema declaration names a file its set carries.
func (m MigrationManifest) validateDeclarations() error {
	if len(m.Declarations) > MaxFloorDeclarations {
		return fmt.Errorf("%w: %d rollback-floor declarations, over the cap of %d", ErrInvalid, len(m.Declarations), MaxFloorDeclarations)
	}
	files := m.byFiles()
	seen := make(map[DeclarationRef]bool, len(m.Declarations))
	for _, d := range m.Declarations {
		if strings.TrimSpace(d.Source) == "" || len(d.Source) > MaxMigrationSourceBytes || !utf8.ValidString(d.Source) {
			return fmt.Errorf("%w: rollback-floor declaration %q has no valid source", ErrInvalid, d.ID)
		}
		if seen[d.Ref()] {
			return fmt.Errorf("%w: rollback-floor declaration %s recorded twice", ErrInvalid, d.Ref())
		}
		seen[d.Ref()] = true
		switch {
		case d.Kind == DeclarationSchema:
			if !validMigrationFileName(d.ID) {
				return fmt.Errorf("%w: schema declaration %s does not name a forward migration file", ErrInvalid, d.Ref())
			}
			if !files[d.Source][d.ID] {
				return fmt.Errorf("%w: schema declaration %s names a migration the manifest does not carry", ErrInvalid, d.Ref())
			}
			if err := validReason(d.Reason, false); err != nil {
				return fmt.Errorf("declaration %s: %w", d.Ref(), err)
			}
		case d.Kind.Valid():
			if err := validDeclarationID(d.ID); err != nil {
				return err
			}
			if err := validReason(d.Reason, true); err != nil {
				return fmt.Errorf("declaration %s: %w", d.Ref(), err)
			}
		default:
			return fmt.Errorf("%w: declaration %s has unknown kind %q", ErrInvalid, d.Ref(), d.Kind)
		}
	}
	return nil
}

// validMigrationFileName is the manifest's file-name rule (Validate's), for a
// name that arrives as a schema declaration's id.
func validMigrationFileName(f string) bool {
	if len(f) > MaxMigrationFileBytes || strings.ContainsAny(f, "/\\") || !strings.HasSuffix(f, ".up.sql") || !utf8.ValidString(f) {
		return false
	}
	_, ok := MigrationVersion(f)
	return ok
}

// sortDeclarations orders declarations by (Source, ID).
func sortDeclarations(ds []FloorDeclaration) {
	sort.Slice(ds, func(i, j int) bool {
		if ds[i].Source != ds[j].Source {
			return ds[i].Source < ds[j].Source
		}
		return ds[i].ID < ds[j].ID
	})
}

// Floor is one constraint on one environment.
type Floor struct {
	// ID is assigned by the backend that recorded the floor.
	ID string `json:"id,omitempty"`
	// Env is the environment name.
	Env    string      `json:"env"`
	Kind   FloorKind   `json:"kind"`
	Source FloorSource `json:"source"`
	// Release is the version label the floor names: an order floor's bound,
	// and for a declared floor the release whose promote raised it.
	Release string `json:"release"`
	// ReleaseCutAt is Release's cut time ([Release.CreatedAt]): an order
	// floor's bound. Denormalized from the immutable release so the
	// predicate is a pure function of the floor and the target.
	ReleaseCutAt time.Time `json:"release_cut_at"`
	// Commit is Release's source commit, when recorded: what lets a client
	// with git also refuse a target whose commit is an ancestor of it. The
	// server never consults it.
	Commit string `json:"commit,omitempty"`
	// Requires is every declaration a declared floor requires; empty for an
	// order floor.
	Requires []FloorDeclaration `json:"requires,omitempty"`
	// Reason is why the floor exists. Required for every floor; a declared
	// floor's is its declarations' reasons, joined.
	Reason string `json:"reason"`
	// SetBy is the principal that set it (a user, or a token / automation).
	SetBy Actor     `json:"set_by,omitempty"`
	SetAt time.Time `json:"set_at"`
	// PromotionID is the promotion that raised the floor (a declared floor,
	// `env deploy --floor`, `floor set --current`); empty for a floor set at
	// a named release.
	PromotionID string `json:"promotion_id,omitempty"`
	// Lift is set once the floor was lifted; a lifted floor constrains
	// nothing.
	Lift *FloorLift `json:"lift,omitempty"`
}

// FloorLift records that a floor no longer applies, and why.
type FloorLift struct {
	Reason string    `json:"reason"`
	By     Actor     `json:"by,omitempty"`
	At     time.Time `json:"at"`
	// ReplacedBy is the floor that replaced this one, when the lift was a
	// `floor lower` (lift the floors above X, set one at X, as one act).
	ReplacedBy string `json:"replaced_by,omitempty"`
	// Acknowledged is every declaration the lifter vouched for: for a
	// declared floor, each of its Requires.
	Acknowledged []DeclarationRef `json:"acknowledged,omitempty"`
}

// Active reports whether the floor still constrains its environment.
func (f Floor) Active() bool { return f.Lift == nil }

// ActiveFloors returns the floors that are not lifted, in their order.
func ActiveFloors(floors []Floor) []Floor {
	var out []Floor
	for _, f := range floors {
		if f.Active() {
			out = append(out, f)
		}
	}
	return out
}

// Validate checks a floor's shape: the kind its source makes, a reason, and
// requirements exactly when declared.
func (f Floor) Validate() error {
	switch {
	case !f.Kind.Valid():
		return fmt.Errorf("%w: floor kind %q is not order or declared", ErrInvalid, f.Kind)
	case !f.Source.Valid():
		return fmt.Errorf("%w: floor source %q is not manual, roll_forward_only or declared", ErrInvalid, f.Source)
	case f.Source.kindOf() != f.Kind:
		return fmt.Errorf("%w: a %s floor is kind %s, not %s", ErrInvalid, f.Source, f.Source.kindOf(), f.Kind)
	case strings.TrimSpace(f.Env) == "":
		return fmt.Errorf("%w: floor names no environment", ErrInvalid)
	case strings.TrimSpace(f.Release) == "":
		return fmt.Errorf("%w: floor names no release", ErrInvalid)
	case f.Kind == FloorDeclared && len(f.Requires) == 0:
		return fmt.Errorf("%w: a declared floor requires at least one declaration", ErrInvalid)
	case f.Kind == FloorOrder && len(f.Requires) > 0:
		return fmt.Errorf("%w: an order floor requires no declarations", ErrInvalid)
	case f.Kind == FloorOrder && f.ReleaseCutAt.IsZero():
		return fmt.Errorf("%w: an order floor needs its release's cut time", ErrInvalid)
	}
	if err := validReason(f.Reason, true); err != nil {
		return fmt.Errorf("floor: %w", err)
	}
	return nil
}

// cutKey is a release's position in cut order, at the microsecond precision
// every backend can store (Postgres timestamptz is microseconds), so a floor
// read back from a database orders exactly as the release it was set from.
func cutKey(at time.Time) time.Time { return at.UTC().Truncate(time.Microsecond) }

// CutBefore reports whether release a was cut before release b: by cut time,
// then by version label, so two releases cut in the same microsecond still
// have one order.
func CutBefore(a, b Release) bool {
	return cutBefore(a.CreatedAt, a.Version, b.CreatedAt, b.Version)
}

func cutBefore(aAt time.Time, aVersion string, bAt time.Time, bVersion string) bool {
	ak, bk := cutKey(aAt), cutKey(bAt)
	if !ak.Equal(bk) {
		return ak.Before(bk)
	}
	return aVersion < bVersion
}

// Carries reports whether the target release carries declaration d: for a
// schema declaration, whether its migrations directory holds the file (or a
// baseline that adopted the file's version from forge's own history, which
// `forge db squash` writes); for every other kind, whether it recorded the
// declaration.
func Carries(target Release, d DeclarationRef, kind DeclarationKind) bool {
	m := target.Migrations
	if m == nil {
		return false
	}
	if kind == DeclarationSchema {
		v, hasVersion := MigrationVersion(d.ID)
		for _, s := range m.Sets {
			if s.Source != d.Source {
				continue
			}
			for _, f := range s.Files {
				if f == d.ID {
					return true
				}
			}
			if hasVersion {
				for _, a := range s.Adopts {
					if n, err := strconv.ParseUint(a, 10, 64); err == nil && n == v {
						return true
					}
				}
			}
		}
		return false
	}
	for _, have := range m.Declarations {
		if have.Source == d.Source && have.ID == d.ID {
			return true
		}
	}
	return false
}

// Admits reports whether the floor admits target, and when it does not, one
// sentence saying why (with no environment name; the caller has it). A
// lifted floor admits everything.
func (f Floor) Admits(target Release) (bool, string) {
	if !f.Active() {
		return true, ""
	}
	switch f.Kind {
	case FloorOrder:
		if cutBefore(target.CreatedAt, target.Version, f.ReleaseCutAt, f.Release) {
			return false, fmt.Sprintf("%s was cut before the floor's release %s (%s floor: %q)", target.Version, f.Release, f.Source, f.Reason)
		}
		return true, ""
	case FloorDeclared:
		// UNKNOWN IS REFUSED HERE, deliberately, and it is the opposite of
		// the rule for raising (DeclarationsRaisedBy raises nothing from a
		// manifest that recorded no declarations). A floor is admitted only
		// on proof the target carries what it requires (owner, 2026-11-08:
		// "a target release must carry that declaration file to be
		// admitted"). A schema declaration is proved by the migration
		// FILE, which every manifest records, so a legacy manifest is
		// judged exactly. An api/feature/data declaration is proved only by
		// a manifest that recorded declarations: one cut by a forge that
		// predates them (DeclarationsRecorded false) cannot show it carries
		// one, and such a release is, in every case but a hand-made one,
		// older than the declaration anyway. Admitting it on no evidence
		// would bind the env to code the floor exists to keep out; the
		// escape for the rare exception is a lift that acknowledges the
		// declaration, recorded.
		var missing, unproven []string
		for _, d := range f.Requires {
			switch {
			case Carries(target, d.Ref(), d.Kind):
			case d.Kind != DeclarationSchema && target.Migrations != nil && !target.Migrations.DeclarationsRecorded:
				unproven = append(unproven, describeDeclaration(d))
			default:
				missing = append(missing, describeDeclaration(d))
			}
		}
		if len(missing) == 0 && len(unproven) == 0 {
			return true, ""
		}
		var parts []string
		if len(missing) > 0 {
			why := fmt.Sprintf("%s does not carry %s", target.Version, strings.Join(missing, "; "))
			if target.Migrations == nil {
				why += " (it records no migration manifest, so it cannot be shown to)"
			}
			parts = append(parts, why)
		}
		if len(unproven) > 0 {
			parts = append(parts, fmt.Sprintf("%s cannot be shown to carry %s: it was cut by a forge that predates rollback-floor declarations, so its manifest records none (if it does carry them, lift the floor acknowledging each)",
				target.Version, strings.Join(unproven, "; ")))
		}
		return false, strings.Join(parts, "; ") + fmt.Sprintf(", declared since %s: no release without it can run here, so roll forward", f.Release)
	}
	// An unknown kind is a floor this code cannot evaluate: refusing is the
	// side that never binds an environment to a release it cannot run.
	return false, fmt.Sprintf("the floor's kind %q is not one this forge understands; upgrade forge", f.Kind)
}

func describeDeclaration(d FloorDeclaration) string {
	what := fmt.Sprintf("%s declaration %s (%s)", d.Kind, d.ID, d.Source)
	if d.Kind == DeclarationSchema {
		what = fmt.Sprintf("migration %s (%s), declared forge:breaks-rollback", d.ID, d.Source)
	}
	if d.Reason != "" {
		what += fmt.Sprintf(" (%q)", d.Reason)
	}
	return what
}

// FloorRefusal is why a target fails an environment's floors.
type FloorRefusal struct {
	// Floors are the active floors the target fails, in the order given.
	Floors []Floor `json:"floors"`
	// Clauses are one sentence per failed floor, in the same order.
	Clauses []string `json:"clauses"`
}

// Error is every clause, joined.
func (r *FloorRefusal) Error() string { return strings.Join(r.Clauses, "; ") }

// AdmitOverFloors returns nil when target satisfies every active floor, and
// otherwise the floors it fails with why. floors may include lifted ones;
// they are skipped.
func AdmitOverFloors(floors []Floor, target Release) *FloorRefusal {
	var r FloorRefusal
	for _, f := range floors {
		if ok, why := f.Admits(target); !ok {
			r.Floors = append(r.Floors, f)
			r.Clauses = append(r.Clauses, why)
		}
	}
	if len(r.Floors) == 0 {
		return nil
	}
	return &r
}

// DeclarationsRaisedBy returns the declarations target carries that no
// active declared floor already requires — what a promote of target must
// raise a declared floor for. Empty when target records none, or every one
// is already required: a re-promote, or a later release that still carries
// an earlier one's declarations, raises nothing.
func DeclarationsRaisedBy(floors []Floor, target Release) []FloorDeclaration {
	if target.Migrations == nil || len(target.Migrations.Declarations) == 0 {
		return nil
	}
	required := map[DeclarationRef]bool{}
	for _, f := range floors {
		if !f.Active() || f.Kind != FloorDeclared {
			continue
		}
		for _, d := range f.Requires {
			required[d.Ref()] = true
		}
	}
	var out []FloorDeclaration
	for _, d := range target.Migrations.Declarations {
		if !required[d.Ref()] {
			out = append(out, d)
		}
	}
	sortDeclarations(out)
	return out
}

// NewDeclaredFloor is the floor a promote of target raises for decls (from
// [DeclarationsRaisedBy]). The backend assigns ID.
func NewDeclaredFloor(env string, target Release, decls []FloorDeclaration, by Actor, at time.Time, promotionID string) Floor {
	reqs := append([]FloorDeclaration(nil), decls...)
	sortDeclarations(reqs)
	reasons := make([]string, 0, len(reqs))
	for _, d := range reqs {
		r := d.Reason
		if r == "" {
			r = d.ID + " declares forge:breaks-rollback"
		}
		reasons = append(reasons, r)
	}
	reason := strings.Join(reasons, "; ")
	if len(reason) > MaxFloorReasonBytes {
		reason = truncateUTF8(reason, MaxFloorReasonBytes-len("…")) + "…"
	}
	return Floor{
		Env: env, Kind: FloorDeclared, Source: FloorSourceDeclared,
		Release: target.Version, ReleaseCutAt: target.CreatedAt, Commit: releaseCommit(target),
		Requires: reqs, Reason: reason, SetBy: by, SetAt: at, PromotionID: promotionID,
	}
}

// PromoteRaises is every floor a promote of target to env raises (its ID left
// for the backend to assign): a declared floor for the declarations target
// carries that no active floor requires ([DeclarationsRaisedBy]), and, when
// orderReason is set (`forge env deploy --floor "<why>"`), a
// roll-forward-only order floor at target unless an active order floor is
// already there. Every backend calls this with the append, under the lock
// that serializes it, so a promotion is never visible without its floors.
func PromoteRaises(active []Floor, env string, target Release, orderReason string, by Actor, at time.Time, promotionID string) []Floor {
	var out []Floor
	if decls := DeclarationsRaisedBy(active, target); len(decls) > 0 {
		out = append(out, NewDeclaredFloor(env, target, decls, by, at, promotionID))
	}
	if strings.TrimSpace(orderReason) == "" {
		return out
	}
	for _, f := range active {
		if f.Active() && f.Kind == FloorOrder && f.Release == target.Version {
			return out
		}
	}
	return append(out, NewOrderFloor(env, target, FloorSourceRollForwardOnly, orderReason, by, at, promotionID))
}

// NewOrderFloor is an order floor at release r. source is manual or
// roll_forward_only.
func NewOrderFloor(env string, r Release, source FloorSource, reason string, by Actor, at time.Time, promotionID string) Floor {
	return Floor{
		Env: env, Kind: FloorOrder, Source: source,
		Release: r.Version, ReleaseCutAt: r.CreatedAt, Commit: releaseCommit(r),
		Reason: strings.TrimSpace(reason), SetBy: by, SetAt: at, PromotionID: promotionID,
	}
}

func releaseCommit(r Release) string {
	if r.Provenance != nil && ValidObjectID(r.Provenance.Commit) {
		return r.Provenance.Commit
	}
	if ValidObjectID(r.Git.Commit) {
		return r.Git.Commit
	}
	return ""
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// CheckNewFloor refuses a floor its environment's current release would not
// satisfy — a floor above current would make the environment inadmissible to
// itself, which is the invariant every backend relies on. current is nil for
// an environment that was never bound: any floor is admissible there.
func CheckNewFloor(f Floor, current *Release) error {
	if err := f.Validate(); err != nil {
		return err
	}
	if current == nil {
		return nil
	}
	if ok, why := f.Admits(*current); !ok {
		return fmt.Errorf("%w: the environment runs %s, which this floor would refuse: %s", ErrInvalid, current.Version, why)
	}
	return nil
}

// CheckLift refuses a lift with no reason, of a floor already lifted, or of a
// declared floor that does not name every declaration it requires. A lift of
// a declared floor is the one act that lets a release which may not run on
// the environment's schema (or API, or data) back in, so the lifter states,
// declaration by declaration, that it does not apply — usually because the
// rollout that raised the floor never ran.
func CheckLift(f Floor, reason string, acknowledged []DeclarationRef) error {
	if !f.Active() {
		return fmt.Errorf("%w: floor %s is already lifted", ErrInvalid, f.ID)
	}
	if err := validReason(reason, true); err != nil {
		return fmt.Errorf("lift: %w", err)
	}
	if f.Kind != FloorDeclared {
		return nil
	}
	named := make(map[DeclarationRef]bool, len(acknowledged))
	for _, a := range acknowledged {
		named[a] = true
	}
	var missing []string
	for _, d := range f.Requires {
		if !named[d.Ref()] {
			missing = append(missing, d.Ref().String())
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: floor %s is declared; a lift must acknowledge each declaration it requires — missing %s", ErrInvalid, f.ID, strings.Join(missing, ", "))
	}
	return nil
}

// OrderFloorsAbove returns the active order floors that refuse r — what
// `floor lower <env> <r>` lifts before it sets a floor at r. Declared floors
// are never lowered: they state what the environment's schema, API or data
// is, not a person's preference, and are lifted one by one ([CheckLift]).
func OrderFloorsAbove(floors []Floor, r Release) []Floor {
	var out []Floor
	for _, f := range floors {
		if f.Active() && f.Kind == FloorOrder {
			if ok, _ := f.Admits(r); !ok {
				out = append(out, f)
			}
		}
	}
	return out
}
