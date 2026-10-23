package cli

// ImportLedger (doc §6.3, §11): the one-time migration of a project's git
// file ledger into its control plane, in ONE transaction.
//
// EVERY ITEM IS A TYPED MESSAGE, not a Struct. The import is the one path
// that writes HISTORY — rows whose created_at predates the import, into
// append-only tables protected by a reject-update trigger — so there is no
// second chance to fix a field that was mis-spelled on the way in. A typed
// message makes a missing field a compile-time or decode-time failure on the
// server; a Struct would make it a silently absent column in an immutable row.
//
// IDEMPOTENT ON `imported_from`. Each item carries the git path or id it came
// from, so a re-run inserts nothing new. That is what makes the import safe to
// retry after a partial failure, and it is why every list below carries the
// field rather than the request carrying one value for all of them: two runs
// over overlapping ranges must dedupe per RECORD.
//
// WHAT IT REFUSES, and why forge does not pre-check it: any env of kind
// persistent or preview (whose history was always hosted), and any env that
// already holds a non-imported promotion (where ordering would be ambiguous).
// Both are judged inside the transaction, against rows that could change under
// a client-side read.
//
// `promoted_by_actor` on an imported promotion is a HISTORICAL NAME — the
// string the old ledger recorded — and is provenance only. It is never read as
// identity: identity comes from the credential, and the credential that
// performed a 2025 promote is not the one running the import (O-11).

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

const procImportLedger = "controlplane.v1.DeployService/ImportLedger"

// ledgerImport is everything one import writes, in forge's own vocabulary.
// The caller assembles it by reading the git ledger with the Add* methods
// below; this file's only job is to render it as the request and read the
// counts back.
//
// THE ITEMS ARE ADDED THROUGH METHODS, NOT SET AS FIELDS, and the reason is
// `imported_from`. Every item needs it — it is the per-record dedupe key that
// makes the whole import idempotent and therefore safe to retry after a
// partial failure — and an item that reached the server without one would be
// re-inserted by the next run, duplicating history in append-only tables that
// have no UPDATE to fix it with. A struct literal lets a caller omit the
// field; a method signature does not. So each Add* takes it as a required
// parameter and the slices stay unexported.
type ledgerImport struct {
	// project scopes every item. Env identity is (org, project, name), so
	// an import without it would land its envs in whichever project the
	// server guessed.
	project string
	// source names what was imported FROM, for the audit row: a git rev, a
	// path. Distinct from each item's importedFrom, which is per record.
	source string
	// dryRun returns the plan without writing. The import is one
	// transaction over append-only tables, so "what would this do" cannot
	// be answered by running it and looking.
	dryRun bool

	releases     []importRelease
	environments []importEnvironment
	promotions   []importPromotion
	bundles      []importBundle
	applies      []importApply
}

// newLedgerImport starts an import of one project from one source.
func newLedgerImport(project, source string, dryRun bool) *ledgerImport {
	return &ledgerImport{project: project, source: source, dryRun: dryRun}
}

// AddRelease queues one historical release. importedFrom is the git path or
// object it was read from.
func (li *ledgerImport) AddRelease(r release.Release, importedFrom string) {
	li.releases = append(li.releases, importRelease{Release: r, ImportedFrom: importedFrom})
}

// AddEnvironment queues one historical env.
//
// deletedAt is carried because a RETIRED env is part of the history: its
// promotions reference it, and importing those against an env that reads as
// live would make a deleted env look current in Live.
func (li *ledgerImport) AddEnvironment(name string, kind release.EnvKind, deletedAt *time.Time, importedFrom string) {
	li.environments = append(li.environments, importEnvironment{
		Name: name, Kind: kind, DeletedAt: deletedAt, ImportedFrom: importedFrom,
	})
}

// AddPromotion queues one historical promotion.
//
// promotedAt is an EXPLICIT parameter rather than taken from the clock: the
// whole point of an import is that these events already happened, and the
// server records the time as given.
func (li *ledgerImport) AddPromotion(p release.Promotion, promotedAt time.Time, importedFrom string) {
	li.promotions = append(li.promotions, importPromotion{
		Promotion: p, PromotedAt: promotedAt, ImportedFrom: importedFrom,
	})
}

// AddBundle queues one historical bundle.
func (li *ledgerImport) AddBundle(b release.BundleRecord, importedFrom string) {
	li.bundles = append(li.bundles, importBundle{Bundle: b, ImportedFrom: importedFrom})
}

// AddApply queues one historical apply, naming its bundle by DIGEST: the
// import is assigning the ids, so the client has none to give, and the digest
// is what the file ledger recorded.
func (li *ledgerImport) AddApply(env, bundleDigest string, outcome *release.ApplyOutcome, createdAt time.Time, importedFrom string) {
	li.applies = append(li.applies, importApply{
		Env: env, BundleDigest: bundleDigest, Outcome: outcome,
		CreatedAt: createdAt, ImportedFrom: importedFrom,
	})
}

// Empty reports whether the import would write nothing — the answer `forge
// ledger import` needs before claiming it migrated anything.
func (li *ledgerImport) Empty() bool {
	return len(li.releases)+len(li.environments)+len(li.promotions)+len(li.bundles)+len(li.applies) == 0
}

// importRelease is one historical release (LedgerImportRelease).
type importRelease struct {
	Release release.Release
	// ImportedFrom is the dedupe key: the git path or object this release
	// was read from.
	ImportedFrom string
}

// importEnvironment is one historical env (LedgerImportEnvironment).
//
// It carries DeletedAt because an env that was retired is part of the
// history: its promotions reference it, and importing them against an env
// that reads as live would make a deleted env look current in Live.
type importEnvironment struct {
	Name         string
	Kind         release.EnvKind
	DeletedAt    *time.Time
	ImportedFrom string
}

// importPromotion is one historical promotion (LedgerImportPromotion).
type importPromotion struct {
	Promotion release.Promotion
	// PromotedAt is EXPLICIT rather than taken as "now": the whole point of
	// an import is that these events already happened. The server records
	// it as given.
	PromotedAt   time.Time
	ImportedFrom string
}

// importBundle is one historical bundle (LedgerImportBundle).
//
// Unlike RecordBundle, this carries a DESCRIPTION — digest, shape, provenance
// — and no bytes. That is deliberate and is the only place the "bytes, never a
// description" rule bends: a historical bundle's blob may be long gone from
// any registry, and refusing to import the record because the artifact expired
// would lose the history to protect a verification that is no longer possible.
// The record is labelled as imported, which is how a reader knows its shape
// was never verified against bytes.
type importBundle struct {
	Bundle       release.BundleRecord
	ImportedFrom string
}

// importApply is one historical apply (LedgerImportApply).
//
// It names its bundle by DIGEST rather than id: the import is assigning the
// ids, so the client has none to give, and the digest is what the file ledger
// recorded.
type importApply struct {
	Env          string
	BundleDigest string
	Outcome      *release.ApplyOutcome
	CreatedAt    time.Time
	ImportedFrom string
}

// ledgerImportResult is what the server reports.
type ledgerImportResult struct {
	// Counts is per-table ("releases": 12, "promotions": 40), read from the
	// response's google.protobuf.Struct. A map rather than named fields,
	// because the server owns which tables it counted and a typed mirror
	// here would have to be edited every time one is added — for data
	// nothing branches on.
	Counts map[string]int
	// Conflicts are the items the import declined, one human line each. A
	// non-empty list with no error is the NORMAL outcome of a partial
	// import: the transaction committed what it could name unambiguously.
	Conflicts []string
}

// hostedImportClient imports a git ledger into one control plane.
type hostedImportClient struct {
	client cloudCaller
}

// ImportLedger performs (or, with DryRun, plans) the import.
func (c hostedImportClient) ImportLedger(ctx context.Context, in *ledgerImport) (ledgerImportResult, error) {
	req := map[string]any{"project": in.project}
	if in.source != "" {
		req["source"] = in.source
	}
	if in.dryRun {
		req["dryRun"] = true
	}

	if rows := importReleaseRows(in.releases); len(rows) > 0 {
		req["releases"] = rows
	}
	if rows := importEnvironmentRows(in.environments); len(rows) > 0 {
		req["environments"] = rows
	}
	promotions, err := importPromotionRows(in.promotions)
	if err != nil {
		return ledgerImportResult{}, err
	}
	if len(promotions) > 0 {
		req["promotions"] = promotions
	}
	bundles, err := importBundleRows(in.bundles)
	if err != nil {
		return ledgerImportResult{}, err
	}
	if len(bundles) > 0 {
		req["bundles"] = bundles
	}
	if rows := importApplyRows(in.applies); len(rows) > 0 {
		req["applies"] = rows
	}

	var resp struct {
		Counts    map[string]any `json:"counts"`
		Conflicts []string       `json:"conflicts"`
	}
	if err := c.client.Call(ctx, procImportLedger, req, &resp); err != nil {
		return ledgerImportResult{}, bundlesUnsupported(err)
	}
	out := ledgerImportResult{Conflicts: resp.Conflicts}
	if len(resp.Counts) > 0 {
		out.Counts = map[string]int{}
		for table, v := range resp.Counts {
			// A Struct's numbers arrive as float64. Anything that is not a
			// number is dropped rather than erroring: the counts are a
			// report, and a server that added a string note to them must
			// not break an import that already committed.
			if n, ok := v.(float64); ok {
				out.Counts[table] = int(n)
			}
		}
	}
	return out, nil
}

func importReleaseRows(in []importRelease) []map[string]any {
	rows := make([]map[string]any, 0, len(in))
	for _, r := range in {
		row := map[string]any{
			"version":   r.Release.Version,
			"artifacts": releaseToWire(r.Release),
			"createdAt": r.Release.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if r.Release.Provenance != nil {
			row["provenance"] = deploytarget.ProvenanceWireFields(*r.Release.Provenance)
		}
		if r.Release.Commits != nil {
			row["commits"] = commitRangeToWire(*r.Release.Commits)
		}
		if r.ImportedFrom != "" {
			row["importedFrom"] = r.ImportedFrom
		}
		rows = append(rows, row)
	}
	return rows
}

func importEnvironmentRows(in []importEnvironment) []map[string]any {
	rows := make([]map[string]any, 0, len(in))
	for _, e := range in {
		row := map[string]any{"name": e.Name, "kind": envKindToWire(e.Kind)}
		if e.DeletedAt != nil {
			row["deletedAt"] = e.DeletedAt.UTC().Format(time.RFC3339Nano)
		}
		if e.ImportedFrom != "" {
			row["importedFrom"] = e.ImportedFrom
		}
		rows = append(rows, row)
	}
	return rows
}

func importPromotionRows(in []importPromotion) ([]map[string]any, error) {
	rows := make([]map[string]any, 0, len(in))
	for _, p := range in {
		row := map[string]any{
			"environment":    p.Promotion.Env,
			"releaseVersion": p.Promotion.Release,
			"promotedAt":     p.PromotedAt.UTC().Format(time.RFC3339Nano),
		}
		// THE FILE'S ID, PRESERVED. The control plane refuses a promotion
		// without it: forge's "N promotions never imported" check compares
		// these ids against the hosted ones, so a minted id would make the
		// imported history look un-imported forever.
		if p.Promotion.ID != "" {
			row["id"] = p.Promotion.ID
		}
		if len(p.Promotion.Resolved) > 0 {
			row["resolvedArtifacts"] = p.Promotion.Resolved
		}
		if len(p.Promotion.Sources) > 0 {
			sources := map[string]any{}
			for name, src := range p.Promotion.Sources {
				sources[name] = wireSource(src)
			}
			row["resolvedSources"] = sources
		}
		if p.Promotion.FromEnv != "" {
			row["fromEnvironment"] = p.Promotion.FromEnv
		}
		if len(p.Promotion.Targets) > 0 {
			row["targets"] = p.Promotion.Targets
		}
		if len(p.Promotion.Gates) > 0 {
			// The STRICT write path, as a live promote uses: a gate whose
			// status only survived a READ because it was mapped cannot be
			// written back, even as history.
			gates, err := gatesToWire(p.Promotion.Gates)
			if err != nil {
				return nil, fmt.Errorf("import promotion of %s to %s: %w", p.Promotion.Release, p.Promotion.Env, err)
			}
			row["gates"] = gates
		}
		if p.Promotion.Note != "" {
			row["note"] = p.Promotion.Note
		}
		// The HISTORICAL name, provenance only. Both halves of the old
		// Actor collapse into it: the control plane's promoted_by_user_id
		// is a real user reference it resolves itself, and a 2025 ledger's
		// user string is not one.
		if actor := historicalActor(p.Promotion.PromotedBy); actor != "" {
			row["promotedByActor"] = actor
		}
		if p.ImportedFrom != "" {
			row["importedFrom"] = p.ImportedFrom
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// historicalActor is the one string an imported promotion records as "who did
// this, as the old ledger said". The actor is preferred; a bare user string
// from a file ledger is carried in the same field rather than as
// promoted_by_user_id, because that column references a real account and an
// imported name is not one.
func historicalActor(a release.Actor) string {
	if a.Actor != "" {
		return a.Actor
	}
	return a.User
}

func importBundleRows(in []importBundle) ([]map[string]any, error) {
	rows := make([]map[string]any, 0, len(in))
	for _, b := range in {
		shape, err := b.Bundle.Shape.Encode()
		if err != nil {
			return nil, fmt.Errorf("import bundle %s: %w", b.Bundle.Digest, err)
		}
		row := map[string]any{
			"environment":  b.Bundle.Env,
			"digest":       b.Bundle.Digest,
			"reference":    b.Bundle.Reference,
			"configDigest": b.Bundle.ConfigDigest,
			// A google.protobuf.Struct carries release.Shape's canonical
			// JSON verbatim — snake_case keys — so it is decoded from the
			// encoded bytes rather than built field by field. Encode also
			// enforces the 256 KiB bound, which the server's CHECK is the
			// second line of.
			"shape":     rawJSONObject(shape),
			"createdAt": b.Bundle.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if b.Bundle.Release != "" {
			row["releaseVersion"] = b.Bundle.Release
		}
		row["provenance"] = deploytarget.ProvenanceWireFields(b.Bundle.Provenance)
		if b.ImportedFrom != "" {
			row["importedFrom"] = b.ImportedFrom
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// applyWorkloadsStruct renders an imported outcome's per-resource results as
// the generic object a google.protobuf.Struct holds. It goes through release's
// own JSON so there is exactly one spelling of ApplyWorkload on the wire.
//
// It lives beside its one caller, the IMPORT, which is now the only thing in
// forge that encodes an apply outcome. The four RPCs that used to were deleted
// as dead code (binding_store.go explains), so this encoder would otherwise
// have no home. An import carries records a PREVIOUS forge wrote, as history,
// and the server labels them with the importing credential precisely because
// nobody can attest to who observed a deploy from last year.
func applyWorkloadsStruct(in []release.ApplyWorkload) (map[string]any, error) {
	raw, err := json.Marshal(map[string]any{"workloads": in})
	if err != nil {
		return nil, fmt.Errorf("apply outcome workloads: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("apply outcome workloads: %w", err)
	}
	return out, nil
}

func importApplyRows(in []importApply) []map[string]any {
	rows := make([]map[string]any, 0, len(in))
	for _, a := range in {
		row := map[string]any{
			"environment":  a.Env,
			"bundleDigest": a.BundleDigest,
			"createdAt":    a.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if o := a.Outcome; o != nil {
			outcome := map[string]any{
				"status":     string(o.Status),
				"finishedAt": o.FinishedAt.UTC().Format(time.RFC3339Nano),
			}
			if o.Summary != "" {
				outcome["summary"] = o.Summary
			}
			if len(o.Workloads) > 0 {
				if workloads, err := applyWorkloadsStruct(o.Workloads); err == nil {
					outcome["workloads"] = workloads
				}
			}
			// reportedBy is not sent, here either: the server sets it, and
			// for an import it sets it to the importing credential — which
			// is honest, since nobody can attest to who observed a 2025
			// apply.
			row["outcome"] = outcome
		}
		if a.ImportedFrom != "" {
			row["importedFrom"] = a.ImportedFrom
		}
		rows = append(rows, row)
	}
	return rows
}

// envKindToWire is the WRITE direction of envKindFromWire.
func envKindToWire(k release.EnvKind) string {
	switch k {
	case release.EnvPersistent:
		return string(deployEnvKindPersistent)
	case release.EnvPreview:
		return string(deployEnvKindPreview)
	case release.EnvSelfManaged:
		return string(deployEnvKindSelfManaged)
	case release.EnvLocal:
		return string(deployEnvKindLocal)
	default:
		// An unspecified kind is what the server refuses, which is the
		// correct outcome: forge derives every kind from a render, so an
		// invalid one here means the caller assembled the import wrong,
		// and that must not be imported as a guess.
		return "DEPLOY_ENVIRONMENT_KIND_UNSPECIFIED"
	}
}

// rawJSONObject re-reads canonical JSON into the generic map a
// google.protobuf.Struct is encoded from.
func rawJSONObject(raw []byte) map[string]any {
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}
