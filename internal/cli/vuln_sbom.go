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
	"github.com/reliant-labs/forge/internal/sbom"
	"github.com/reliant-labs/forge/internal/toolfetch"
	"github.com/reliant-labs/forge/internal/vulnscan"
)

type vulnSBOMOptions struct {
	release  string
	artifact string
	format   string
	file     string
}

func newVulnSBOMCmd(deps vulnDeps) *cobra.Command {
	var opts vulnSBOMOptions
	cmd := &cobra.Command{
		Use:   "sbom <env> | --file <path>",
		Short: "Print the SBOM a release recorded for one artifact, or generate one for a file",
		Long: `Print the SBOM (CycloneDX JSON, as forge stored it at the cut) of one artifact of
the release an env runs, or of --release. The bytes come from this machine's
ledger, verified against the digest the release records. For an env whose
ledger is a control plane, the control plane names the SBOM's digest and the
bytes are this machine's copy (the cut keeps one), or, for a release cut on
another machine, the OCI referrer beside the image, verified against that
digest and kept in this machine's ledger for the next read.

--artifact names the artifact; it may be left out when only one artifact has an
SBOM. --format spdx converts to SPDX JSON with syft (installed on first use).

--file <path> takes no env: it generates the SBOM of one file, a binary (Go
buildinfo lists every linked module) or a lockfile, with forge's pinned syft
and the same invocation a release cut uses for a host binary, and prints it.
Nothing is recorded. The document names the artifact --artifact (default: the
file's name), never the path on this machine, and carries --release as its
version. Use it for binaries your own pipeline publishes; forge's own releases
attach one per archive this way. A file syft cannot describe exits 2.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if opts.file != "" {
				if len(args) != 0 {
					return fmt.Errorf("--file generates an SBOM of a file and reads no env: drop %q", args[0])
				}
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.file != "" {
				return runVulnSBOMFile(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), deps, opts)
			}
			return runVulnSBOM(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), deps, args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.release, "release", "", "read this release instead of the one the env runs (with --file: the version the SBOM records)")
	cmd.Flags().StringVar(&opts.artifact, "artifact", "", "the artifact whose SBOM to print (with --file: the name the SBOM records)")
	cmd.Flags().StringVar(&opts.format, "format", "cyclonedx", "cyclonedx (as stored) or spdx (converted with syft)")
	cmd.Flags().StringVar(&opts.file, "file", "", "generate the SBOM of this binary or lockfile with forge's pinned syft, instead of reading a release")
	return cmd
}

func sbomFormat(opts vulnSBOMOptions) (string, error) {
	format := strings.ToLower(strings.TrimSpace(opts.format))
	if format != "cyclonedx" && format != "spdx" {
		return "", fmt.Errorf("--format %q (expected cyclonedx or spdx)", opts.format)
	}
	return format, nil
}

// runVulnSBOMFile generates the SBOM of opts.file with the generator the
// release cut uses (internal/sbom, KindBinary): forge's pinned syft, the
// pinned CycloneDX version, the document checked, and the artifact's name in
// place of this machine's path. The SBOM cache is shared with the cut, so a
// file described before (a retried release) runs no syft.
func runVulnSBOMFile(ctx context.Context, out, errOut io.Writer, deps vulnDeps, opts vulnSBOMOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	format, err := sbomFormat(opts)
	if err != nil {
		return err
	}
	fi, err := os.Stat(opts.file)
	switch {
	case err != nil:
		return fmt.Errorf("--file %s: %w", opts.file, err)
	case fi.IsDir():
		return fmt.Errorf("--file %s is a directory: name one file (`forge vuln scan <dir>` scans a tree)", opts.file)
	}
	name := strings.TrimSpace(opts.artifact)
	if name == "" {
		name = filepath.Base(opts.file)
	}
	gen := &sbom.Generator{
		Tool: func(ctx context.Context) (string, string, error) {
			return deps.tool(ctx, toolfetch.Syft, true)
		},
		Runner:  deps.sbomRunner,
		Timeout: sbom.DefaultTimeout,
	}
	if deps.sbomCache != nil {
		if dir := deps.sbomCache(); dir != "" {
			gen.Cache = &sbom.Cache{Dir: dir}
		}
	}
	res := gen.Generate(ctx, []sbom.Target{{Name: name, Kind: sbom.KindBinary, Ref: opts.file, Version: strings.TrimSpace(opts.release)}})[0]
	if !res.Ref.HasSBOM() {
		return undeterminedf("no SBOM for %s: %s", opts.file, res.Ref.Reason)
	}
	return writeSBOM(ctx, out, errOut, deps, res.Bytes, format)
}

func runVulnSBOM(ctx context.Context, out, errOut io.Writer, deps vulnDeps, envName string, opts vulnSBOMOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	format, err := sbomFormat(opts)
	if err != nil {
		return err
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
