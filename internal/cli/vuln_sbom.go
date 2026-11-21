package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/internal/toolfetch"
	"github.com/reliant-labs/forge/internal/vulnscan"
)

type vulnSBOMOptions struct {
	release  string
	artifact string
	format   string
}

func newVulnSBOMCmd(deps vulnDeps) *cobra.Command {
	var opts vulnSBOMOptions
	cmd := &cobra.Command{
		Use:   "sbom <env>",
		Short: "Print the SBOM a release recorded for one artifact",
		Long: `Print the SBOM (CycloneDX JSON, as forge stored it at the cut) of one artifact of
the release an env runs, or of --release. The bytes come from this machine's
ledger, verified against the digest the release records. For an env whose
ledger is a control plane, the control plane names the SBOM's digest and the
bytes are this machine's copy (the cut keeps one), or, for a release cut on
another machine, the OCI referrer beside the image, verified against that
digest and kept in this machine's ledger for the next read.

--artifact names the artifact; it may be left out when only one artifact has an
SBOM. --format spdx converts to SPDX JSON with syft (installed on first use).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVulnSBOM(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), deps, args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.release, "release", "", "read this release instead of the one the env runs")
	cmd.Flags().StringVar(&opts.artifact, "artifact", "", "the artifact whose SBOM to print")
	cmd.Flags().StringVar(&opts.format, "format", "cyclonedx", "cyclonedx (as stored) or spdx (converted with syft)")
	return cmd
}

func runVulnSBOM(ctx context.Context, out, errOut io.Writer, deps vulnDeps, envName string, opts vulnSBOMOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	format := strings.ToLower(strings.TrimSpace(opts.format))
	if format != "cyclonedx" && format != "spdx" {
		return fmt.Errorf("--format %q (expected cyclonedx or spdx)", opts.format)
	}
	projectDir := deps.projectDir()
	env, err := deps.resolveEnv(ctx, projectDir, envName)
	if err != nil {
		return undeterminedf("env %q: %v", envName, err)
	}
	if env.hosted() {
		data, err := hostedVulnSBOM(ctx, deps, env, opts)
		if err != nil {
			return err
		}
		return writeSBOM(ctx, out, errOut, deps, data, format)
	}
	store, err := deps.machineLedger(projectDir)
	if err != nil {
		return undeterminedf("open this machine's ledger: %v", err)
	}
	rel, err := releaseOf(store, envName, opts.release)
	if err != nil {
		return err
	}
	var withSBOM []string
	for name, a := range rel.Artifacts {
		if a.SBOM.HasSBOM() {
			withSBOM = append(withSBOM, name)
		}
	}
	sort.Strings(withSBOM)
	name := opts.artifact
	switch {
	case name == "" && len(withSBOM) == 1:
		name = withSBOM[0]
	case name == "" && len(withSBOM) == 0:
		return undeterminedf("release %s recorded no SBOM for any artifact", rel.Version)
	case name == "":
		return fmt.Errorf("release %s has SBOMs for %s: name one with --artifact", rel.Version, strings.Join(withSBOM, ", "))
	}
	a, ok := rel.Artifacts[name]
	if !ok {
		return fmt.Errorf("release %s has no artifact %q (artifacts with an SBOM: %s)", rel.Version, name, strings.Join(withSBOM, ", "))
	}
	if !a.SBOM.HasSBOM() {
		reason := ""
		if a.SBOM != nil && a.SBOM.Reason != "" {
			reason = ": " + a.SBOM.Reason
		}
		return undeterminedf("release %s recorded no SBOM for %s%s", rel.Version, name, reason)
	}
	data, err := store.GetSBOM(a.SBOM.Digest)
	if err != nil {
		if errors.Is(err, ledgerfile.ErrSBOMNotFound) || errors.Is(err, ledgerfile.ErrSBOMCorrupt) {
			return undeterminedf("the SBOM of %s in release %s: %v", name, rel.Version, err)
		}
		return err
	}
	return writeSBOM(ctx, out, errOut, deps, data, format)
}

// writeSBOM prints a stored CycloneDX SBOM as format.
func writeSBOM(ctx context.Context, out, errOut io.Writer, deps vulnDeps, data []byte, format string) error {
	if format == "cyclonedx" {
		_, err := out.Write(data)
		return err
	}
	return convertSBOMToSPDX(ctx, out, errOut, deps, data)
}

// convertSBOMToSPDX runs `syft convert` over the stored CycloneDX.
func convertSBOMToSPDX(ctx context.Context, out, errOut io.Writer, deps vulnDeps, cdx []byte) error {
	path, _, err := deps.tool(ctx, toolfetch.Syft, true)
	if err != nil {
		return undeterminedf("convert to SPDX: syft unavailable: %v", err)
	}
	dir, err := os.MkdirTemp("", "forge-vuln-sbom-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	in := filepath.Join(dir, "sbom.cdx.json")
	if err := atomicfile.WriteFile(in, cdx, 0o600); err != nil {
		return err
	}
	run := deps.run
	if run == nil {
		run = vulnscan.Exec
	}
	var stderr bytes.Buffer
	if err := run(ctx, vulnscan.Command{Path: path, Args: []string{"convert", in, "-o", "spdx-json", "-q"}, Env: os.Environ(), Dir: dir, Stdout: out, Stderr: &stderr}); err != nil {
		_, _ = io.Copy(errOut, &stderr)
		return fmt.Errorf("syft convert: %w", err)
	}
	return nil
}
