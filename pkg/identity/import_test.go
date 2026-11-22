package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

// A real bcrypt hash (cost 10) of "Correct-Horse-1". The fake does not verify
// it; the e2e against the pinned Zitadel signs in with it.
const testBcrypt = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// importSpec declares one console role, one declared role and two social
// providers. Only GitHub is put on the instance by convergedForImport, so
// Google is declared but not applied.
func importSpec() Spec {
	return Spec{
		Project: ProjectSpec{Name: "acme"},
		Roles: []Role{
			{Key: "member"},
			{Key: "admin", Declared: true},
		},
		IdPs: []IdP{
			{Name: IdPName(IdPGitHub), Type: IdPGitHub, ClientID: "gh-client", ClientSecret: "gh-secret", Scopes: []string{"read:user"}},
			{Name: IdPName(IdPGoogle), Type: IdPGoogle, ClientID: "g-client", ClientSecret: "g-secret", Scopes: []string{"openid"}},
		},
	}
}

// convergedForImport converges the spec without Google, so the instance has
// the project, its roles and the GitHub provider.
func convergedForImport(t *testing.T, mod func(*Options)) (*fakeZitadel, *Reconciler) {
	t.Helper()
	f := newFake(t)
	r, _ := newRec(t, f, mod)
	spec := importSpec()
	spec.IdPs = spec.IdPs[:1]
	if _, err := r.Reconcile(context.Background(), spec); err != nil {
		t.Fatalf("converge: %v", err)
	}
	return f, r
}

func ada() ImportUser {
	return ImportUser{
		ID: "4f1c2a9e-0000-4000-8000-000000000001", Email: "ada@acme.example", EmailVerified: true,
		GivenName: "Ada", FamilyName: "Lovelace", DisplayName: "Ada",
		PasswordHash: testBcrypt,
		Links:        []ImportLink{{Provider: "github", ProviderUserID: "583231", ProviderUserName: "ada"}},
		Metadata:     map[string]string{"legacy_plan": "pro"},
		Roles:        []string{"member"},
	}
}

func importOne(t *testing.T, r *Reconciler, opts ImportOptions, users ...ImportUser) []ImportResult {
	t.Helper()
	res, err := r.ImportUsers(context.Background(), importSpec(), users, opts)
	if err != nil {
		t.Fatalf("ImportUsers: %v", err)
	}
	if len(res) != len(users) {
		t.Fatalf("%d results for %d rows", len(res), len(users))
	}
	return res
}

func wantResult(t *testing.T, got ImportResult, outcome ImportOutcome, reason string) {
	t.Helper()
	if got.Outcome != outcome || got.Reason != reason {
		t.Errorf("row %s = %s %q, want %s %q", got.ID, got.Outcome, got.Reason, outcome, reason)
	}
}

func (f *fakeZitadel) githubID() string {
	for _, i := range f.idps {
		if i.Type == "github" {
			return i.ID
		}
	}
	return ""
}

func TestImportUsers_CreatesWithTheCallersIDHashVerifiedAddressLinksMetadataAndRoles(t *testing.T) {
	f, r := convergedForImport(t, nil)
	u := ada()
	res := importOne(t, r, ImportOptions{}, u)
	wantResult(t, res[0], ImportCreated, "")

	got := f.users[u.ID]
	if got == nil {
		t.Fatalf("no account under the imported id %s", u.ID)
	}
	if got.Hash != testBcrypt || got.Unverified || got.UserName != u.Email || got.Email != u.Email ||
		got.Given != "Ada" || got.Family != "Lovelace" || got.DisplayName != "Ada" {
		t.Errorf("account = %+v", got)
	}
	if want := []fLink{{IdPID: f.githubID(), UserID: "583231", UserName: "ada"}}; !reflect.DeepEqual(got.Links, want) {
		t.Errorf("links = %+v, want %+v", got.Links, want)
	}
	if got.Metadata["legacy_plan"] != "pro" {
		t.Errorf("metadata = %v", got.Metadata)
	}
	var roles []string
	for _, g := range f.grants {
		if g.UserID == u.ID {
			roles = g.Roles
		}
	}
	if !reflect.DeepEqual(roles, []string{"member"}) {
		t.Errorf("roles = %v", roles)
	}
}

func TestImportUsers_ReRunIsUnchangedAndWritesNothing(t *testing.T) {
	f, r := convergedForImport(t, nil)
	importOne(t, r, ImportOptions{}, ada())
	before := f.writeCount()
	res := importOne(t, r, ImportOptions{}, ada())
	wantResult(t, res[0], ImportUnchanged, "")
	// UpdateExisting on a converged account writes nothing either.
	res = importOne(t, r, ImportOptions{UpdateExisting: true}, ada())
	wantResult(t, res[0], ImportUnchanged, "")
	if f.writeCount() != before {
		t.Errorf("a re-run wrote: %v", f.log[before:])
	}
}

func TestImportUsers_AnAddressAnotherAccountHoldsIsAConflict(t *testing.T) {
	f, r := convergedForImport(t, nil)
	f.users["someone"] = &fUser{ID: "someone", UserName: "ada@acme.example", Email: "Ada@Acme.example"}
	before := f.writeCount()
	res := importOne(t, r, ImportOptions{}, ada())
	wantResult(t, res[0], ImportConflict, ReasonEmailTaken)
	if f.writeCount() != before || f.users[ada().ID] != nil {
		t.Error("a conflicting row was written")
	}

	// Two rows of one file naming one address under two ids: the second is
	// refused before the instance's lagging reads could show the first.
	f2, r2 := convergedForImport(t, nil)
	other := ada()
	other.ID, other.Links = "other-id", nil
	res = importOne(t, r2, ImportOptions{}, ada(), other)
	wantResult(t, res[0], ImportCreated, "")
	wantResult(t, res[1], ImportConflict, ReasonEmailTaken)
	if f2.users["other-id"] != nil {
		t.Error("the second row was created")
	}
}

func TestImportUsers_AnIDHoldingAnotherAddressOrAMachineIsAConflict(t *testing.T) {
	f, r := convergedForImport(t, nil)
	f.users[ada().ID] = &fUser{ID: ada().ID, UserName: "grace@acme.example", Email: "grace@acme.example"}
	f.users["svc"] = &fUser{ID: "svc", UserName: "svc", Machine: true}
	machine := ada()
	machine.ID, machine.Email, machine.Links = "svc", "svc@acme.example", nil
	res := importOne(t, r, ImportOptions{UpdateExisting: true}, ada(), machine)
	wantResult(t, res[0], ImportConflict, ReasonIDHasOtherEmail)
	wantResult(t, res[1], ImportConflict, ReasonIDIsMachine)
}

func TestImportUsers_RefusesHashesTheInstanceCannotVerify(t *testing.T) {
	f, r := convergedForImport(t, nil)
	md5 := ada()
	md5.ID, md5.Email, md5.Links, md5.PasswordHash = "md5", "md5@acme.example", nil, "$1$saltsalt$2vnaRpHa6Jxjz5n83ok8Z0"
	plain := ada()
	plain.ID, plain.Email, plain.Links, plain.PasswordHash = "plain", "plain@acme.example", nil, "Correct-Horse-1"
	// argon2 is a scheme forge accepts; this instance (like one with no
	// Verifiers configured) refuses it, and the row says so.
	argon := ada()
	argon.ID, argon.Email, argon.Links, argon.PasswordHash = "argon", "argon@acme.example", nil, "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA"
	res := importOne(t, r, ImportOptions{}, md5, plain, argon, ada())
	wantResult(t, res[0], ImportInvalid, ReasonUnsupportedHash)
	wantResult(t, res[1], ImportInvalid, ReasonUnsupportedHash)
	wantResult(t, res[2], ImportInvalid, ReasonUnsupportedHash)
	// Rows are independent: the good one after them still lands.
	wantResult(t, res[3], ImportCreated, "")
	for _, id := range []string{"md5", "plain", "argon"} {
		if f.users[id] != nil {
			t.Errorf("row %s was created", id)
		}
	}
}

func TestImportUsers_RefusesProvidersRolesAndMetadataTheDeclarationDoesNotAllow(t *testing.T) {
	f, r := convergedForImport(t, nil)
	row := func(id string, mod func(*ImportUser)) ImportUser {
		u := ada()
		u.ID, u.Email = id, id+"@acme.example"
		mod(&u)
		return u
	}
	rows := []ImportUser{
		row("okta", func(u *ImportUser) { u.Links = []ImportLink{{Provider: "okta", ProviderUserID: "1"}} }),
		row("google", func(u *ImportUser) { u.Links = []ImportLink{{Provider: "google", ProviderUserID: "1170"}} }),
		row("ghlogin", func(u *ImportUser) { u.Links = []ImportLink{{Provider: "github", ProviderUserID: "ada"}} }),
		row("twice", func(u *ImportUser) { u.Links = append(u.Links, u.Links[0]) }),
		row("reserved", func(u *ImportUser) { u.Metadata = map[string]string{"forge.self_signup": "1"} }),
		row("emptymd", func(u *ImportUser) { u.Metadata = map[string]string{"plan": ""} }),
		row("norole", func(u *ImportUser) { u.Roles = []string{"superuser"} }),
		row("declared", func(u *ImportUser) { u.Roles = []string{"admin"} }),
		row("bad id!", func(*ImportUser) {}),
		row("bademail", func(u *ImportUser) { u.Email = "Ada <ada@acme.example>" }),
	}
	before := f.writeCount()
	res := importOne(t, r, ImportOptions{}, rows...)
	for i, want := range []string{ReasonUnknownProvider, ReasonProviderNotApplied, ReasonInvalidProviderUser, ReasonDuplicateLink,
		ReasonReservedMetadata, ReasonInvalidMetadata, ReasonUnknownRole, ReasonDeclaredRole, ReasonInvalidID, ReasonInvalidEmail} {
		wantResult(t, res[i], ImportInvalid, want)
	}
	if f.writeCount() != before {
		t.Errorf("an invalid row was written: %v", f.log[before:])
	}
}

func TestImportUsers_ALinkAnotherAccountHoldsIsAConflict(t *testing.T) {
	f, r := convergedForImport(t, nil)
	importOne(t, r, ImportOptions{}, ada())
	thief := ada()
	thief.ID, thief.Email = "thief", "thief@acme.example"
	res := importOne(t, r, ImportOptions{}, thief)
	wantResult(t, res[0], ImportConflict, ReasonLinkTaken)
	if f.users["thief"] != nil {
		t.Error("the row holding a taken link was created")
	}
	// Adding it to an existing account is refused the same way, never moved.
	f.users["plain"] = &fUser{ID: "plain", UserName: "plain@acme.example", Email: "plain@acme.example"}
	plain := ada()
	plain.ID, plain.Email, plain.Roles, plain.Metadata = "plain", "plain@acme.example", nil, nil
	res = importOne(t, r, ImportOptions{UpdateExisting: true}, plain)
	wantResult(t, res[0], ImportConflict, ReasonLinkTaken)
	if len(f.users[ada().ID].Links) != 1 || len(f.users["plain"].Links) != 0 {
		t.Error("a taken link moved")
	}
}

func TestImportUsers_DryRunWritesNothing(t *testing.T) {
	f, r := convergedForImport(t, nil)
	f.users["existing"] = &fUser{ID: "existing", UserName: "e@acme.example", Email: "e@acme.example", Given: "E", Family: "X"}
	existing := ada()
	existing.ID, existing.Email, existing.Links = "existing", "e@acme.example", nil
	bad := ada()
	bad.ID, bad.Email, bad.Roles = "bad", "bad@acme.example", []string{"nope"}
	before := f.writeCount()
	res := importOne(t, r, ImportOptions{DryRun: true, UpdateExisting: true, UpdatePasswords: true}, ada(), existing, bad)
	wantResult(t, res[0], ImportCreated, "")
	wantResult(t, res[1], ImportUpdated, "profile,metadata,roles,password")
	wantResult(t, res[2], ImportInvalid, ReasonUnknownRole)
	if f.writeCount() != before {
		t.Errorf("a dry run wrote: %v", f.log[before:])
	}
	if f.users[ada().ID] != nil || f.users["existing"].Hash != "" {
		t.Error("a dry run changed an account")
	}
	// The Reconciler's own DryRun option is honoured too.
	f2, r2 := convergedForImport(t, func(o *Options) { o.DryRun = false })
	r2.opts.DryRun = true
	before = f2.writeCount()
	wantResult(t, importOne(t, r2, ImportOptions{}, ada())[0], ImportCreated, "")
	if f2.writeCount() != before {
		t.Error("Options.DryRun did not stop the write")
	}
}

func TestImportUsers_UpdateExistingAndUpdatePasswordsAreSeparateOptIns(t *testing.T) {
	f, r := convergedForImport(t, nil)
	f.users[ada().ID] = &fUser{ID: ada().ID, UserName: "ada@acme.example", Email: "ada@acme.example", Given: "A", Family: "L", Unverified: true, Hash: "$2a$10$old"}
	f.users[ada().ID].EmailCode = ""

	res := importOne(t, r, ImportOptions{}, ada())
	wantResult(t, res[0], ImportUnchanged, "differs:profile,email_verified,links,metadata,roles")
	if f.users[ada().ID].Hash != "$2a$10$old" {
		t.Error("the hash was overwritten without UpdatePasswords")
	}

	res = importOne(t, r, ImportOptions{UpdateExisting: true}, ada())
	wantResult(t, res[0], ImportUpdated, "profile,email_verified,links,metadata,roles")
	got := f.users[ada().ID]
	if got.Given != "Ada" || got.Unverified || len(got.Links) != 1 || got.Metadata["legacy_plan"] != "pro" || got.Hash != "$2a$10$old" {
		t.Errorf("converged account = %+v", got)
	}

	res = importOne(t, r, ImportOptions{UpdatePasswords: true}, ada())
	wantResult(t, res[0], ImportUpdated, "password")
	if f.users[ada().ID].Hash != testBcrypt {
		t.Error("UpdatePasswords did not write the hash")
	}
}

// TestImportUsers_NeverLogsTheHash: neither the library's own log, nor any of
// the ways a caller might print a row, carries the hash.
func TestImportUsers_NeverLogsTheHash(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f, r := convergedForImport(t, func(o *Options) { o.Logger = logger })
	_ = f
	u := ada()
	importOne(t, r, ImportOptions{}, u)
	bad := u
	bad.ID, bad.Email, bad.PasswordHash = "bad", "bad@acme.example", "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$c2VjcmV0LWhhc2g"
	importOne(t, r, ImportOptions{}, bad)

	logger.Info("caller", "user", u, "users", []ImportUser{u})
	b, _ := json.Marshal(u)
	printed := logs.String() + fmt.Sprintf("%v %+v %#v %s", u, []ImportUser{u}, u, u) + string(b)
	for _, secret := range []string{testBcrypt, "N9qo8uLOickgx2ZMRZoMy", bad.PasswordHash, "c2VjcmV0LWhhc2g"} {
		if strings.Contains(printed, secret) {
			t.Fatalf("the hash %q leaked into:\n%s", secret, printed)
		}
	}
	if !strings.Contains(logs.String(), `"outcome":"created"`) || !strings.Contains(printed, "<redacted>") {
		t.Errorf("expected the outcome and a redaction marker:\n%s", printed)
	}
}

func TestImportUsers_RefusesAnUnconvergedIdentity(t *testing.T) {
	f := newFake(t)
	r, _ := newRec(t, f, nil)
	_, err := r.ImportUsers(context.Background(), importSpec(), []ImportUser{ada()}, ImportOptions{})
	if err == nil || !strings.Contains(err.Error(), "converge the identity") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseImportFile_JSONL(t *testing.T) {
	in := strings.Join([]string{
		`{"v":1,"id":"u1","email":"ada@acme.example","email_verified":true,"given_name":"Ada","family_name":"L","display_name":"Ada",` +
			`"password_hash":"` + testBcrypt + `","links":[{"provider":"github","provider_user_id":"583231","provider_user_name":"ada"}],` +
			`"metadata":{"legacy_plan":"pro"},"roles":["member"]}`,
		``,
		`{"v":1,"id":"u2","email":"b@acme.example","password":"hunter2"}`,
		`{"v":2,"id":"u3","email":"c@acme.example"}`,
		`{"v":1,"id":"u4","emial":"typo@acme.example"}`,
		`not json`,
	}, "\n")
	type row struct {
		line int
		u    ImportUser
		err  string
	}
	var rows []row
	if err := ParseImportFile(strings.NewReader(in), func(line int, u ImportUser, err error) error {
		r := row{line: line, u: u}
		if err != nil {
			r.err = err.Error()
		}
		rows = append(rows, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("rows = %+v", rows)
	}
	want := ada()
	want.ID, want.Email, want.FamilyName = "u1", "ada@acme.example", "L"
	if rows[0].err != "" || rows[0].line != 1 || !reflect.DeepEqual(rows[0].u, want) {
		t.Errorf("row 1 = %+v (%s)", rows[0].u, rows[0].err)
	}
	for i, w := range []string{`no plaintext "password"`, `"v" is 2`, `unknown field "emial"`, "not a version-1 import row"} {
		r := rows[i+1]
		if !strings.Contains(r.err, w) {
			t.Errorf("line %d error = %q, want it to mention %q", r.line, r.err, w)
		}
		if strings.Contains(r.err, "hunter2") {
			t.Error("a row's content leaked into its error")
		}
	}
	if rows[1].line != 3 || rows[1].u.ID != "u2" {
		t.Errorf("an unreadable row keeps its line and id: %+v", rows[1])
	}
}

func TestParseImportFile_CSV(t *testing.T) {
	in := "id,email,email_verified,given_name,family_name,password_hash,roles,link.github,link.google,meta.legacy_plan\n" +
		"u1,ada@acme.example,true,Ada,L," + testBcrypt + ",member;owner,583231:ada,,pro\n" +
		"u2,b@acme.example,,,,,,,1170 1171:b@gmail.com,\n" +
		"u3,c@acme.example,maybe,,,,,,,\n"
	var got []ImportUser
	var errs []string
	if err := ParseImportFile(strings.NewReader(in), func(line int, u ImportUser, err error) error {
		got = append(got, u)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%d: %v", line, err))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("rows = %v", got)
	}
	want := ImportUser{ID: "u1", Email: "ada@acme.example", EmailVerified: true, GivenName: "Ada", FamilyName: "L",
		PasswordHash: testBcrypt, Roles: []string{"member", "owner"},
		Links: []ImportLink{{Provider: "github", ProviderUserID: "583231", ProviderUserName: "ada"}}, Metadata: map[string]string{"legacy_plan": "pro"}}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("row 1 = %#v\nwant %#v", got[0], want)
	}
	if wl := []ImportLink{{Provider: "google", ProviderUserID: "1170"}, {Provider: "google", ProviderUserID: "1171", ProviderUserName: "b@gmail.com"}}; !reflect.DeepEqual(got[1].Links, wl) || got[1].Metadata != nil || got[1].EmailVerified {
		t.Errorf("row 2 = %+v", got[1])
	}
	if len(errs) != 1 || !strings.HasPrefix(errs[0], "4: ") || !strings.Contains(errs[0], "email_verified") {
		t.Errorf("errors = %v", errs)
	}

	for header, want := range map[string]string{
		"id,email,password\n":  `no plaintext "password" column`,
		"id,email,colour\n":    `unknown column "colour"`,
		"email,given_name\n":   "id and email columns are required",
		"id,email,id\n":        "appears twice",
		"\ufeffid,email,v\n1,": "",
	} {
		err := ParseImportFile(strings.NewReader(header), func(int, ImportUser, error) error { return nil })
		if want == "" {
			if err != nil {
				t.Errorf("header %q: %v", header, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("header %q: err = %v, want %q", header, err, want)
		}
	}
}

func TestParseImportFile_EachErrorStopsAndEmptyIsNothing(t *testing.T) {
	if err := ParseImportFile(strings.NewReader("  \n\n"), func(int, ImportUser, error) error {
		t.Error("a row in an empty file")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stop := fmt.Errorf("stop")
	n := 0
	err := ParseImportFile(strings.NewReader(`{"v":1,"id":"a","email":"a@x.example"}`+"\n"+`{"v":1,"id":"b","email":"b@x.example"}`), func(int, ImportUser, error) error {
		n++
		return stop
	})
	if err != stop || n != 1 {
		t.Fatalf("err = %v after %d rows", err, n)
	}
}

// TestClassifyImportError pins the mapping of Zitadel's refusals (as v4.20.5
// sends them: translated messages, stable ids) to row reasons.
func TestClassifyImportError(t *testing.T) {
	apiErr := func(status, code int, id, msg string) error {
		return &APIError{Status: status, Code: code, ID: id, Message: msg + " (" + id + ")"}
	}
	for _, tc := range []struct {
		err     error
		links   bool
		scheme  string
		reason  string
		outcome ImportOutcome
	}{
		{apiErr(409, 6, "V3-DKcYh", "External IDP already taken"), true, "bcrypt", ReasonLinkTaken, ImportConflict},
		{apiErr(409, 6, "V3-DKcYh", "User already exists"), false, "bcrypt", ReasonUsernameTaken, ImportConflict},
		{apiErr(400, 9, "COMMAND-7yiox1isql", "User already exists"), false, "bcrypt", "id_exists", ImportConflict},
		{apiErr(400, 3, "CRYPT-r8Qm2", "Password is invalid"), false, "argon2", ReasonUnsupportedHash, ImportInvalid},
		{apiErr(400, 3, "CRYPT-r8Qm2", "Password is invalid"), false, "bcrypt", ReasonInvalidHash, ImportInvalid},
		{apiErr(400, 3, "CRYPT-5uV9n", "Password is invalid"), false, "bcrypt", ReasonInvalidHash, ImportInvalid},
		{apiErr(400, 3, "CRYPT-2xK7d", "Hash not supported"), false, "scrypt", ReasonUnsupportedHash, ImportInvalid},
		{apiErr(400, 9, "COMMAND-39nf2", "Errors.IDPConfig.NotExisting"), true, "", ReasonProviderNotApplied, ImportInvalid},
		{apiErr(500, 13, "X-1", "boom"), false, "bcrypt", "", ""},
		{fmt.Errorf("dial tcp: refused"), false, "", "", ""},
	} {
		reason, outcome := classifyImportError(tc.err, tc.links, tc.scheme)
		if reason != tc.reason || outcome != tc.outcome {
			t.Errorf("%v (links=%t, %s) = %q %q, want %q %q", tc.err, tc.links, tc.scheme, reason, outcome, tc.reason, tc.outcome)
		}
	}
}

// A row that changes only the display name keeps the account's given and
// family names: an omitted name is never replaced by one derived from the
// address.
func TestImportUsers_UpdatingOneProfileFieldKeepsTheOthers(t *testing.T) {
	f, r := convergedForImport(t, nil)
	f.users[ada().ID] = &fUser{ID: ada().ID, UserName: "ada@acme.example", Email: "ada@acme.example", Given: "Augusta", Family: "King", DisplayName: "Ada"}
	row := ImportUser{ID: ada().ID, Email: "ada@acme.example", DisplayName: "Countess"}
	res := importOne(t, r, ImportOptions{UpdateExisting: true}, row)
	wantResult(t, res[0], ImportUpdated, "profile")
	if got := f.users[ada().ID]; got.Given != "Augusta" || got.Family != "King" || got.DisplayName != "Countess" {
		t.Errorf("profile = %q %q %q, want Augusta King Countess", got.Given, got.Family, got.DisplayName)
	}
}

// A refusal after an earlier write of the same row is not "conflict" (which
// promises nothing was written): it fails, naming what was written.
func TestImportUsers_ARefusalAfterAWriteSaysWhatWasWritten(t *testing.T) {
	f, r := convergedForImport(t, nil)
	importOne(t, r, ImportOptions{}, ada()) // ada holds github 583231
	f.users["plain"] = &fUser{ID: "plain", UserName: "plain@acme.example", Email: "plain@acme.example"}
	plain := ImportUser{ID: "plain", Email: "plain@acme.example", Links: []ImportLink{
		{Provider: "github", ProviderUserID: "999"},    // free: written
		{Provider: "github", ProviderUserID: "583231"}, // ada's: refused
	}}
	res := importOne(t, r, ImportOptions{UpdateExisting: true}, plain)
	wantResult(t, res[0], ImportFailed, ReasonLinkTaken+"; already written: links")
	if len(f.users["plain"].Links) != 1 {
		t.Errorf("links = %+v, want the free one", f.users["plain"].Links)
	}
}

// A malformed first field is reported as that row's error, never a panic.
func TestParseImportFile_CSVMalformedRow(t *testing.T) {
	in := "id,email\n\"u1,a@acme.example\n"
	var errs []string
	if err := ParseImportFile(strings.NewReader(in), func(line int, _ ImportUser, err error) error {
		if err != nil {
			errs = append(errs, fmt.Sprintf("%d: %v", line, err))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "not a CSV row") {
		t.Errorf("errors = %v", errs)
	}
}
