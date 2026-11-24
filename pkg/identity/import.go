package identity

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// user import
// ---------------------------------------------------------------------------
//
// An app moving onto a managed identity from another identity provider brings
// its users with it: the SAME user id (every foreign key and token subject the
// app holds stays valid), the password hash (nobody resets a password), the
// verified flag of the address, and the social accounts linked to it ("Sign in
// with GitHub" lands on the same account). Each person then signs in once more
// with the credentials they already had.
//
// Import is idempotent by user id: re-running a file converges, never
// duplicates, so a re-run is the resume path of an import that stopped. Rows are
// independent: one row's failure never aborts the rest.

// ImportUser is one account carried over from another identity provider.
type ImportUser struct {
	// ID is the account's id in the new identity, and so the `sub` of every
	// token it is issued: the id the app already keys its rows by.
	// Required; 1-200 of [A-Za-z0-9._@-].
	ID string
	// Email is the account's address and its login name. Required.
	Email string
	// EmailVerified carries the source provider's verified flag. An account
	// imported unverified signs in only once its owner proves the address.
	EmailVerified bool
	// GivenName and FamilyName are required by the issuer; an empty one is
	// derived from the address. DisplayName is optional.
	GivenName, FamilyName, DisplayName string
	// PasswordHash is a modular-crypt or PHC string: bcrypt ($2a$, $2b$,
	// $2y$), or argon2i/argon2id, scrypt or pbkdf2 where the instance is
	// configured to verify them. Optional: an account without one signs in
	// through a social link, a passkey or a magic link. There is no plaintext
	// field. It is never logged: LogValue, String and GoString redact it, and
	// it is not marshalled to JSON.
	PasswordHash string `json:"-"`
	// Links are the social accounts that sign in to this account.
	Links []ImportLink
	// Metadata is written to the account. Keys under "forge." are reserved
	// for forge and refused.
	Metadata map[string]string
	// Roles are keys of the identity's console-managed roles. A declared role
	// (members_from = "declared") is refused: its holders are the declaration's.
	Roles []string
}

// LogValue is what slog records for an ImportUser: never the hash, only
// whether there is one.
func (u ImportUser) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("id", u.ID),
		slog.String("email", u.Email),
		slog.Bool("email_verified", u.EmailVerified),
		slog.Bool("password_hash", u.PasswordHash != ""),
		slog.Int("links", len(u.Links)),
		slog.Int("metadata", len(u.Metadata)),
		slog.Any("roles", u.Roles),
	)
}

// String redacts the hash, so a %v of a user (or of a slice of them) cannot
// print one.
func (u ImportUser) String() string {
	hash := "<none>"
	if u.PasswordHash != "" {
		hash = "<redacted>"
	}
	return fmt.Sprintf("ImportUser{ID:%q Email:%q EmailVerified:%t PasswordHash:%s Links:%d Metadata:%d Roles:%v}",
		u.ID, u.Email, u.EmailVerified, hash, len(u.Links), len(u.Metadata), u.Roles)
}

// GoString redacts the hash from %#v as well.
func (u ImportUser) GoString() string { return u.String() }

// ImportLink is a social account linked to an imported user.
type ImportLink struct {
	// Provider is a social provider type the identity declares ("github",
	// "google"), resolved to the instance's provider at import.
	Provider string
	// ProviderUserID is the provider's STABLE id for the person: GitHub's
	// numeric user id, Google's `sub`. It is what the provider reports at
	// sign-in, so it is what finds the account then. Never a login or an
	// address, which can change hands.
	ProviderUserID string
	// ProviderUserName is the display name of the link (a GitHub login, an
	// address). Optional.
	ProviderUserName string
}

// ImportOptions tune an import.
type ImportOptions struct {
	// DryRun validates every row and resolves it against the instance, and
	// writes nothing. A dry run cannot see a social link another account
	// already holds (link_taken): only the write reports that.
	DryRun bool
	// UpdateExisting converges an account that already has the row's id:
	// profile, the verified flag (only ever set, never cleared), links,
	// metadata and roles are added or corrected; nothing the row does not
	// name is removed. Without it an existing account is left as it is.
	UpdateExisting bool
	// UpdatePasswords also overwrites the password hash of an existing
	// account. Separate from UpdateExisting on purpose: overwriting a live
	// account's password is the one change a person notices. A hash cannot be
	// compared, so every such row is written, and reported updated.
	UpdatePasswords bool
}

// ImportOutcome is what an import did with one row.
type ImportOutcome string

const (
	// ImportCreated: the account did not exist and was created.
	ImportCreated ImportOutcome = "created"
	// ImportUpdated: the account existed and a difference was applied.
	ImportUpdated ImportOutcome = "updated"
	// ImportUnchanged: the account existed and nothing was written. Reason
	// names what differs when UpdateExisting was not set ("differs:roles").
	ImportUnchanged ImportOutcome = "unchanged"
	// ImportConflict: the row collides with another account (its address,
	// its username or a social link belongs to someone else, or its id holds
	// another address). Nothing was written for it.
	ImportConflict ImportOutcome = "conflict"
	// ImportInvalid: the row itself is wrong (a malformed id or address, an
	// unsupported hash, an unknown provider or role). Nothing was written.
	ImportInvalid ImportOutcome = "invalid"
	// ImportFailed: the instance failed the write. Re-running retries it.
	ImportFailed ImportOutcome = "failed"
)

// OK reports whether the outcome needs no attention.
func (o ImportOutcome) OK() bool {
	return o == ImportCreated || o == ImportUpdated || o == ImportUnchanged
}

// Import reasons: why a row is not created, updated or unchanged.
const (
	ReasonInvalidID           = "invalid_id"
	ReasonDuplicateID         = "duplicate_id"
	ReasonInvalidEmail        = "invalid_email"
	ReasonUnsupportedHash     = "unsupported_hash"
	ReasonInvalidHash         = "invalid_hash"
	ReasonUnknownProvider     = "unknown_provider"
	ReasonProviderNotApplied  = "provider_not_applied"
	ReasonInvalidProviderUser = "invalid_provider_user_id"
	ReasonDuplicateLink       = "duplicate_link"
	ReasonReservedMetadata    = "reserved_metadata_key"
	ReasonInvalidMetadata     = "invalid_metadata"
	ReasonUnknownRole         = "unknown_role"
	ReasonDeclaredRole        = "declared_role"
	ReasonEmailTaken          = "email_taken_by_other_id"
	ReasonUsernameTaken       = "username_taken"
	ReasonIDHasOtherEmail     = "id_has_other_email"
	ReasonIDIsMachine         = "id_is_machine_user"
	ReasonLinkTaken           = "link_taken"
)

// ImportResult is the outcome of one row. It never carries row content beyond
// the id: it is safe to log and to write to a report.
type ImportResult struct {
	ID      string        `json:"id"`
	Outcome ImportOutcome `json:"outcome"`
	Reason  string        `json:"reason,omitempty"`
}

// ReservedMetadataPrefix is the metadata namespace forge's own sign-in logic
// writes (pkg/devidp's Meta* keys). An import may not set it: a forged
// "forge.self_signup" or proof marker would change what the broker grants.
const ReservedMetadataPrefix = "forge."

// MaxImportMetadataValue bounds one metadata value.
const MaxImportMetadataValue = 4096

var (
	importIDPattern = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,200}$`)
	bcryptPattern   = regexp.MustCompile(`^\$2[aby]\$\d\d\$[./A-Za-z0-9]{53}$`)
	numericPattern  = regexp.MustCompile(`^[0-9]{1,20}$`)
)

// hashScheme names the scheme of a password hash, or "" when it is not one
// an import accepts. The instance has the last word (its verifier
// configuration and cost bounds), which a dry run cannot ask.
func hashScheme(h string) string {
	switch {
	case strings.HasPrefix(h, "$2a$"), strings.HasPrefix(h, "$2b$"), strings.HasPrefix(h, "$2y$"):
		if bcryptPattern.MatchString(h) {
			return "bcrypt"
		}
	case strings.HasPrefix(h, "$argon2id$"), strings.HasPrefix(h, "$argon2i$"):
		return "argon2"
	case strings.HasPrefix(h, "$scrypt$"):
		return "scrypt"
	case strings.HasPrefix(h, "$pbkdf2"):
		return "pbkdf2"
	}
	return ""
}

// importTarget is what the declaration says rows may refer to.
type importTarget struct {
	roles     map[string]Role
	providers map[string]string // declared provider type -> instance IdP id ("" = not applied)
}

// validate checks a row against the declaration without asking the instance.
// It returns the refusal reason and a human detail, or "".
func (t importTarget) validate(u ImportUser) (reason, detail string) {
	if !importIDPattern.MatchString(u.ID) {
		return ReasonInvalidID, "the id must be 1-200 of [A-Za-z0-9._@-]"
	}
	if a, err := mail.ParseAddress(u.Email); err != nil || a.Address != u.Email {
		return ReasonInvalidEmail, "not a bare email address"
	}
	if u.PasswordHash != "" && hashScheme(u.PasswordHash) == "" {
		return ReasonUnsupportedHash, "accepted: bcrypt ($2a$/$2b$/$2y$), and argon2, scrypt or pbkdf2 where the instance verifies them"
	}
	seen := map[string]bool{}
	for _, l := range u.Links {
		idp, declared := t.providers[l.Provider]
		switch {
		case !declared:
			return ReasonUnknownProvider, fmt.Sprintf("%q is not a social provider the identity declares", l.Provider)
		case idp == "":
			return ReasonProviderNotApplied, fmt.Sprintf("%q is declared but not on the instance yet: converge the identity first", l.Provider)
		case strings.TrimSpace(l.ProviderUserID) == "" || len(l.ProviderUserID) > 200:
			return ReasonInvalidProviderUser, "a link needs the provider's user id"
		case l.Provider == string(IdPGitHub) && !numericPattern.MatchString(l.ProviderUserID):
			return ReasonInvalidProviderUser, "a GitHub link needs the numeric user id, never the login (logins are renamed and reused)"
		case seen[l.Provider+"\x00"+l.ProviderUserID]:
			return ReasonDuplicateLink, "the same link twice"
		}
		seen[l.Provider+"\x00"+l.ProviderUserID] = true
	}
	for k, v := range u.Metadata {
		switch {
		case strings.HasPrefix(k, ReservedMetadataPrefix):
			return ReasonReservedMetadata, fmt.Sprintf("%q: keys under %q are forge's", k, ReservedMetadataPrefix)
		case strings.TrimSpace(k) == "" || len(k) > 200:
			return ReasonInvalidMetadata, "a metadata key must be 1-200 characters"
		case v == "" || len(v) > MaxImportMetadataValue:
			return ReasonInvalidMetadata, fmt.Sprintf("%q: a metadata value must be 1-%d bytes (the issuer stores no empty value)", k, MaxImportMetadataValue)
		}
	}
	for _, r := range u.Roles {
		role, ok := t.roles[r]
		switch {
		case !ok:
			return ReasonUnknownRole, fmt.Sprintf("%q is not a role the identity declares", r)
		case role.Declared:
			return ReasonDeclaredRole, fmt.Sprintf("%q is a declared role: its holders are the declaration's members (members_from = \"declared\"), so a grant here would be revoked by the next converge", r)
		}
	}
	return "", ""
}

// ImportUsers creates or converges users on the instance, idempotent by
// ImportUser.ID, and returns one result per row in input order.
//
// spec is the identity's declaration, the one this Reconciler converges: rows
// may name only its roles and its social providers, and the import needs the
// declaration's project (roles are granted on it). Converge first: a project
// or provider the instance does not have yet refuses the rows that need it.
//
// Per row: an id the instance does not know is created with that id, the
// hash, the verified flag, the links and the metadata in ONE write, then its
// roles granted; an address another account holds is a conflict, never a
// merge. An id it knows is unchanged, or converged with UpdateExisting. A
// social link another account holds is a conflict (link_taken), never moved.
//
// The error is for the import as a whole (the instance is unreachable, the
// credential refused, the project missing); a row's failure is its result.
func (r *Reconciler) ImportUsers(ctx context.Context, spec Spec, users []ImportUser, opts ImportOptions) ([]ImportResult, error) {
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("identity: invalid spec: %w", err)
	}
	o := r.opts
	o.DryRun = o.DryRun || opts.DryRun
	p := &run{
		r: r, c: r.c, o: o, rep: &Report{DryRun: o.DryRun}, spec: spec,
		roles: map[string]Role{}, users: map[string]string{}, createdHumans: map[string]bool{},
		machine: map[string]MachineOutput{}, apps: map[string]AppOutput{}, idpIDs: map[string]string{},
	}
	readyCtx, cancel := context.WithTimeout(ctx, r.opts.ReadyTimeout)
	err := r.c.waitReady(readyCtx)
	cancel()
	if err != nil {
		return nil, err
	}
	if err := p.resolveOrg(ctx); err != nil {
		return nil, err
	}
	if err := p.locateProject(ctx); err != nil {
		return nil, err
	}
	if p.projectID == "" {
		return nil, objErr("project", spec.Project.Name, errors.New("not on the instance: converge the identity before importing users into it"))
	}

	t := importTarget{roles: map[string]Role{}, providers: map[string]string{}}
	for _, role := range spec.Roles {
		t.roles[role.Key] = role
	}
	if len(spec.IdPs) > 0 {
		all, err := pageAll[providerObj](ctx, p.c, "/admin/v1/idps/templates/_search", map[string]any{}, noOrg)
		if err != nil {
			return nil, objErr("identity providers", "instance", err)
		}
		byName := map[string]providerObj{}
		for _, o := range all {
			byName[o.Name] = o
		}
		for _, d := range spec.IdPs {
			id := ""
			if cur, ok := byName[d.Name]; ok && cur.Type == idpProviderType[d.Type] {
				id = cur.ID
			}
			t.providers[string(d.Type)] = id
		}
	}
	grants, err := p.listGrants(ctx)
	if err != nil {
		return nil, objErr("role grants", spec.Project.Name, err)
	}
	byUser := make(map[string]grantObj, len(grants))
	for _, g := range grants {
		byUser[g.UserID] = g
	}

	imp := &importer{p: p, t: t, opts: opts, grants: byUser, ids: map[string]bool{}, emails: map[string]string{}}
	results := make([]ImportResult, 0, len(users))
	for _, u := range users {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		res := imp.row(ctx, u)
		p.c.log.InfoContext(ctx, "identity: import", "user_id", res.ID, "outcome", string(res.Outcome), "reason", res.Reason, "dry_run", o.DryRun)
		results = append(results, res)
	}
	return results, nil
}

// importer is the state of one ImportUsers call.
type importer struct {
	p      *run
	t      importTarget
	opts   ImportOptions
	grants map[string]grantObj // user id -> project grant
	// ids and emails are the rows of THIS call, so two rows naming one id,
	// or one address under two ids, are refused even before the instance's
	// reads (which trail its writes) would show the first.
	ids    map[string]bool
	emails map[string]string // lowercased email -> id
}

// v2Human is a v2 user read (GET /v2/users/{id}).
type v2Human struct {
	UserID   string `json:"userId"`
	Username string `json:"username"`
	Human    *struct {
		Profile struct {
			GivenName   string `json:"givenName"`
			FamilyName  string `json:"familyName"`
			DisplayName string `json:"displayName"`
		} `json:"profile"`
		Email struct {
			Email      string `json:"email"`
			IsVerified bool   `json:"isVerified"`
		} `json:"email"`
	} `json:"human"`
	Machine *struct{} `json:"machine"`
}

func result(id string, o ImportOutcome, reason string) ImportResult {
	return ImportResult{ID: id, Outcome: o, Reason: reason}
}

func (m *importer) row(ctx context.Context, u ImportUser) ImportResult {
	u.ID = strings.TrimSpace(u.ID)
	u.Email = strings.TrimSpace(u.Email)
	if reason, _ := m.t.validate(u); reason != "" {
		return result(u.ID, ImportInvalid, reason)
	}
	if m.ids[u.ID] {
		return result(u.ID, ImportInvalid, ReasonDuplicateID)
	}
	m.ids[u.ID] = true
	email := strings.ToLower(u.Email)
	if other, ok := m.emails[email]; ok && other != u.ID {
		return result(u.ID, ImportConflict, ReasonEmailTaken)
	}
	m.emails[email] = u.ID

	cur, err := m.read(ctx, u.ID)
	if err != nil {
		return result(u.ID, ImportFailed, err.Error())
	}
	if cur == nil {
		return m.create(ctx, u)
	}
	return m.converge(ctx, u, cur)
}

// read returns the account with id, or nil when there is none.
func (m *importer) read(ctx context.Context, id string) (*v2Human, error) {
	var out struct {
		User v2Human `json:"user"`
	}
	err := m.p.c.call(ctx, http.MethodGet, "/v2/users/"+url.PathEscape(id), nil, &out, idem)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the account: %w", err)
	}
	if out.User.UserID == "" {
		return nil, nil
	}
	return &out.User, nil
}

// emailHolder returns the id of another account holding the address.
func (m *importer) emailHolder(ctx context.Context, id, email string) (string, error) {
	hits, err := pageAll[humanUser](ctx, m.p.c, "/v2/users", map[string]any{
		"queries": []any{map[string]any{"emailQuery": map[string]any{
			"emailAddress": email, "method": "TEXT_QUERY_METHOD_EQUALS_IGNORE_CASE",
		}}},
	}, idem)
	if err != nil {
		return "", fmt.Errorf("search the address: %w", err)
	}
	for _, h := range hits {
		if h.UserID != id {
			return h.UserID, nil
		}
	}
	return "", nil
}

func (m *importer) profile(u ImportUser) map[string]any {
	given, family := u.GivenName, u.FamilyName
	if given == "" || family == "" {
		derived := profileFromEmail(strings.ToLower(u.Email))
		if given == "" {
			given = derived["givenName"].(string)
		}
		if family == "" {
			family = derived["familyName"].(string)
		}
	}
	prof := map[string]any{"givenName": given, "familyName": family}
	if u.DisplayName != "" {
		prof["displayName"] = u.DisplayName
	}
	return prof
}

func (m *importer) linkBody(l ImportLink) map[string]any {
	name := l.ProviderUserName
	if name == "" {
		name = l.ProviderUserID
	}
	return map[string]any{"idpId": m.t.providers[l.Provider], "userId": l.ProviderUserID, "userName": name}
}

func metadataBody(md map[string]string) []any {
	keys := make([]string, 0, len(md))
	for k := range md {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, map[string]any{"key": k, "value": base64.StdEncoding.EncodeToString([]byte(md[k]))})
	}
	return out
}

func (m *importer) create(ctx context.Context, u ImportUser) ImportResult {
	holder, err := m.emailHolder(ctx, u.ID, u.Email)
	if err != nil {
		return result(u.ID, ImportFailed, err.Error())
	}
	if holder != "" {
		return result(u.ID, ImportConflict, ReasonEmailTaken)
	}
	if m.p.dry() {
		return result(u.ID, ImportCreated, "")
	}
	// An unverified address gets a code RETURNED, never mailed: an import
	// must not send a message to every person in the file. The owner asks
	// for a verification link when they first sign in.
	emailBody := map[string]any{"email": u.Email, "returnCode": map[string]any{}}
	if u.EmailVerified {
		emailBody = map[string]any{"email": u.Email, "isVerified": true}
	}
	body := map[string]any{
		"userId":       u.ID,
		"username":     strings.ToLower(u.Email),
		"organization": map[string]any{"orgId": m.p.c.orgID},
		"profile":      m.profile(u),
		"email":        emailBody,
	}
	if u.PasswordHash != "" {
		body["hashedPassword"] = map[string]any{"hash": u.PasswordHash, "changeRequired": false}
	}
	if len(u.Links) > 0 {
		links := make([]any, 0, len(u.Links))
		for _, l := range u.Links {
			links = append(links, m.linkBody(l))
		}
		body["idpLinks"] = links
	}
	if len(u.Metadata) > 0 {
		body["metadata"] = metadataBody(u.Metadata)
	}
	err = m.p.c.call(ctx, http.MethodPost, "/v2/users/human", body, nil, mint)
	if err != nil {
		if reason, outcome := classifyImportError(err, len(u.Links) > 0, hashScheme(u.PasswordHash)); reason != "" {
			if reason == "id_exists" {
				// The id exists after all: a read that trailed an earlier
				// write (the issuer's reads lag its writes), or a racing
				// import. Converge it as the existing account it is.
				if cur := m.readSettled(ctx, u.ID); cur != nil {
					return m.converge(ctx, u, cur)
				}
				return result(u.ID, ImportConflict, "id_exists")
			}
			return result(u.ID, outcome, reason)
		}
		return result(u.ID, ImportFailed, "create: "+err.Error())
	}
	if len(u.Roles) > 0 {
		if err := m.grant(ctx, u, grantObj{}); err != nil {
			return result(u.ID, ImportFailed, "created, but "+err.Error()+"; re-run with UpdateExisting to grant the roles")
		}
	}
	return result(u.ID, ImportCreated, "")
}

// readSettled reads an account a write just reported as existing, waiting out
// the issuer's projection lag for a few seconds.
func (m *importer) readSettled(ctx context.Context, id string) *v2Human {
	for i := 0; i < 10; i++ {
		if cur, err := m.read(ctx, id); err == nil && cur != nil {
			return cur
		}
		if m.p.c.sleep(ctx, 500*time.Millisecond) != nil {
			return nil
		}
	}
	return nil
}

// classifyImportError maps the issuer's refusal of a row to its reason.
// Branches on Zitadel's stable error ids and message keys, with the HTTP
// status as the fallback. scheme is the row's hash scheme (hashScheme).
//
// Zitadel v4.20.5 answers translated messages ("External IDP already taken
// (V3-DKcYh)", "Password is invalid (CRYPT-r8Qm2)"), so the ids carry the
// meaning. A hash of a scheme no verifier is configured for comes back as
// CRYPT-r8Qm2, the same id as a malformed one, not as CRYPT-2xK7d
// (Errors.Hash.NotSupported): for a scheme other than bcrypt, which every
// instance verifies, r8Qm2 is read as the instance not verifying it.
func classifyImportError(err error, hasLinks bool, scheme string) (string, ImportOutcome) {
	ae := asAPI(err)
	if ae == nil {
		return "", ""
	}
	text := strings.ToLower(ae.ID + " " + ae.Message + " " + ae.Body)
	switch {
	case strings.Contains(text, "crypt-2xk7d"), strings.Contains(text, "hash.notsupported"):
		return ReasonUnsupportedHash, ImportInvalid
	case strings.Contains(text, "crypt-r8qm2") && scheme != "" && scheme != "bcrypt":
		return ReasonUnsupportedHash, ImportInvalid
	case strings.Contains(text, "crypt-5uv9n"), strings.Contains(text, "crypt-r8qm2"):
		return ReasonInvalidHash, ImportInvalid
	case strings.Contains(text, "externalidp.alreadyexists"), strings.Contains(text, "external idp already"):
		return ReasonLinkTaken, ImportConflict
	case strings.Contains(text, "idpconfig.notexisting"):
		return ReasonProviderNotApplied, ImportInvalid
	case strings.Contains(text, "command-7yiox1isql"), strings.Contains(text, "command-k2unb"), strings.Contains(text, "user.alreadyexisting"):
		return "id_exists", ImportConflict
	case strings.Contains(text, "user.alreadyexists"), strings.Contains(text, "user already exists"), strings.Contains(text, "username"):
		return ReasonUsernameTaken, ImportConflict
	case isAlreadyExists(err):
		// A uniqueness refusal with no recognizable key: on a row with links
		// the link is the likelier collision (the address was checked).
		if hasLinks {
			return ReasonLinkTaken, ImportConflict
		}
		return ReasonUsernameTaken, ImportConflict
	}
	return "", ""
}

// converge handles a row whose id the instance already knows.
func (m *importer) converge(ctx context.Context, u ImportUser, cur *v2Human) ImportResult {
	if cur.Human == nil {
		return result(u.ID, ImportConflict, ReasonIDIsMachine)
	}
	if !strings.EqualFold(cur.Human.Email.Email, u.Email) {
		return result(u.ID, ImportConflict, ReasonIDHasOtherEmail)
	}
	links, err := m.links(ctx, u.ID)
	if err != nil {
		return result(u.ID, ImportFailed, err.Error())
	}
	md, err := m.metadata(ctx, u.ID, len(u.Metadata) > 0)
	if err != nil {
		return result(u.ID, ImportFailed, err.Error())
	}

	var diff []string
	prof := cur.Human.Profile
	profileDiff := (u.GivenName != "" && u.GivenName != prof.GivenName) ||
		(u.FamilyName != "" && u.FamilyName != prof.FamilyName) ||
		(u.DisplayName != "" && u.DisplayName != prof.DisplayName)
	if profileDiff {
		diff = append(diff, "profile")
	}
	verify := u.EmailVerified && !cur.Human.Email.IsVerified
	if verify {
		diff = append(diff, "email_verified")
	}
	var addLinks []ImportLink
	for _, l := range u.Links {
		if !links[m.t.providers[l.Provider]+"\x00"+l.ProviderUserID] {
			addLinks = append(addLinks, l)
		}
	}
	if len(addLinks) > 0 {
		diff = append(diff, "links")
	}
	setMD := map[string]string{}
	for k, v := range u.Metadata {
		if have, ok := md[k]; !ok || have != v {
			setMD[k] = v
		}
	}
	if len(setMD) > 0 {
		diff = append(diff, "metadata")
	}
	g := m.grants[u.ID]
	var missingRoles []string
	for _, r := range u.Roles {
		if !slices.Contains(g.RoleKeys, r) {
			missingRoles = append(missingRoles, r)
		}
	}
	if len(missingRoles) > 0 {
		diff = append(diff, "roles")
	}
	password := m.opts.UpdatePasswords && u.PasswordHash != ""

	if !m.opts.UpdateExisting {
		diff = nil // only the password may be written
	}
	if len(diff) == 0 && !password {
		if !m.opts.UpdateExisting {
			if d := m.wouldDiffer(profileDiff, verify, addLinks, setMD, missingRoles); d != "" {
				return result(u.ID, ImportUnchanged, "differs:"+d)
			}
		}
		return result(u.ID, ImportUnchanged, "")
	}
	changed := slices.Clone(diff)
	if password {
		changed = append(changed, "password")
	}
	if m.p.dry() {
		return result(u.ID, ImportUpdated, strings.Join(changed, ","))
	}

	base := "/v2/users/" + url.PathEscape(u.ID)
	// The writes that the instance can refuse for the row's own sake (a
	// link another account holds, a hash it does not accept) go first, so a
	// refusal usually lands before anything was written. One that lands
	// after an earlier write is reported as failed, naming what was already
	// written: conflict and invalid promise nothing was.
	var written []string
	refused := func(reason string, outcome ImportOutcome) ImportResult {
		if len(written) == 0 {
			return result(u.ID, outcome, reason)
		}
		return result(u.ID, ImportFailed, reason+"; already written: "+strings.Join(written, ","))
	}
	failed := func(reason string) ImportResult {
		if len(written) > 0 {
			reason += "; already written: " + strings.Join(written, ",")
		}
		return result(u.ID, ImportFailed, reason)
	}
	if m.opts.UpdateExisting {
		for _, l := range addLinks {
			err := m.p.c.call(ctx, http.MethodPost, base+"/links", map[string]any{"idpLink": m.linkBody(l)}, nil, idem)
			if err != nil && !isNoChange(err) {
				if reason, outcome := classifyImportError(err, true, ""); reason == ReasonLinkTaken || reason == ReasonProviderNotApplied {
					return refused(reason, outcome)
				}
				return failed("link: " + err.Error())
			}
			if !slices.Contains(written, "links") {
				written = append(written, "links")
			}
		}
	}
	if password {
		err := m.p.c.call(ctx, http.MethodPut, "/v2/users/human/"+url.PathEscape(u.ID), map[string]any{
			"password": map[string]any{"hashedPassword": map[string]any{"hash": u.PasswordHash, "changeRequired": false}},
		}, nil, idem)
		if err != nil {
			if reason, outcome := classifyImportError(err, false, hashScheme(u.PasswordHash)); reason == ReasonUnsupportedHash || reason == ReasonInvalidHash {
				return refused(reason, outcome)
			}
			return failed("password: " + err.Error())
		}
		written = append(written, "password")
	}
	if profileDiff && m.opts.UpdateExisting {
		// The row's names over the account's current ones: a name the row
		// omits keeps its value, never one derived from the address.
		cp := cur.Human.Profile
		next := ImportUser{Email: u.Email, GivenName: cmp.Or(u.GivenName, cp.GivenName), FamilyName: cmp.Or(u.FamilyName, cp.FamilyName), DisplayName: cmp.Or(u.DisplayName, cp.DisplayName)}
		if err := m.p.c.call(ctx, http.MethodPut, "/v2/users/human/"+url.PathEscape(u.ID), map[string]any{"profile": m.profile(next)}, nil, idem); err != nil && !isNoChange(err) {
			return failed("profile: " + err.Error())
		}
		written = append(written, "profile")
	}
	if verify && m.opts.UpdateExisting {
		if err := m.markVerified(ctx, u.ID); err != nil {
			return failed("email_verified: " + err.Error())
		}
		written = append(written, "email_verified")
	}
	if m.opts.UpdateExisting {
		if len(setMD) > 0 {
			if err := m.p.c.call(ctx, http.MethodPost, base+"/metadata", map[string]any{"metadata": metadataBody(setMD)}, nil, idem); err != nil {
				return failed("metadata: " + err.Error())
			}
			written = append(written, "metadata")
		}
		if len(missingRoles) > 0 {
			if err := m.grant(ctx, u, g); err != nil {
				return failed(err.Error())
			}
		}
	}
	return result(u.ID, ImportUpdated, strings.Join(changed, ","))
}

// wouldDiffer names what UpdateExisting would change.
func (m *importer) wouldDiffer(profile, verify bool, links []ImportLink, md map[string]string, roles []string) string {
	var d []string
	if profile {
		d = append(d, "profile")
	}
	if verify {
		d = append(d, "email_verified")
	}
	if len(links) > 0 {
		d = append(d, "links")
	}
	if len(md) > 0 {
		d = append(d, "metadata")
	}
	if len(roles) > 0 {
		d = append(d, "roles")
	}
	return strings.Join(d, ",")
}

// links returns the account's social links, keyed "<idp id>\x00<external id>".
func (m *importer) links(ctx context.Context, id string) (map[string]bool, error) {
	hits, err := pageAll[struct {
		IdPID  string `json:"idpId"`
		UserID string `json:"userId"`
	}](ctx, m.p.c, "/v2/users/"+url.PathEscape(id)+"/links/_search", map[string]any{}, idem)
	if err != nil {
		return nil, fmt.Errorf("read the account's links: %w", err)
	}
	out := make(map[string]bool, len(hits))
	for _, h := range hits {
		out[h.IdPID+"\x00"+h.UserID] = true
	}
	return out, nil
}

// metadata returns the account's metadata; nothing is read when the row
// carries none.
func (m *importer) metadata(ctx context.Context, id string, need bool) (map[string]string, error) {
	if !need {
		return nil, nil
	}
	var out struct {
		Metadata []struct {
			Key   string `json:"key"`
			Value []byte `json:"value"` // the API's bytes: standard base64 in JSON
		} `json:"metadata"`
	}
	if err := m.p.c.call(ctx, http.MethodPost, "/v2/users/"+url.PathEscape(id)+"/metadata/search", map[string]any{}, &out, idem); err != nil {
		return nil, fmt.Errorf("read the account's metadata: %w", err)
	}
	md := make(map[string]string, len(out.Metadata))
	for _, e := range out.Metadata {
		md[e.Key] = string(e.Value)
	}
	return md, nil
}

// markVerified marks the account's current address verified. Zitadel refuses
// to "set" an unchanged address as verified, so a code is minted (returned,
// never mailed) and redeemed at once, as devidp's markAddressVerified does.
func (m *importer) markVerified(ctx context.Context, id string) error {
	base := "/v2/users/" + url.PathEscape(id)
	var out struct {
		VerificationCode string `json:"verificationCode"`
	}
	if err := m.p.c.call(ctx, http.MethodPost, base+"/email/resend", map[string]any{"returnCode": map[string]any{}}, &out, mint); err != nil {
		return fmt.Errorf("mint a verification code: %w", err)
	}
	if out.VerificationCode == "" {
		return errors.New("the issuer returned no verification code")
	}
	return m.p.c.call(ctx, http.MethodPost, base+"/email/verify", map[string]any{"verificationCode": out.VerificationCode}, nil, mint)
}

// grant gives the account the row's roles, keeping any it holds.
func (m *importer) grant(ctx context.Context, u ImportUser, cur grantObj) error {
	want := map[string]bool{}
	for _, r := range u.Roles {
		want[r] = true
	}
	if err := m.p.grantRoles(ctx, u.ID, u.ID, cur, want); err != nil {
		return fmt.Errorf("roles: %w", err)
	}
	return nil
}
