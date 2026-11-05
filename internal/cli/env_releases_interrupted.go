package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cutjournal"
)

// interruptedDoc is `forge env releases interrupted --json`.
type interruptedDoc struct {
	Env     string `json:"env"`
	Version string `json:"version"`
	// Found is true when a cut of the version left a journal in the registry.
	Found bool `json:"found"`
	// Resumable is true when `forge env build <env> --release <version>`
	// from this checkout would resume that cut rather than build.
	Resumable    bool               `json:"resumable"`
	Journal      string             `json:"journal,omitempty"`
	Commit       string             `json:"commit,omitempty"`
	ForgeVersion string             `json:"forge_version,omitempty"`
	Images       []cutjournal.Image `json:"images,omitempty"`
	// Problems are why it is not resumable from this checkout.
	Problems []string `json:"problems,omitempty"`
}

func newEnvReleasesInterruptedCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "interrupted <env> <version>",
		Short: "Whether a cut of <version> pushed its images, was never recorded, and can be resumed from here",
		Args:  cobra.ExactArgs(2),
		Long: "A `forge env build <env> --release <version>` writes a cut journal to the registry after\n" +
			"its last push and before its ledger record. If the cut is interrupted between the two,\n" +
			"the version's image tags exist (and are immutable) while the ledger has no release.\n" +
			"Re-running the same cut from the same clean commit, with the same forge, resumes it:\n" +
			"it records the release over the images already pushed and rebuilds nothing.\n\n" +
			"This answers, without building or writing anything, whether such a journal exists\n" +
			"and whether this checkout can resume it: the commit and tree, the forge version,\n" +
			"and that every recorded tag still names its recorded digest.\n\n" +
			"Exit 0 a journal exists and this checkout resumes it, 1 no journal or not resumable\n" +
			"(the document says which), 2 the registry or the env could not be read.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvReleasesInterrupted(cmd.Context(), cmd.OutOrStdout(), args[0], args[1], asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print one JSON document")
	return cmd
}

func runEnvReleasesInterrupted(ctx context.Context, out io.Writer, env, version string, asJSON bool) error {
	if err := validateBuildEnvArg(env); err != nil {
		return err
	}
	opts := buildOptions{env: env, release: version, push: true, buildDocker: true, skipGenerate: true, outputDir: "bin"}
	cfg, err := prepareBuild(opts)
	if err != nil {
		return undeterminedf("env %s: %v", env, err)
	}
	entities, _, err := renderBuildInputs(ctx, cfg, &opts)
	if err != nil {
		return undeterminedf("env %s: %v", env, err)
	}
	found, err := findInterruptedCut(ctx, projectDirForKCL(), entities, opts)
	if err != nil {
		return undeterminedf("read the cut journal of %s in %s: %v", version, cutJournalRepository(opts.pushPlan), err)
	}
	doc := interruptedDoc{Env: env, Version: version, Found: found.Found, Resumable: found.resumable(), Journal: found.Ref, Problems: found.Problems}
	if found.Found {
		doc.Commit, doc.ForgeVersion, doc.Images = found.Journal.Commit, found.Journal.ForgeVersion, found.Journal.Images
	}
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(doc); err != nil {
			return err
		}
	} else {
		renderInterrupted(out, doc)
	}
	if !doc.Resumable {
		return &exitCodeError{code: 1, msg: interruptedVerdict(doc)}
	}
	return nil
}

func interruptedVerdict(doc interruptedDoc) string {
	if !doc.Found {
		return fmt.Sprintf("no interrupted cut of %s in %s's registry", doc.Version, doc.Env)
	}
	return fmt.Sprintf("the interrupted cut of %s cannot be resumed from this checkout: %s", doc.Version, strings.Join(doc.Problems, "; "))
}

func renderInterrupted(out io.Writer, doc interruptedDoc) {
	if !doc.Found {
		fmt.Fprintf(out, "No interrupted cut of %s: no cut journal is tagged for it.\n", doc.Version)
		return
	}
	fmt.Fprintf(out, "Interrupted cut of %s (journal %s)\n  built from %s by forge %s\n", doc.Version, doc.Journal, doc.Commit, doc.ForgeVersion)
	for _, img := range doc.Images {
		fmt.Fprintf(out, "  %s\n", img.Ref())
	}
	if doc.Resumable {
		fmt.Fprintf(out, "Resumable from this checkout: forge env build %s --release %s records it over these images.\n", doc.Env, doc.Version)
		return
	}
	fmt.Fprintln(out, "Not resumable from this checkout:")
	for _, p := range doc.Problems {
		fmt.Fprintf(out, "  - %s\n", p)
	}
}
