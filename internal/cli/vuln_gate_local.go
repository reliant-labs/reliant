package cli

// `forge vuln gate`: SBOM local build output, scan it, and judge it by a
// vuln_gate, with no release, no ledger and (with --db or --offline) no
// network. It is the SBOM + vulnerability-scan stage of a pipeline gate,
// which runs inside a credential-free boundary with the machine's database
// mounted read-only, and it gives a repo that forge does not deploy the same
// verdict, document and exit codes as one it does.
//
// What it reuses, so nothing about vulnerabilities is decided twice: the
// release cut's SBOM generator (internal/sbom: pinned syft, CycloneDX 1.6, the
// artifact's name as the subject), the shared database's freshness rule and
// grype invocation (vulnscan.DB, vulnscan.Scanner), the vuln_gate rule and
// the forge.dev/vuln-list/v1 document (vulnscan.BuildDocument), and the
// exemption vocabulary of `forge vuln exempt` (an env's KCL) and of
// ci.vuln_scan.exemptions (forge.yaml).
//
// UNSCANNED IS NEVER A PASS. Every input is an artifact of the document. One
// that has no SBOM (syft missing, a tar that holds no image, a path the build
// never produced) or no scan (grype missing, no database, a stale one) is
// listed unscanned with the reason, and the verdict is unscanned or stale:
// exit 2, reason vuln_gate_unscanned. The document is printed (and written to
// --out) on every outcome, so a caller reads the same JSON whatever happened.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/sbom"
	"github.com/reliant-labs/forge/internal/toolfetch"
	"github.com/reliant-labs/forge/internal/vulnscan"
	"github.com/reliant-labs/forge/pkg/release"
)

// vulnGateDocFile is the document's name in --out.
const vulnGateDocFile = "vuln-gate.json"

type vulnGateOptions struct {
	sboms   []string
	db      string
	offline bool
	out     string
	policy  string
	env     string
	asJSON  bool
}

func newVulnGateCmd(deps vulnDeps) *cobra.Command {
	var opts vulnGateOptions
	cmd := &cobra.Command{
		Use:   "gate [<name>=]<path>... [--sbom [<name>=]<file>]... [--db <dir>] [--out <dir>] [--policy <file> | --env <env>] [--json]",
		Short: "SBOM local build output, scan it and judge it by vuln_gate: no release, no ledger, offline with --db",
		Long: `Describe local build output with forge's pinned syft, scan each SBOM with grype,
and judge the findings by a vuln_gate. Nothing is recorded: no release, no
ledger. It is the SBOM + vulnerability-scan stage of a merge gate, and the same
verdict for a repo forge does not deploy.

Each <path> is one artifact, named by its base name or by <name>=:
  a directory       syft's directory scan (lockfiles, manifests, binaries)
  an image tarball  ` + "`docker save`" + ` output, or an OCI layout archive: read from the file
  any other file    a binary or lockfile (Go buildinfo lists every linked module)
--sbom [<name>=]<file> (repeatable) adds an SBOM your build already made
(CycloneDX or SPDX JSON), scanned as given.

The database:
  --db <dir>    a vulnerability database directory (the layout of
                $FORGE_VULN_DB_DIR, which ` + "`forge vuln db pull`" + ` keeps), used read-only
                and offline: nothing is written into it, nothing is fetched, and
                syft and grype must already be installed (` + "`forge tools install syft grype`" + `)
  --offline     this machine's database, with no network read and no install
  (neither)     this machine's database, refreshed when it is a day old, as
                ` + "`forge vuln scan`" + ` does
A database that is missing, or built more than 5 days ago, judges nothing:
the verdict is unscanned or stale.

The policy:
  --env <env>      that env's vuln_gate and its ` + "`forge vuln exempt`" + ` exemptions
  --policy <file>  a vuln_gate object (YAML or JSON: mode, block_severity,
                   warn_severity, block_kev, fixable_only, exemptions); a field
                   it leaves out keeps its default
  (neither)        the default vuln_gate (block KEV-listed and fixable critical
                   advisories, warn at high) with forge.yaml's
                   ci.vuln_scan.exemptions, when there is a forge.yaml

--out <dir> writes each SBOM there and the document as ` + vulnGateDocFile + `; --json
prints the document (forge.dev/vuln-list/v1, ` + "`forge project schema vuln-list`" + `):
its gate block says the verdict (pass, warn, block, unscanned, stale, off), the
reason code, the advisories that block and the exemptions that applied; each
finding names the rule that decided it; sboms lists the files written.

Exit codes: 0 nothing blocks (pass, warn, or a vuln_gate that is off); 1 at
least one blocking finding, in any gate mode; 2 could not determine (an
artifact with no SBOM or no scan, syft or grype missing, the database missing
or stale, an input or policy that cannot be read): never a pass.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVulnGate(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), deps, args, opts)
		},
	}
	cmd.Flags().StringArrayVar(&opts.sboms, "sbom", nil, "an SBOM your build made, `[<name>=]<file>` (CycloneDX or SPDX JSON); repeatable")
	cmd.Flags().StringVar(&opts.db, "db", "", "use this vulnerability database directory, read-only and offline")
	cmd.Flags().BoolVar(&opts.offline, "offline", false, "use this machine's database without any network read or tool install")
	cmd.Flags().StringVar(&opts.out, "out", "", "write each SBOM and the document ("+vulnGateDocFile+") into this directory")
	cmd.Flags().StringVar(&opts.policy, "policy", "", "judge by the vuln_gate in this YAML or JSON file")
	cmd.Flags().StringVar(&opts.env, "env", "", "judge by this env's vuln_gate and exemptions")
	cmd.Flags().BoolVar(&opts.asJSON, "json", false, "print the forge.dev/vuln-list/v1 document")
	return cmd
}

// vulnGateInput is one artifact to judge.
type vulnGateInput struct {
	name string
	path string
	// provided is an SBOM the caller supplied; otherwise path is described.
	provided bool
	kind     sbom.Kind
	// problem is why the input cannot be described (it is then unscanned).
	problem string
	// sbom is its SBOM once there is one.
	sbom      []byte
	format    string
	generator string
	source    string
}

func runVulnGate(ctx context.Context, out, errOut io.Writer, deps vulnDeps, paths []string, opts vulnGateOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(paths) == 0 && len(opts.sboms) == 0 {
		return undeterminedf("nothing to scan: name a directory, an image tarball or a file, or --sbom <file>")
	}
	if opts.policy != "" && opts.env != "" {
		return undeterminedf("--policy and --env name the vuln_gate two ways; pass one")
	}
	offline := opts.offline || opts.db != ""
	now := deps.now()

	inputs, err := vulnGateInputs(paths, opts.sboms)
	if err != nil {
		return err
	}
	gate, gradedAs, notes, err := vulnGatePolicy(ctx, deps, opts)
	if err != nil {
		return err
	}

	describeInputs(ctx, deps, inputs, offline)
	var files []vulnscan.SBOMFile
	if opts.out != "" {
		if files, err = writeGateSBOMs(opts.out, inputs); err != nil {
			return undeterminedf("--out %s: %v", opts.out, err)
		}
	}

	scans, st, cleanup, dbErr := scanGateInputs(ctx, deps, errOut, inputs, opts, offline)
	defer cleanup()
	if dbErr != nil {
		notes = append(notes, "not scanned: "+vulnGateProblem(dbErr, offline))
	}
	stale := errors.Is(dbErr, vulnscan.ErrStale)

	arts := make([]vulnscan.ArtifactScan, 0, len(inputs))
	for i, in := range inputs {
		a := vulnscan.ArtifactScan{Artifact: in.name, Latest: scans[i].scan, Reason: scans[i].reason}
		if in.sbom != nil {
			a.SBOMDigest = sbom.Digest(in.sbom)
		}
		arts = append(arts, a)
	}
	doc := vulnscan.BuildDocument(vulnscan.DocumentInput{
		Env: opts.env, Gate: gate, GradedAs: gradedAs, Artifacts: arts, Now: now, Notes: notes, Stale: stale,
	})
	if stale && doc.Coverage.Scanned == 0 {
		// Nothing was scanned because the database is stale: say stale,
		// the more precise of the two undetermined verdicts. BuildDocument
		// dates the database from scans, and there are none.
		doc.DB.Stale = true
		if st.Present {
			doc.DB.BuiltAt, doc.DB.Schema = st.BuiltAt, st.Schema
		}
		if doc.Gate.Verdict == vulnscan.VerdictUnscanned {
			doc.Gate.Verdict = vulnscan.VerdictStale
		}
	}
	doc.SBOMs = files

	if opts.out != "" {
		if err := writeVulnGateDocument(opts.out, doc); err != nil {
			return undeterminedf("--out %s: %v", opts.out, err)
		}
	}
	if opts.asJSON {
		if err := writeVulnJSON(out, doc); err != nil {
			return err
		}
	} else {
		label := fmt.Sprintf("%d artifacts", len(inputs))
		if len(inputs) == 1 {
			label = inputs[0].name
		}
		writeVulnDocument(out, doc, now, label)
		for _, f := range files {
			_, _ = fmt.Fprintf(out, "sbom: %s (%s)\n", filepath.Join(opts.out, f.Path), f.Artifact)
		}
	}
	return vulnGateExit(doc)
}

// vulnGateExit is the exit code of a vuln gate document: 0 nothing blocks,
// 1 a blocking finding, 2 could not determine.
func vulnGateExit(doc vulnscan.Document) error {
	g := doc.Gate
	switch {
	case g.Verdict == vulnscan.VerdictBlock:
		return &exitCodeError{code: exitWrong, verdict: true, msg: fmt.Sprintf("vuln_gate (%s) blocks: %s", g.Mode, describeBlocking(doc, 5))}
	case !g.Determined:
		msg := "could not determine (" + string(g.Verdict) + ")"
		var names []string
		for _, u := range doc.Coverage.Unscanned {
			names = append(names, u.Artifact+" ("+u.Reason+")")
		}
		if len(names) > 0 {
			msg += ": not scanned: " + strings.Join(names, ", ")
		}
		if len(g.Blocking) > 0 {
			msg += "; the old data would block: " + strings.Join(g.Blocking, ", ")
		}
		return &exitCodeError{code: exitUndetermined, verdict: true, msg: msg}
	}
	return nil
}

// splitNamed splits `<name>=<path>`. A path with no `=`, or whose `=` comes
// after a path separator, is a bare path named by its base name.
func splitNamed(arg string) (name, path string) {
	if i := strings.Index(arg, "="); i > 0 && !strings.ContainsAny(arg[:i], `/\`) {
		return arg[:i], arg[i+1:]
	}
	path = arg
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return filepath.Base(abs), path
}

// vulnGateInputs classifies each argument. A path that cannot be read is an
// input with a problem (unscanned), not an error: the document still covers
// it. Only a name used twice is refused, since the findings could not say
// which artifact they are in.
func vulnGateInputs(paths, sboms []string) ([]*vulnGateInput, error) {
	var inputs []*vulnGateInput
	seen := map[string]bool{}
	add := func(in *vulnGateInput) error {
		if in.name == "" || in.path == "" {
			return undeterminedf("an empty artifact name or path in %q", in.name+"="+in.path)
		}
		if seen[in.name] {
			return undeterminedf("two inputs are named %q: name one with <name>=<path>", in.name)
		}
		seen[in.name] = true
		inputs = append(inputs, in)
		return nil
	}
	for _, arg := range paths {
		name, path := splitNamed(arg)
		in := &vulnGateInput{name: name, path: path}
		classifyGatePath(in)
		if err := add(in); err != nil {
			return nil, err
		}
	}
	for _, arg := range sboms {
		name, path := splitNamed(arg)
		in := &vulnGateInput{name: name, path: path, provided: true, source: release.SBOMSourceCIProvided}
		data, err := os.ReadFile(path) //nolint:gosec // the caller names its own build's SBOM
		switch {
		case err != nil:
			in.problem = fmt.Sprintf("read the SBOM: %v", err)
		default:
			doc, ierr := sbom.Identify(data)
			if ierr != nil {
				in.problem = fmt.Sprintf("%s is not an SBOM: %v", path, ierr)
			} else {
				in.sbom, in.format, in.generator = data, doc.Format, doc.Generator
			}
		}
		if err := add(in); err != nil {
			return nil, err
		}
	}
	return inputs, nil
}

// classifyGatePath sets the SBOM kind syft reads path as.
func classifyGatePath(in *vulnGateInput) {
	fi, err := os.Stat(in.path)
	switch {
	case err != nil:
		in.problem = fmt.Sprintf("no such input: %v", err)
		return
	case fi.IsDir():
		in.kind = sbom.KindDir
		return
	case !fi.Mode().IsRegular():
		in.problem = fmt.Sprintf("%s is not a regular file or a directory", in.path)
		return
	}
	head, err := readHead(in.path, 512)
	if err != nil {
		in.problem = err.Error()
		return
	}
	switch {
	case sbom.IsTar(head):
		in.kind = sbom.KindArchive
	case sbom.IsGzip(head):
		in.problem = fmt.Sprintf("%s is gzip-compressed: pass the uncompressed tar `docker save` writes, or a directory", in.path)
	default:
		in.kind = sbom.KindBinary
	}
}

func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // the caller names its own build output
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, n)
	m, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:m], nil
}

// describeInputs generates the SBOM of every input that is not one, with the
// release cut's generator. Offline, syft must already be installed.
func describeInputs(ctx context.Context, deps vulnDeps, inputs []*vulnGateInput, offline bool) {
	var targets []sbom.Target
	var idx []int
	for i, in := range inputs {
		if in.provided || in.problem != "" {
			continue
		}
		abs, err := filepath.Abs(in.path)
		if err != nil {
			abs = in.path
		}
		targets = append(targets, sbom.Target{Name: in.name, Kind: in.kind, Ref: abs})
		idx = append(idx, i)
	}
	if len(targets) == 0 {
		return
	}
	gen := &sbom.Generator{
		Tool: func(ctx context.Context) (string, string, error) {
			path, version, err := deps.tool(ctx, toolfetch.Syft, !offline)
			if err != nil && offline {
				err = fmt.Errorf("%w; offline, forge installs nothing: run `forge tools install syft grype` where there is network", err)
			}
			return path, version, err
		},
		Runner:      deps.sbomRunner,
		Timeout:     sbom.DefaultTimeout,
		Concurrency: sbom.DefaultConcurrency,
	}
	if deps.sbomCache != nil {
		if dir := deps.sbomCache(); dir != "" {
			gen.Cache = &sbom.Cache{Dir: dir}
		}
	}
	for j, res := range gen.Generate(ctx, targets) {
		in := inputs[idx[j]]
		in.source = res.Ref.Source
		if !res.Ref.HasSBOM() {
			in.problem = "no SBOM: " + res.Ref.Reason
			continue
		}
		in.sbom, in.format, in.generator = res.Bytes, res.Ref.Format, res.Ref.Generator
	}
}

type gateScan struct {
	scan   *vulnscan.Scan
	reason string
}

// scanGateInputs scans every SBOM. dbErr is why nothing could be scanned (no
// grype, no database, a stale one); each input's reason says why it was not.
func scanGateInputs(ctx context.Context, deps vulnDeps, errOut io.Writer, inputs []*vulnGateInput, opts vulnGateOptions, offline bool) (scans []gateScan, st vulnscan.Status, cleanup func(), dbErr error) {
	scans = make([]gateScan, len(inputs))
	cleanup = func() {}
	for i, in := range inputs {
		if in.problem != "" {
			scans[i].reason = in.problem
		}
	}
	db, err := deps.openDB(errOut, !offline)
	if err == nil && opts.db != "" {
		db.Dir, err = filepath.Abs(opts.db)
		if err == nil {
			var cfg string
			if cfg, err = os.MkdirTemp("", "forge-vuln-gate-"); err == nil {
				cleanup = func() { _ = os.RemoveAll(cfg) }
				db.ReadOnly, db.ConfigDir = true, cfg
			}
		}
	}
	if err == nil {
		db.Offline = offline
		st, err = db.Ensure(ctx, vulnscan.EnsureOptions{Fetch: !offline})
	}
	if err != nil {
		reason := "not scanned: " + vulnGateProblem(err, offline)
		for i := range scans {
			if scans[i].reason == "" {
				scans[i].reason = reason
			}
		}
		return scans, st, cleanup, err
	}
	scanner := &vulnscan.Scanner{DB: db}
	for i, in := range inputs {
		if in.sbom == nil {
			continue
		}
		s, err := scanner.ScanSBOM(ctx, in.sbom)
		if err != nil {
			scans[i].reason = "not scanned: " + vulnGateProblem(err, offline)
			continue
		}
		scans[i].scan = s
	}
	return scans, st, cleanup, nil
}

// vulnGateProblem is one line for a database or scanner failure.
func vulnGateProblem(err error, offline bool) string {
	msg := err.Error()
	if offline && errors.Is(err, vulnscan.ErrScannerMissing) {
		msg += "; offline, forge installs nothing: run `forge tools install syft grype` where there is network"
	}
	return msg
}

// vulnGatePolicy resolves the vuln_gate the findings are judged by.
func vulnGatePolicy(ctx context.Context, deps vulnDeps, opts vulnGateOptions) (release.ShapeVulnGate, release.DeliveryMode, []string, error) {
	switch {
	case opts.policy != "":
		g, notes, err := readVulnGatePolicy(opts.policy)
		return g, "", notes, err
	case opts.env != "":
		env, err := deps.resolveEnv(ctx, deps.projectDir(), opts.env)
		if err != nil {
			return release.ShapeVulnGate{}, "", nil, undeterminedf("env %q: %v", opts.env, err)
		}
		var notes []string
		if env.GradeNote != "" {
			notes = append(notes, env.GradeNote)
		}
		if env.hosted() {
			notes = append(notes, fmt.Sprintf("env %q records on %s, which judges its promotions; this verdict is the same rule over local build output", opts.env, env.Endpoint))
		}
		return env.Gate, env.GradedAs, notes, nil
	}
	gate := release.DefaultDeliveryPolicy().VulnGate
	var notes []string
	if cfg, err := config.LoadProjectDir(deps.projectDir()); err == nil && cfg != nil {
		accepted, malformed := sbomScanExemptions(cfg.CI.VulnScan.Exemptions)
		gate.Exemptions = accepted
		for _, m := range malformed {
			notes = append(notes, "exemption not honored: "+m)
		}
	}
	return gate, "", notes, nil
}

// vulnGatePolicySeverities are the thresholds a vuln_gate accepts (the
// schema's).
var vulnGatePolicySeverities = map[string]bool{"none": true, "low": true, "medium": true, "high": true, "critical": true}

// readVulnGatePolicy reads a vuln_gate object from a YAML or JSON file, over
// the defaults: a field the file leaves out keeps its default.
func readVulnGatePolicy(path string) (release.ShapeVulnGate, []string, error) {
	gate := release.DefaultDeliveryPolicy().VulnGate
	data, err := os.ReadFile(path) //nolint:gosec // the caller names its policy file
	if err != nil {
		return gate, nil, undeterminedf("--policy: %v", err)
	}
	if err := yaml.UnmarshalStrict(data, &gate); err != nil {
		return gate, nil, undeterminedf("--policy %s is not a vuln_gate (mode, block_severity, warn_severity, block_kev, fixable_only, exemptions, scoped_exemptions): %v", path, err)
	}
	switch {
	case gate.Mode == "":
		gate.Mode = release.DeliveryAudit
	case !gate.Mode.Valid():
		return gate, nil, undeterminedf("--policy %s: mode %q (expected off, audit or enforce)", path, gate.Mode)
	}
	for field, v := range map[string]string{"block_severity": gate.BlockSeverity, "warn_severity": gate.WarnSeverity} {
		if !vulnGatePolicySeverities[strings.ToLower(v)] {
			return gate, nil, undeterminedf("--policy %s: %s %q (expected none, low, medium, high or critical)", path, field, v)
		}
	}
	entries := make([]config.CIVulnExemption, 0, len(gate.Exemptions))
	for _, x := range gate.Exemptions {
		entries = append(entries, config.CIVulnExemption{ID: x.ID, Reason: x.Reason, Expires: x.Expires})
	}
	accepted, malformed := sbomScanExemptions(entries)
	gate.Exemptions = accepted
	// A scoped exemption (scoped_exemptions, release.ShapeVulnScopedExempt)
	// is checked the same way, and keeps its scope.
	scoped := gate.ScopedExemptions[:0]
	for _, x := range gate.ScopedExemptions {
		ok, bad := sbomScanExemptions([]config.CIVulnExemption{{ID: x.ID, Reason: x.Reason, Expires: x.Expires}})
		malformed = append(malformed, bad...)
		if len(ok) == 1 {
			x.ID, x.Reason, x.Expires = ok[0].ID, ok[0].Reason, ok[0].Expires
			scoped = append(scoped, x)
		}
	}
	gate.ScopedExemptions = scoped
	var notes []string
	for _, m := range malformed {
		notes = append(notes, "exemption not honored: "+m)
	}
	return gate, notes, nil
}

var gateFileNameRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// writeGateSBOMs writes each input's SBOM into dir, one file per artifact.
func writeGateSBOMs(dir string, inputs []*vulnGateInput) ([]vulnscan.SBOMFile, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var files []vulnscan.SBOMFile
	used := map[string]bool{vulnGateDocFile: true}
	for _, in := range inputs {
		if in.sbom == nil {
			continue
		}
		ext := ".cdx.json"
		if strings.HasPrefix(in.format, "spdx") {
			ext = ".spdx.json"
		}
		base := strings.Trim(gateFileNameRE.ReplaceAllString(in.name, "_"), "._")
		if base == "" {
			base = "artifact"
		}
		name := base + ext
		for n := 2; used[name]; n++ {
			name = fmt.Sprintf("%s-%d%s", base, n, ext)
		}
		used[name] = true
		if err := atomicfile.WriteFile(filepath.Join(dir, name), in.sbom, 0o644); err != nil {
			return nil, err
		}
		files = append(files, vulnscan.SBOMFile{
			Artifact: in.name, Path: name, Digest: sbom.Digest(in.sbom),
			Format: in.format, Generator: in.generator, Source: in.source,
		})
	}
	return files, nil
}

func writeVulnGateDocument(dir string, doc vulnscan.Document) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var buf strings.Builder
	if err := writeVulnJSON(&buf, doc); err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(dir, vulnGateDocFile), []byte(buf.String()), 0o644)
}
