package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/vulnscan"
	"github.com/reliant-labs/forge/pkg/release"
)

type vulnScanOptions struct {
	image    string
	release  string
	deployed string
	asJSON   bool
}

func newVulnScanCmd(deps vulnDeps) *cobra.Command {
	var opts vulnScanOptions
	cmd := &cobra.Command{
		Use:   "scan [<dir> | <sbom-file> | --image <ref@digest> | --release <v> | --deployed <env>]",
		Short: "Scan a directory, an image, a release's SBOMs, or what an env runs, for vulnerabilities",
		Long: `Scan for known vulnerabilities with grype, against this machine's shared
vulnerability database (refreshed first when it is more than a day old: the first
download is ~200 MB and says so).

  <dir>             a source tree or build output (default: the current directory)
  <file>            an SBOM another tool produced (CycloneDX or SPDX JSON)
  --image <ref@d>   an image, read from its registry by digest
  --release <v>     every SBOM release <v> recorded in this machine's ledger;
                    each scan is stored there (` + "`forge vuln list`" + ` reads it)
  --deployed <env>  the release env's ledger says it runs, judged by env's
                    vuln_gate (a nightly CI job's command)

A directory, image or --release scan is judged by the default vuln_gate (block
KEV and fixable critical, warn high); --deployed by the env's own.

--deployed reads the release the env's ledger records as deployed, not the
digests running in its cluster; a drift between the two is ` + "`forge env status`" + `'s.

Exit codes: 0 nothing blocking; 1 a blocking finding; 2 could not determine
(no SBOM, scanner missing, database stale).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := ""
			if len(args) == 1 {
				dir = args[0]
			}
			return runVulnScan(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), deps, dir, opts)
		},
	}
	cmd.Flags().StringVar(&opts.image, "image", "", "scan this image (`ref@sha256:…`), read from its registry")
	cmd.Flags().StringVar(&opts.release, "release", "", "scan every SBOM this release recorded in the machine ledger")
	cmd.Flags().StringVar(&opts.deployed, "deployed", "", "scan the release this env's ledger says it runs, judged by the env's vuln_gate")
	cmd.Flags().BoolVar(&opts.asJSON, "json", false, "print the forge.dev/vuln-list/v1 document (forge project schema vuln-list)")
	return cmd
}

func runVulnScan(ctx context.Context, out, errOut io.Writer, deps vulnDeps, dir string, opts vulnScanOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	set := 0
	for _, s := range []string{dir, opts.image, opts.release, opts.deployed} {
		if s != "" {
			set++
		}
	}
	if set > 1 {
		return fmt.Errorf("name one thing to scan: a directory, --image, --release or --deployed")
	}
	if opts.image != "" && !strings.Contains(opts.image, "@sha256:") {
		return fmt.Errorf("--image %q names no digest: scan an image by digest (ref@sha256:…), so the result describes bytes, not a tag that moves", opts.image)
	}

	projectDir := deps.projectDir()
	env := vulnEnv{Gate: release.DefaultDeliveryPolicy().VulnGate}
	if opts.deployed != "" {
		var err error
		if env, err = deps.resolveEnv(ctx, projectDir, opts.deployed); err != nil {
			return undeterminedf("env %q: %v", opts.deployed, err)
		}
		if env.hosted() {
			return hostedVulnError(env)
		}
	}

	db, err := deps.openDB(errOut, true)
	if err != nil {
		return dbExit(err)
	}
	if _, err := db.Ensure(ctx, vulnscan.EnsureOptions{Fetch: true}); err != nil {
		return dbExit(err)
	}
	scanner := &vulnscan.Scanner{DB: db}
	now := deps.now()

	var in vulnscan.DocumentInput
	var notes []string
	switch {
	case opts.release != "" || opts.deployed != "":
		store, err := deps.machineLedger(projectDir)
		if err != nil {
			return undeterminedf("open this machine's ledger: %v", err)
		}
		rel, err := releaseOf(store, opts.deployed, opts.release)
		if err != nil {
			return err
		}
		local, err := db.Status(ctx)
		if err != nil {
			return dbExit(err)
		}
		rs, err := vulnscan.ScanRelease(ctx, store, scanner, local, rel, vulnscan.RescanAlways)
		if err != nil {
			return undeterminedf("%v", err)
		}
		notes = rs.Notes
		if env.GradeNote != "" {
			notes = append(notes, env.GradeNote)
		}
		in = vulnscan.DocumentInput{Env: opts.deployed, Release: rel.Version, Gate: env.Gate, GradedAs: env.GradedAs, Artifacts: rs.Artifacts, Now: now, Notes: notes}
	default:
		source, name := "", ""
		if opts.image != "" {
			source, name = "registry:"+opts.image, opts.image
		} else {
			if dir == "" {
				dir = "."
			}
			abs, err := filepath.Abs(dir)
			if err != nil {
				return err
			}
			fi, err := os.Stat(abs)
			switch {
			case err != nil:
				return fmt.Errorf("scan %s: %w", dir, err)
			case fi.IsDir():
				source, name = "dir:"+abs, dir
			default:
				// A file is an SBOM someone else produced (CycloneDX or
				// SPDX JSON; grype detects which).
				source, name = "sbom:"+abs, dir
			}
		}
		scan, err := scanner.Scan(ctx, source)
		if err != nil {
			return dbExit(err)
		}
		in = vulnscan.DocumentInput{Gate: env.Gate, Artifacts: []vulnscan.ArtifactScan{{Artifact: name, Latest: scan}}, Now: now}
	}
	doc := vulnscan.BuildDocument(in)
	if opts.asJSON {
		if err := writeVulnJSON(out, doc); err != nil {
			return err
		}
	} else {
		label := ""
		if len(in.Artifacts) == 1 && in.Release == "" {
			label = in.Artifacts[0].Artifact
		}
		writeVulnDocument(out, doc, now, label)
	}
	return vulnExit(doc)
}

// writeVulnJSON prints a document as JSON, unescaped (a purl's & stays &).
func writeVulnJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
