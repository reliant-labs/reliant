package cli

// `forge cloud identity import`: bring users from another identity provider
// into an environment's managed identity, keeping each user's id, password
// hash, verified address and social links (forge G5).
//
// ONE LIBRARY, TWO TRANSPORTS. A host-run identity (forge.IdentityOnHost, the
// dev IdP `forge env up` runs) is imported into directly, with pkg/identity
// and the IdP's bootstrap token, so a migration can be rehearsed locally with
// the very file it will run against prod. A hosted identity is imported into
// by its control plane (controlplane.v1.IdentityService/ImportUsers), which
// runs the same library against the env's instance. A control plane that
// predates the rpc is told apart from a refusal and said so.
//
// THE FILE CARRIES CREDENTIALS. Nothing here prints, logs or writes a password
// hash: results carry the id, the outcome and a reason, and the --report file
// is those three per row.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/devidentity"
	"github.com/reliant-labs/forge/pkg/identity"
)

const procIdentityImport = "controlplane.v1.IdentityService/ImportUsers"

// maxIdentityImportBatch is the most rows one ImportUsers call carries, the
// control plane's limit.
const maxIdentityImportBatch = 500

// identityImporter imports one batch of users into an env's identity.
type identityImporter interface {
	importBatch(ctx context.Context, users []identity.ImportUser, opts identity.ImportOptions) ([]identity.ImportResult, error)
	// where names the identity for the summary line.
	where() string
}

// identityImportTarget picks the importer for env: the host-run IdP directly,
// or the env's control plane. A seam so the contract tests need no KCL.
var identityImportTarget = resolveIdentityImportTarget

func resolveIdentityImportTarget(ctx context.Context, env string, f *cloudIdentityFlags) (identityImporter, error) {
	projectDir := projectDirForKCL()
	e, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return nil, fmt.Errorf("render KCL for env %q: %w", env, err)
	}
	if id := hostIdentity(e); id != nil {
		prov, err := secretProviderFromEntities(e, projectDir)
		if err != nil {
			return nil, err
		}
		in, err := devIdentityInput(e, id, projectDir, prov)
		if err != nil {
			return nil, err
		}
		zitadel, _ := hostZitadel(e, id) // devIdentityInput just found it
		loc := idpPATLocations(zitadel, projectDir)
		if in.PATPath = loc.Find(); in.PATPath == "" {
			return nil, fmt.Errorf("identity %s runs on this env's host IdP, and no token for it was found (looked in %s): run `%s env up %s` first",
				id.Name, strings.Join(loc.Searched(), ", "), Name(), env)
		}
		return &hostIdentityImporter{in: in, env: env}, nil
	}
	client, req, err := f.target(ctx, env)
	if err != nil {
		return nil, err
	}
	return &cloudIdentityImporter{client: client, req: req, env: env}, nil
}

// hostIdentityImporter imports into a host-run identity with pkg/identity.
type hostIdentityImporter struct {
	in  devidentity.Input
	env string
}

func (h *hostIdentityImporter) where() string {
	return fmt.Sprintf("identity %s on env %s's host IdP (%s)", h.in.Identity.Name, h.env, h.in.BaseURL)
}

func (h *hostIdentityImporter) importBatch(ctx context.Context, users []identity.ImportUser, opts identity.ImportOptions) ([]identity.ImportResult, error) {
	res, _, err := devidentity.Import(ctx, h.in, users, opts)
	if err != nil {
		return nil, fmt.Errorf("%w (is `%s env up %s` running?)", err, Name(), h.env)
	}
	return res, nil
}

// Proto3 JSON of controlplane.v1.ImportIdentityUsersRequest and its rows.
// password_hash is debug_redact in the proto; here it exists only on the wire.
type identityImportUserWire struct {
	ID            string                   `json:"id"`
	Email         string                   `json:"email"`
	EmailVerified bool                     `json:"emailVerified,omitempty"`
	GivenName     string                   `json:"givenName,omitempty"`
	FamilyName    string                   `json:"familyName,omitempty"`
	DisplayName   string                   `json:"displayName,omitempty"`
	PasswordHash  string                   `json:"passwordHash,omitempty"`
	Links         []identityImportLinkWire `json:"links,omitempty"`
	Metadata      map[string]string        `json:"metadata,omitempty"`
	Roles         []string                 `json:"roles,omitempty"`
}

type identityImportLinkWire struct {
	Provider         string `json:"provider"`
	ProviderUserID   string `json:"providerUserId"`
	ProviderUserName string `json:"providerUserName,omitempty"`
}

type identityImportResponseWire struct {
	Results []struct {
		ID      string `json:"id"`
		Outcome string `json:"outcome"`
		Reason  string `json:"reason"`
	} `json:"results"`
}

// cloudIdentityImporter imports through the env's control plane.
type cloudIdentityImporter struct {
	client cloudCaller
	req    map[string]any // orgId, environmentId
	env    string
}

func (c *cloudIdentityImporter) where() string { return "env " + c.env + "'s managed identity" }

// errImportUnsupported is a control plane that does not serve ImportUsers.
var errImportUnsupported = errors.New("this control plane does not support identity import yet (" + procIdentityImport + "); nothing was imported. Rehearse against a host-run identity, or import once the control plane is upgraded")

func (c *cloudIdentityImporter) importBatch(ctx context.Context, users []identity.ImportUser, opts identity.ImportOptions) ([]identity.ImportResult, error) {
	req := make(map[string]any, len(c.req)+4)
	for k, v := range c.req {
		req[k] = v
	}
	rows := make([]identityImportUserWire, 0, len(users))
	for _, u := range users {
		w := identityImportUserWire{
			ID: u.ID, Email: u.Email, EmailVerified: u.EmailVerified,
			GivenName: u.GivenName, FamilyName: u.FamilyName, DisplayName: u.DisplayName,
			PasswordHash: u.PasswordHash, Metadata: u.Metadata, Roles: u.Roles,
		}
		for _, l := range u.Links {
			w.Links = append(w.Links, identityImportLinkWire(l))
		}
		rows = append(rows, w)
	}
	req["users"] = rows
	req["dryRun"] = opts.DryRun
	req["updateExisting"] = opts.UpdateExisting
	req["updatePasswords"] = opts.UpdatePasswords
	var resp identityImportResponseWire
	if err := c.client.Call(ctx, procIdentityImport, req, &resp); err != nil {
		var ce *cloud.Error
		if errors.As(err, &ce) && (ce.HasCode(cloud.CodeUnimplemented) || ce.HTTPStatus == 404) {
			return nil, exitCodeError{code: exitUndetermined, msg: errImportUnsupported.Error()}
		}
		return nil, err
	}
	if len(resp.Results) != len(users) {
		return nil, fmt.Errorf("the control plane answered %d results for %d rows", len(resp.Results), len(users))
	}
	out := make([]identity.ImportResult, len(users))
	for i, r := range resp.Results {
		out[i] = identity.ImportResult{ID: r.ID, Outcome: identity.ImportOutcome(r.Outcome), Reason: r.Reason}
	}
	return out, nil
}

// identityImportRow is one row's place in the file and its result.
type identityImportRow struct {
	Line    int                    `json:"line"`
	ID      string                 `json:"id"`
	Outcome identity.ImportOutcome `json:"outcome"`
	Reason  string                 `json:"reason,omitempty"`
}

type identityImportDocument struct {
	jsonEnvelope
	DryRun    bool                `json:"dryRun"`
	Totals    map[string]int      `json:"totals"`
	Results   []identityImportRow `json:"results"`
	Attempted int                 `json:"attempted"`
	Rows      int                 `json:"rows"`
}

type identityImportOptions struct {
	file   string
	report string
	batch  int
	json   bool
	opts   identity.ImportOptions
}

func newCloudIdentityImportCmd() *cobra.Command {
	var f cloudIdentityFlags
	var o identityImportOptions
	cmd := &cobra.Command{
		Use:   "import <env> --file users.jsonl",
		Short: "Import users from another identity provider, keeping their ids, password hashes, verified addresses and social links (identity:manage)",
		Long: `Create (or converge) users in the environment's managed identity from a file
the project produces from its old identity provider. Each user keeps:

  - the same user id, which becomes the token subject, so every row the app
    keys by user id stays valid;
  - the password hash, so nobody resets a password: bcrypt always, and argon2,
    scrypt or pbkdf2 where the instance verifies them. There is no plaintext
    field;
  - the verified flag of the address;
  - social links (GitHub, Google), by the provider's STABLE user id (GitHub's
    numeric id, Google's sub), so "Sign in with GitHub" lands on the same
    account. The identity must declare the provider, and it must be applied.

Metadata (keys under "forge." are refused) and console-managed roles carry
over too.

FILE. JSONL, one user per line (version 1):

  {"v":1,"id":"4f1c…","email":"ada@acme.com","email_verified":true,
   "given_name":"Ada","family_name":"L","display_name":"Ada",
   "password_hash":"$2a$10$…",
   "links":[{"provider":"github","provider_user_id":"583231","provider_user_name":"ada"}],
   "metadata":{"legacy_plan":"pro"},"roles":["member"]}

or CSV with a header row: id, email, email_verified, given_name, family_name,
display_name, password_hash, roles (separated by ';' or spaces), one
link.<provider> column ("<provider_user_id>[:<name>]"), one meta.<key> column
per key.

IDEMPOTENT BY ID. Re-running a file converges and never duplicates, so a re-run
is how an import that stopped resumes. An existing account is left alone unless
--update-existing (profile, verified flag, links, metadata, roles; nothing is
removed); its password hash is overwritten only with --update-passwords. An
address or a social link another account holds is a conflict, never a merge.
--dry-run validates and resolves every row and writes nothing.

WHERE IT GOES. A host-run identity (forge.IdentityOnHost: the dev IdP of
'forge env up') is imported into directly, so the same file can be rehearsed
locally first. A hosted identity is imported into by its control plane, in
batches of at most 500 rows (--batch); that needs identity:manage, held by a
person, as export does.

OUTPUT. Totals per outcome and every row that needs attention, by line and id
(--json for the document). --report writes {line, id, outcome, reason} per row
as JSONL. Nothing printed or written carries a password hash.

EXIT. 0 when every row is created, updated or unchanged; 3 when any row is a
conflict or invalid; 1 when the instance failed a row (re-run the file).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if o.file == "" {
				return errors.New("--file is required")
			}
			if o.batch < 1 || o.batch > maxIdentityImportBatch {
				return fmt.Errorf("--batch must be 1..%d", maxIdentityImportBatch)
			}
			o.json = f.json
			return runIdentityImport(cmd.Context(), cmd.OutOrStdout(), o, func(ctx context.Context) (identityImporter, error) {
				return identityImportTarget(ctx, args[0], &f)
			})
		},
	}
	f.register(cmd)
	cmd.Flags().StringVar(&o.file, "file", "", "The users to import: JSONL (version 1) or CSV with a header row")
	cmd.Flags().BoolVar(&o.opts.DryRun, "dry-run", false, "Validate and resolve every row; write nothing")
	cmd.Flags().BoolVar(&o.opts.UpdateExisting, "update-existing", false, "Converge accounts that already exist (profile, verified flag, links, metadata, roles)")
	cmd.Flags().BoolVar(&o.opts.UpdatePasswords, "update-passwords", false, "Also overwrite the password hash of accounts that already exist")
	cmd.Flags().StringVar(&o.report, "report", "", "Write {line, id, outcome, reason} per row to this file, as JSONL")
	cmd.Flags().IntVar(&o.batch, "batch", maxIdentityImportBatch, "Rows per import call (1-500)")
	return cmd
}

// runIdentityImport parses the file, imports its readable rows in batches and
// reports. target is resolved only once the file has parsed, so a file that
// cannot be read never reaches an identity.
func runIdentityImport(ctx context.Context, out io.Writer, o identityImportOptions, target func(context.Context) (identityImporter, error)) error {
	fh, err := os.Open(o.file) // #nosec G304 -- the operator's own file
	if err != nil {
		return err
	}
	defer func() { _ = fh.Close() }()

	var rows []identityImportRow
	var users []identity.ImportUser
	var index []int // users[i] is rows[index[i]]
	err = identity.ParseImportFile(fh, func(line int, u identity.ImportUser, perr error) error {
		row := identityImportRow{Line: line, ID: u.ID}
		if perr != nil {
			row.Outcome, row.Reason = identity.ImportInvalid, "unreadable: "+perr.Error()
		} else {
			users = append(users, u)
			index = append(index, len(rows))
		}
		rows = append(rows, row)
		return nil
	})
	if err != nil {
		return fmt.Errorf("read %s: %w", o.file, err)
	}

	attempted := 0
	var runErr error
	where := ""
	if len(users) > 0 {
		imp, err := target(ctx)
		if err != nil {
			return err
		}
		where = imp.where()
		for start := 0; start < len(users); start += o.batch {
			end := min(start+o.batch, len(users))
			res, err := imp.importBatch(ctx, users[start:end], o.opts)
			if err != nil {
				runErr = err
				break
			}
			for i, r := range res {
				row := &rows[index[start+i]]
				row.ID, row.Outcome, row.Reason = r.ID, r.Outcome, r.Reason
			}
			attempted = end
		}
	}

	// Rows a failed call never reached have no outcome; they are not
	// reported as anything.
	done := make([]identityImportRow, 0, len(rows))
	totals := map[string]int{}
	for _, r := range rows {
		if r.Outcome == "" {
			continue
		}
		done = append(done, r)
		totals[string(r.Outcome)]++
	}
	if o.report != "" {
		if err := writeIdentityImportReport(o.report, done); err != nil {
			return errors.Join(runErr, err)
		}
	}

	verdict := runErr
	if verdict == nil {
		switch {
		case totals[string(identity.ImportFailed)] > 0:
			verdict = exitCodeError{code: exitWrong, msg: fmt.Sprintf("%d row(s) failed on the instance; re-run the same file to retry them (it is idempotent)", totals[string(identity.ImportFailed)])}
		case totals[string(identity.ImportConflict)]+totals[string(identity.ImportInvalid)] > 0:
			verdict = exitCodeError{code: exitConflict, msg: fmt.Sprintf("%d row(s) were not imported: %d conflict, %d invalid",
				totals[string(identity.ImportConflict)]+totals[string(identity.ImportInvalid)], totals[string(identity.ImportConflict)], totals[string(identity.ImportInvalid)])}
		}
	} else {
		verdict = fmt.Errorf("identity import stopped after %d of %d rows: %w; re-run the same file to resume (it is idempotent)", attempted, len(users), runErr)
		if code := exitCodeForError(runErr); code != exitWrong {
			verdict = exitCodeError{code: code, msg: verdict.Error()}
		}
	}

	if o.json {
		doc := identityImportDocument{DryRun: o.opts.DryRun, Totals: totals, Results: done, Attempted: attempted, Rows: len(rows)}
		doc.stamp(verdict)
		if err := writeJSONDocument(out, doc); err != nil {
			return err
		}
		return verdict
	}
	writeIdentityImportSummary(out, where, o, rows, done, totals)
	return verdict
}

func writeIdentityImportSummary(out io.Writer, where string, o identityImportOptions, rows, done []identityImportRow, totals map[string]int) {
	mode := "Imported"
	if o.opts.DryRun {
		mode = "Dry run (nothing written) of importing"
	}
	if where == "" {
		where = "the identity"
	}
	fmt.Fprintf(out, "%s %d row(s) of %s into %s.\n", mode, len(done), o.file, where)
	keys := make([]string, 0, len(totals))
	for k := range totals {
		keys = append(keys, k)
	}
	order := map[string]int{"created": 0, "updated": 1, "unchanged": 2, "conflict": 3, "invalid": 4, "failed": 5}
	sort.Slice(keys, func(i, j int) bool { return order[keys[i]] < order[keys[j]] })
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, totals[k]))
	}
	if len(parts) > 0 {
		fmt.Fprintf(out, "  %s\n", strings.Join(parts, ", "))
	}
	if n := len(rows) - len(done); n > 0 {
		fmt.Fprintf(out, "  %d row(s) not attempted\n", n)
	}
	const maxListed = 50
	listed := 0
	for _, r := range done {
		if r.Outcome.OK() {
			continue
		}
		if listed == maxListed {
			fmt.Fprintf(out, "  … and more (--report or --json lists every row)\n")
			break
		}
		id := r.ID
		if id == "" {
			id = "(no id)"
		}
		fmt.Fprintf(out, "  line %d  %s  %s  %s\n", r.Line, id, r.Outcome, r.Reason)
		listed++
	}
	if o.report != "" {
		fmt.Fprintf(out, "Report: %s\n", o.report)
	}
}

func writeIdentityImportReport(path string, rows []identityImportRow) error {
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- the operator's own path
	if err != nil {
		return fmt.Errorf("write the report: %w", err)
	}
	enc := json.NewEncoder(fh)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			_ = fh.Close()
			return fmt.Errorf("write the report: %w", err)
		}
	}
	return fh.Close()
}
