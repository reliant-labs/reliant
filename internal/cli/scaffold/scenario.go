package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/templates"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// scenarioNameRE constrains scenario names to lowercase-kebab matching
// the `?scenario=<name>` URL param. The leading-letter rule keeps the
// generated TS identifier safe (digits and hyphens aren't valid
// identifier starts).
var scenarioNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// validateScenarioName enforces the lowercase-kebab rule plus a few
// extras (no trailing hyphen, no reserved word). See ADR 0002 §Rules.
func validateScenarioName(name string) error {
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}
	if !scenarioNameRE.MatchString(name) {
		return fmt.Errorf("name %q must match ^[a-z][a-z0-9-]*$ (lowercase-kebab, starts with a letter)", name)
	}
	if strings.HasSuffix(name, "-") {
		return fmt.Errorf("name cannot end with a hyphen")
	}
	if strings.Contains(name, "--") {
		return fmt.Errorf("name cannot contain consecutive hyphens")
	}
	return nil
}

// newScenarioCmd is the cobra surface for `forge scaffold scenario <name>`.
//
// Scenarios are typed RPC handler overlays selected via `?scenario=<name>`
// in the URL. The contract is the generated src/mocks/scenario-types_gen.ts in
// the target frontend, plus the mock-mode section of `forge skill load
// frontend`.
//
// The command:
//   - validates the name (lowercase-kebab),
//   - locates the target frontend's src/mocks/scenarios/ directory,
//   - writes src/mocks/scenarios/<name>.ts (either from the scenario
//     template or, with --from, by copying an existing scenario),
//   - regenerates the registry barrel so the new scenario is picked up.
func newScenarioCmd(f *factory.Factory) *cobra.Command {
	var (
		frontend    string
		from        string
		description string
	)

	cmd := &cobra.Command{
		Use:   "scenario <name>",
		Short: "Scaffold a new frontend mock scenario",
		Long: `Scaffold a new mock scenario for the frontend.

Scenarios are typed Connect-RPC handler overlays that let agents and
humans teleport into specific server-state shapes by navigating to
?scenario=<name> in the URL. Anything not overridden by the scenario
falls through to the base fixture transport.

The command writes src/mocks/scenarios/<name>.ts and regenerates the
registry so the new file is picked up automatically. Edit the new file
to add typed handlers — the contract is src/mocks/scenario-types_gen.ts, and
` + "`forge skill load frontend`" + ` covers mock mode end to end.

Examples:
  forge scaffold scenario github-connected
  forge scaffold scenario github-revoked --from github-connected
  forge scaffold scenario admin-dashboard --frontend admin-web`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScenario(f, args[0], frontend, from, description)
		},
	}

	cmd.Flags().StringVar(&frontend, "frontend", "", "Target frontend name (required when the project has multiple frontends)")
	cmd.Flags().StringVar(&from, "from", "", "Copy an existing scenario file as a starting point (by name, no .ts suffix)")
	cmd.Flags().StringVar(&description, "description", "", "Optional human-readable description embedded in the generated file")

	return cmd
}

func runScenario(f *factory.Factory, name, frontend, from, description string) error {
	ctxLabel := fmt.Sprintf("forge scaffold scenario %s", name)

	if err := validateScenarioName(name); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "invalid scenario name", "",
			"use a lowercase-kebab name starting with a letter, e.g. 'github-connected'",
			err)
	}

	root, err := projectRoot()
	if err != nil {
		return err
	}

	configPath := filepath.Join(root, "forge.yaml")
	cfg, err := generator.ReadProjectConfig(configPath)
	if err != nil {
		return cliutil.WrapUserErr(ctxLabel, "read project config", configPath,
			"verify forge.yaml is valid YAML", err)
	}

	fe, err := resolveScenarioFrontend(cfg, frontend)
	if err != nil {
		return cliutil.WrapUserErr(ctxLabel, "resolve target frontend", "",
			"pass --frontend <name> to disambiguate, or add a frontend first with 'forge scaffold frontend <name>'",
			err)
	}

	feDir, ok := fe.Dir(root)
	if !ok {
		return cliutil.WrapUserErr(ctxLabel, "resolve target frontend", fe.Name,
			"a frontend whose code is in another repository has no directory in this "+
				"project to write scenarios into — scaffold scenarios in that repository instead",
			fmt.Errorf("frontend %q has no directory in this project", fe.Name))
	}
	scenariosRel := filepath.Join(feDir, "src", "mocks", "scenarios")
	scenariosDir := filepath.Join(root, scenariosRel)
	if err := os.MkdirAll(scenariosDir, 0o755); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "create scenarios directory", scenariosDir,
			"check filesystem permissions on the frontend directory", err)
	}

	// Scenarios import typed handlers from the Connect-RPC TS stubs under
	// src/gen/. If the frontend hasn't been generated yet (or src/gen was
	// wiped) the new file's import path won't resolve — warn but continue.
	genDir := filepath.Join(root, feDir, "src", "gen")
	if _, err := os.Stat(genDir); os.IsNotExist(err) {
		fmt.Printf("⚠️  %s is missing — run `forge generate` first or the scenario import path won't resolve.\n",
			filepath.Join(feDir, "src", "gen"))
	}

	outPath := filepath.Join(scenariosDir, name+".ts")
	if _, err := os.Stat(outPath); err == nil {
		return cliutil.UserErr(ctxLabel,
			fmt.Sprintf("scenario %q already exists at %s", name, outPath),
			"",
			"pick a different name, or delete the existing file first")
	}

	if from != "" {
		if err := copyScenarioFrom(scenariosDir, from, name, outPath); err != nil {
			return cliutil.WrapUserErr(ctxLabel,
				fmt.Sprintf("copy scenario from %q", from), "",
				fmt.Sprintf("verify src/mocks/scenarios/%s.ts exists in the target frontend", from),
				err)
		}
	} else {
		if err := writeScenarioFromTemplate(outPath, name, description); err != nil {
			return cliutil.WrapUserErr(ctxLabel, "render scenario template", outPath, "", err)
		}
	}

	// Regenerate the registry barrel so the new scenario is discoverable.
	// The barrel is Tier-1, so the write goes through the certification
	// chokepoint with the project's ownership state — a barrel the user
	// disowned stays theirs even here.
	cs, err := generator.LoadChecksums(root)
	if err != nil {
		return cliutil.WrapUserErr(ctxLabel, "load ownership state", "",
			"verify .forge/disowned.json and .forge/hashes.json are valid JSON", err)
	}
	if err := f.Gen.WriteScenariosIndex(root, scenariosRel, cs); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "regenerate scenarios index", scenariosDir,
			"run 'forge generate' to rebuild the registry manually", err)
	}
	if err := generator.SaveChecksums(root, cs); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "save ownership state", "",
			"check write permissions on .forge/", err)
	}

	relPath, _ := filepath.Rel(root, outPath)
	if relPath == "" {
		relPath = outPath
	}
	fmt.Printf("✅ Created %s\n", relPath)
	fmt.Println("   Edit it to add typed handlers. Contract: src/mocks/scenario-types_gen.ts (`forge skill load frontend` for mock mode).")
	return nil
}

// resolveScenarioFrontend picks the target frontend for the new scenario.
//
//   - If --frontend was passed, look it up by name; error if missing.
//   - Otherwise, exactly one nextjs/vite-spa frontend must exist.
//
// React-Native ("mobile") frontends are excluded — they don't ship with
// the connect.ts / mock-transport substrate scenarios sit on.
func resolveScenarioFrontend(cfg *config.ProjectConfig, requested string) (*config.FrontendConfig, error) {
	var supported []*config.FrontendConfig
	for i := range cfg.Frontends {
		fe := &cfg.Frontends[i]
		if !strings.EqualFold(fe.Type, "nextjs") && !strings.EqualFold(fe.Type, "vite-spa") {
			continue
		}
		supported = append(supported, fe)
	}

	if len(supported) == 0 {
		return nil, fmt.Errorf("no nextjs or vite-spa frontends found in forge.yaml")
	}

	if requested != "" {
		for _, fe := range supported {
			if fe.Name == requested {
				return fe, nil
			}
		}
		names := make([]string, 0, len(supported))
		for _, fe := range supported {
			names = append(names, fe.Name)
		}
		return nil, fmt.Errorf("frontend %q not found; known frontends: %s", requested, strings.Join(names, ", "))
	}

	if len(supported) > 1 {
		names := make([]string, 0, len(supported))
		for _, fe := range supported {
			names = append(names, fe.Name)
		}
		return nil, fmt.Errorf("multiple frontends present (%s); pass --frontend <name>", strings.Join(names, ", "))
	}

	return supported[0], nil
}

// writeScenarioFromTemplate renders scenarios/scenario.ts.tmpl with the
// new scenario's name + description and writes it to outPath.
func writeScenarioFromTemplate(outPath, name, description string) error {
	content, err := templates.FrontendTemplates().Render(
		filepath.Join("mocks", "scenarios", "scenario.ts.tmpl"),
		struct {
			Name        string
			Description string
		}{Name: name, Description: description},
	)
	if err != nil {
		return fmt.Errorf("render scenario template: %w", err)
	}
	if err := atomicfile.WriteFile(outPath, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	return nil
}

// copyScenarioFrom reads an existing scenario file and writes a copy
// with the `name:` field replaced. The copy keeps the original
// handlers + setup intact — the user typically tweaks one or two
// handlers from there.
//
// We do a simple regex replace on the literal `name: "<old>"` /
// `name: '<old>'` form (the only one the template ever emits). If the
// source has been hand-edited into a more exotic shape and the regex
// misses, the user notices immediately on the first render because the
// new scenario's `byName(<new>)` lookup fails. That's preferable to
// silently writing a file whose `name` field still points at the old
// scenario.
func copyScenarioFrom(scenariosDir, from, newName, outPath string) error {
	srcPath := filepath.Join(scenariosDir, from+".ts")
	srcBytes, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read source scenario: %w", err)
	}

	// Match `name: "<from>"` or `name: '<from>'`. The template always
	// emits double quotes; users may have switched to single quotes by
	// hand. Anchoring on the exact source name keeps the replace narrow.
	pattern := regexp.MustCompile(`name:\s*["']` + regexp.QuoteMeta(from) + `["']`)
	updated := pattern.ReplaceAllString(string(srcBytes), fmt.Sprintf(`name: %q`, newName))

	if updated == string(srcBytes) {
		// No name field found — bail rather than write a scenario whose
		// `name` doesn't match its filename. The user can hand-edit the
		// copy after running with --from.
		return fmt.Errorf("could not find `name: %q` in %s; copy aborted (hand-edit the source's name field to the literal string, then retry)", from, srcPath)
	}

	if err := atomicfile.WriteFile(outPath, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	return nil
}
