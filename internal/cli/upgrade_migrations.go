package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/shellrun"
	"github.com/reliant-labs/forge/internal/templates"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Migration metadata + state lives alongside the existing template-drift
// upgrader (upgrade.go). The split is intentional: `forge project upgrade` (the
// existing command) runs deterministic codemods + template-drift rewrites;
// `forge project upgrade list` and `forge project upgrade apply <id>` surface the
// LLM-readable migration skills under skills/forge/migrations/ and let
// the user (or an agent) record that a migration has been applied.
//
// # The per-version model
//
// One skill per RELEASE that introduced a breaking change, named for that
// release and living at skills/forge/migrations/<version>/SKILL.md:
//
//	---
//	name: v0.5.0
//	description: the deploy target moved from forge.yaml into KCL
//	version:   v0.5.0
//	detection: grep -q '^dev_target:' forge.yaml
//	---
//
// `version` is the release that BROKE the shape, and it must equal the
// directory name — the directory is the identifier the CLI prints and
// records, the frontmatter makes the file self-describing when an agent
// reads it standalone, and TestMigrationSkills_VersionMatchesDirectory
// pins the two together so they cannot drift.
//
// A migration applies to a project when the project predates the release
// (baseline < version) AND the project still exhibits the old shape
// (detection exits 0). That pair is what makes a multi-version jump
// correct: a project pinned at v0.2 upgrading to v0.6 satisfies
// `baseline < version` for v0.3, v0.4, v0.5 and v0.6 alike, so it is
// handed all four in release order rather than only the last hop's.
//
// Why per-version rather than the per-transition ranges this used to
// use: a range ([from, to)) describes a project's position on a timeline,
// so a project that jumped a range without landing inside it fell through
// the gap and got nothing. A single `version` describes the RELEASE, and
// "did you cross it" is a question every baseline can answer, including
// baselines that skipped ten releases at once.
//
// Detection is REQUIRED, and it must test for the old SHAPE, never for a
// version — the version half of the decision is already made by the
// `version` field. A project can sit below a release and still not need
// its migration (it never used the feature that changed), and an
// unpinned or dev-built project has no usable baseline at all, so what
// the tree actually contains is the only gate that answers both cases.

// migrationSkillsRoot is the embedded-template path prefix where every
// migration skill lives. Every direct subdirectory of this root is a
// migration named for the release it belongs to; nothing else lives here.
const migrationSkillsRoot = "migrations"

// migrationsStateFile is the on-disk record of applied migrations,
// relative to the project root. The file is JSON; absent file means
// no migrations have been recorded yet.
const migrationsStateFile = ".forge/migrations.json"

// supportedUpgradeWindowMinors is how many minor releases back forge
// accepts as an upgrade starting point — the Istio model, where the
// project owner does a STAGED upgrade through intermediate releases
// instead of one long jump.
//
// The tradeoff this number sets:
//
//   - Larger N means fewer stops for someone upgrading an old project,
//     paid for by every migration skill in the window having to stay
//     correct against every shape in it. Migrations compose
//     multiplicatively — N releases back is N migrations that must all
//     still apply cleanly, in order, to a tree none of their authors
//     ever saw — and that product is what makes an unbounded registry
//     rot silently rather than loudly.
//   - Smaller N means a bounded, testable registry, paid for by more
//     intermediate hops on a very old project.
//
// Two is the smallest N that still lets someone skip a release they
// never deployed, which is the case that actually comes up. It is also
// the number the deprecation policy already promises: an old shape stays
// buildable with warnings for two minors, so a project inside the window
// is a project whose shapes forge still supports.
//
// This constant is also the pruning rule. When a release ages out of the
// window its migration skill is DELETED, not archived — a skill nothing
// can reach is a skill nothing tests, and the honest answer for a project
// that far back is the staged-upgrade message, not a stale playbook.
const supportedUpgradeWindowMinors = 2

// migrationMeta is the parsed frontmatter for one migration skill.
type migrationMeta struct {
	// ID is the directory name, which is the release version this
	// migration belongs to (e.g. "v0.5.0") — the stable identifier used
	// by `forge project upgrade apply <id>` and in the applied-state JSON.
	ID string `json:"id"`
	// SkillPath is the path you'd pass to `forge skill load` to read
	// the migration's body (e.g. "migrations/v0.5.0").
	SkillPath string `json:"skill_path"`
	// Title is a human-readable name from frontmatter `name:`. Falls
	// back to ID when missing.
	Title string `json:"title"`
	// Description is the one-line summary from frontmatter `description:`.
	Description string `json:"description"`
	// Version is the release that introduced the breaking change, from
	// frontmatter `version:`. A project applies this migration when its
	// baseline is BELOW this version.
	Version string `json:"version,omitempty"`
	// Detection is the shell command run in the project root that decides
	// whether the project still exhibits the old shape. Required: a
	// migration with no detection cannot show it applies to anything.
	Detection string `json:"detection,omitempty"`
}

// migrationsState is the JSON shape written to .forge/migrations.json.
type migrationsState struct {
	// Applied maps migration ID -> ISO-8601 timestamp it was marked
	// applied. Keys are stable across forge versions (the migration
	// ID is the release version, not a hash).
	Applied map[string]string `json:"applied"`
}

// pendingMigration is one row in `forge project upgrade list` output.
type pendingMigration struct {
	Meta    migrationMeta `json:"meta"`
	Applied bool          `json:"applied"`
	// AppliedAt is the ISO-8601 timestamp from migrations.json when
	// Applied is true; empty otherwise.
	AppliedAt string `json:"applied_at,omitempty"`
}

// attachMigrationSubcommands wires `forge project upgrade list` and
// `forge project upgrade apply <id>` onto the existing upgrade cobra command.
func attachMigrationSubcommands(upgrade *cobra.Command) {
	upgrade.AddCommand(newUpgradeListCmd())
	upgrade.AddCommand(newUpgradeApplyCmd())
}

func newUpgradeListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List pending forge migrations for this project",
		Long: `List forge migration skills for releases this project has not crossed
yet and whose detection script matches the project's current shape.

Migrations are LLM-readable playbooks under skills/forge/migrations/,
one per release that introduced a breaking change. This command does NOT
apply them — it surfaces the worklist so the user (or an agent) can load
each skill via 'forge skill load' and execute the steps. Use
'forge project upgrade apply <id>' to record a migration as applied once
you've finished its steps.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			pending, err := computePendingMigrations()
			if err != nil {
				return err
			}
			shipped, err := loadMigrationMetas()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				return writePendingMigrationsJSON(out, pending, len(shipped))
			}
			return writePendingMigrationsHuman(out, pending, len(shipped))
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output JSON instead of human-readable text")
	return cmd
}

func newUpgradeApplyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "apply <migration-id>",
		Short: "Record a migration as applied (writes .forge/migrations.json)",
		Long: `Mark a migration as applied. This does NOT execute the migration —
loading the skill and running its steps is the user's (or an agent's)
job. 'apply' just records the outcome so later 'forge project upgrade list'
runs hide migrations that have already been worked through.

The migration ID is the release version it belongs to, which is also its
directory name under skills/forge/migrations/ (e.g. "v0.5.0").`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			return runUpgradeApply(cmd.OutOrStdout(), id)
		},
	}
}

// computePendingMigrations is the core listing routine. It returns every
// migration that migrationApplies accepts for this project, tagged with
// whether it has already been applied (per .forge/migrations.json).
//
// Migrations that don't apply are simply omitted — the caller doesn't
// need to reason about why.
func computePendingMigrations() ([]pendingMigration, error) {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return nil, err
	}
	if projectRoot == "" {
		return nil, cliutil.UserErr("forge project upgrade list",
			"no forge project found in this directory or any parent",
			"",
			"run 'forge project new' to create a project, or cd into one")
	}

	migrations, err := loadMigrationMetas()
	if err != nil {
		return nil, fmt.Errorf("load migration skills: %w", err)
	}

	state, err := readMigrationsState(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", migrationsStateFile, err)
	}

	out := applicableMigrations(migrations, projectBaselineVersion(), projectRoot)
	for i := range out {
		if ts, ok := state.Applied[out[i].Meta.ID]; ok {
			out[i].Applied = true
			out[i].AppliedAt = ts
		}
	}
	return out, nil
}

// applicableMigrations filters a migration set down to the rows that
// apply at the given baseline, in RELEASE ORDER (oldest first).
//
// The ordering is the contract, not a presentation detail. A project
// crossing several releases at once has to apply their migrations in the
// order the releases shipped: each one assumes the previous one has
// already landed, so running v0.6's playbook against a tree still in
// v0.4's shape is how a staged upgrade corrupts a project. Sorting by
// SemVer precedence (with the ID as tie-break for determinism) is what
// makes the printed worklist directly executable top to bottom.
func applicableMigrations(migrations []migrationMeta, baseline, projectRoot string) []pendingMigration {
	var out []pendingMigration
	for _, m := range migrations {
		if !migrationApplies(m, baseline, projectRoot) {
			continue
		}
		out = append(out, pendingMigration{Meta: m})
	}
	sort.Slice(out, func(i, j int) bool {
		vi, vj := semverKey(out[i].Meta.Version), semverKey(out[j].Meta.Version)
		if vi != vj && vi != "" && vj != "" {
			return semver.Compare(vi, vj) < 0
		}
		return out[i].Meta.ID < out[j].Meta.ID
	})
	return out
}

// projectBaselineVersion reads the forge_version the project is pinned
// to, or "" when there is no readable forge.yaml.
func projectBaselineVersion() string {
	cfgPath, err := findProjectConfigFile()
	if err != nil {
		return ""
	}
	store, err := loadProjectStoreFrom(cfgPath)
	if err != nil {
		return ""
	}
	return store.Meta().EffectiveForgeVersion()
}

// loadMigrationMetas enumerates every SKILL.md under
// skills/forge/migrations/ in the embedded templates and parses its
// frontmatter. Every direct subdirectory of migrationSkillsRoot is a
// migration named for its release.
//
// An empty result is a normal, expected answer — forge ships no
// migrations until a release actually breaks something, and the registry
// is pruned back down as releases age out of the supported window.
func loadMigrationMetas() ([]migrationMeta, error) {
	// The skills tree is rooted under "forge" in the embedded FS, so
	// the relative scan path is "forge/<migrationSkillsRoot>".
	relRoot := path.Join("forge", migrationSkillsRoot)
	entries, err := templates.ProjectTemplates().List(path.Join("skills", relRoot))
	if err != nil {
		// Missing root — no migrations shipped. Not an error: the
		// registry is empty whenever no release in the supported
		// window carries a breaking change.
		return nil, nil
	}

	var out []migrationMeta
	for _, rel := range entries {
		// `entries` are paths relative to skills/forge/migrations/,
		// e.g. "v0.5.0/SKILL.md".
		if !strings.HasSuffix(rel, "/SKILL.md") {
			continue
		}
		id := strings.TrimSuffix(rel, "/SKILL.md")
		// Defence in depth: a nested SKILL.md (a/b/SKILL.md) would
		// produce an id of "a/b" which is not a valid filesystem-friendly
		// migration identifier. Skip any non-flat layout.
		if strings.Contains(id, "/") {
			continue
		}

		content, err := templates.ProjectTemplates().Get(path.Join("skills", relRoot, rel))
		if err != nil {
			continue
		}
		m := parseMigrationFrontmatter(content)
		m.ID = id
		m.SkillPath = path.Join(migrationSkillsRoot, id)
		// The directory name is the canonical version. Frontmatter that
		// omits `version:` inherits it rather than silently becoming
		// version-less (which would make the migration apply to every
		// baseline that matched detection).
		if strings.TrimSpace(m.Version) == "" {
			m.Version = id
		}
		out = append(out, m)
	}
	return out, nil
}

// parseMigrationFrontmatter extracts the migration-skill-specific fields
// from a SKILL.md body. It is a focused parser separate from the generic
// parseFrontmatter in skill.go because migration skills carry extra
// fields (version, detection) that aren't part of the generic SkillMeta
// shape.
func parseMigrationFrontmatter(content []byte) migrationMeta {
	s := string(content)
	if !strings.HasPrefix(s, "---\n") {
		return migrationMeta{}
	}
	end := strings.Index(s[4:], "\n---")
	if end < 0 {
		return migrationMeta{}
	}
	block := s[4 : 4+end]

	var m migrationMeta
	for _, line := range strings.Split(block, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		// Strip surrounding quotes — some authors quote values with
		// special chars in YAML; we don't need a full YAML parser to
		// handle the common case. Only a MATCHED pair is a YAML quote: a
		// detection script that ends in a quoted argument (`... "$f"`)
		// must keep its closing quote, or the shell rejects the script
		// and the migration silently matches nothing.
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		switch k {
		case "name":
			m.Title = v
		case "description":
			m.Description = v
		case "version":
			m.Version = v
		case "detection":
			m.Detection = v
		}
	}
	return m
}

// migrationApplies is the SINGLE decision about whether a migration is
// worth putting in front of a project. Every surface that offers
// migrations — `forge project upgrade`'s pre-flight banner and
// `forge project upgrade list` — routes through here, so the two can
// never drift into disagreeing about what applies.
//
// Both gates must pass:
//
//  1. The project predates the release: baseline < version. A project
//     already at or past the release has crossed it, by definition.
//  2. The project exhibits the old shape: detection exits 0.
//
// When the baseline names no version at all (see baselineIsUnknown) the
// version gate is skipped and detection carries the decision alone. An
// unknown baseline is not evidence of being old — it is the absence of
// evidence — so answering it from version ordering means inventing a
// position on the timeline and then filtering against the invention.
// What the project contains is the only thing left that is actually true.
//
// baseline is the project's pinned forge_version (possibly a Go
// pseudo-version from a dev build); projectRoot is where detection runs.
func migrationApplies(m migrationMeta, baseline, projectRoot string) bool {
	// A migration with no detection cannot demonstrate it applies to
	// anything. Refusing it here (rather than defaulting to "applies")
	// keeps a malformed skill out of every worklist instead of into all
	// of them.
	if strings.TrimSpace(m.Detection) == "" {
		return false
	}
	if !baselineIsUnknown(baseline) && !baselinePrecedes(baseline, m.Version) {
		return false
	}
	return runDetection(projectRoot, m.Detection)
}

// baselinePrecedes reports whether a project's baseline sits strictly
// below the release a migration belongs to — i.e. whether the project
// has yet to cross that release.
//
// A migration whose version cannot be ordered falls back to "yes": the
// version gate has nothing to say, so detection decides alone rather
// than the migration silently vanishing.
func baselinePrecedes(baseline, version string) bool {
	v := semverKey(version)
	if v == "" {
		return true
	}
	b := semverKey(baseline)
	if b == "" {
		return true
	}
	return semver.Compare(b, v) < 0
}

// baselineIsUnknown reports whether a project's forge_version names no
// version at all: absent, one of the "dev"/"(devel)" sentinels, the
// "0.0.0" stand-in EffectiveForgeVersion returns for an unset field, or
// anything SemVer cannot place.
//
// "0.0.0" is a sentinel, not a measurement — a project can be unpinned
// and perfectly modern. A REAL pin whose base tag happens to be v0.0.0
// (the `v0.0.0-<timestamp>-<sha>` pseudo-version `go install` produces
// against a repo with no tags) is a different thing: it names a specific
// commit, it is genuinely ancient, and it orders normally.
func baselineIsUnknown(v string) bool {
	switch strings.TrimSpace(v) {
	case "", "dev", "(devel)", "0.0.0", "v0.0.0":
		return true
	}
	return semverKey(v) == ""
}

// semverKey canonicalises a SemVer-ish forge version into a string
// golang.org/x/mod/semver can order, or "" when it is not orderable at
// all (the "0.0.0" unpinned sentinel is orderable; "dev" is not).
//
// Two normalisations: a missing leading "v" is added (forge.yaml pins
// are written both ways), and build metadata is dropped — SemVer gives
// build metadata no precedence, so `v0.0.4-…+dirty` orders exactly
// where `v0.0.4-…` does. Partial versions ("0.5") are accepted and
// zero-filled by semver itself.
func semverKey(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	if !semver.IsValid(v) {
		return ""
	}
	return v
}

// runDetection runs the migration's detection script in the project root
// in-process (internal/shellrun) so it needs no `sh` on PATH; grep and rg
// resolve to portable built-ins, since stock Windows has neither. Returns true if the project exhibits the old shape (the
// script exits 0).
//
// An empty script reports false, not true: "this migration named no way
// to tell whether it applies" is not evidence that it does.
//
// We deliberately do not pipe output to the user — the detection script
// is meant as a silent gate. If a script needs to log, it should be
// rewritten as a real check.
func runDetection(projectRoot, script string) bool {
	if strings.TrimSpace(script) == "" {
		return false
	}
	return shellrun.Run(context.Background(), script, shellrun.Options{
		Dir:      projectRoot,
		Commands: shellrun.PortableCommands(),
	}) == nil
}

// readMigrationsState reads .forge/migrations.json. Absent file is not
// an error — it just means no migrations have been recorded yet.
func readMigrationsState(projectRoot string) (migrationsState, error) {
	state := migrationsState{Applied: map[string]string{}}
	if projectRoot == "" {
		return state, nil
	}
	p := filepath.Join(projectRoot, migrationsStateFile)
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, err
	}
	if len(data) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if state.Applied == nil {
		state.Applied = map[string]string{}
	}
	return state, nil
}

// writeMigrationsState writes the applied-set back to disk, creating
// the .forge dir if missing. The file is JSON with sorted keys (Go's
// encoding/json sorts map keys by default) so diffs against a previous
// state are clean.
func writeMigrationsState(projectRoot string, state migrationsState) error {
	if projectRoot == "" {
		return fmt.Errorf("no project root")
	}
	dir := filepath.Join(projectRoot, ".forge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	p := filepath.Join(projectRoot, migrationsStateFile)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal migrations state: %w", err)
	}
	// Trailing newline keeps the file POSIX-friendly.
	data = append(data, '\n')
	return atomicfile.WriteFile(p, data, 0o644)
}

// runUpgradeApply is the body of `forge project upgrade apply <id>`.
//
// Behaviour:
//   - Reads the project's current applied-state from .forge/migrations.json.
//   - Verifies the migration ID exists in the embedded skill set. Unknown
//     IDs return a UserErr — typo-friendly.
//   - Records the migration as applied with the current timestamp.
//   - Writes the state file back.
//
// Re-applying an already-applied migration is a no-op apart from
// refreshing the timestamp; we don't refuse, because the user might
// legitimately want to re-record after a partial migration.
func runUpgradeApply(out io.Writer, id string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return err
	}
	if projectRoot == "" {
		return cliutil.UserErr("forge project upgrade apply",
			"no forge project found in this directory or any parent",
			"",
			"run 'forge project new' to create a project, or cd into one")
	}

	known, err := loadMigrationMetas()
	if err != nil {
		return fmt.Errorf("load migration skills: %w", err)
	}
	var found *migrationMeta
	for i := range known {
		if known[i].ID == id {
			found = &known[i]
			break
		}
	}
	if found == nil {
		ids := make([]string, 0, len(known))
		for _, k := range known {
			ids = append(ids, k.ID)
		}
		sort.Strings(ids)
		// With an empty registry there is no ID to suggest, so say that
		// outright rather than pointing at a list that will also be
		// empty and leaving the user to discover it a second time.
		hint := fmt.Sprintf("this %s binary ships no migrations, so there is no ID to apply", Name())
		if len(ids) > 0 {
			hint = "available IDs: " + strings.Join(ids, ", ")
		}
		return cliutil.UserErr(
			fmt.Sprintf("forge project upgrade apply %s", id),
			fmt.Sprintf("migration %q not found", id),
			"",
			hint,
		)
	}

	state, err := readMigrationsState(projectRoot)
	if err != nil {
		return fmt.Errorf("read %s: %w", migrationsStateFile, err)
	}
	state.Applied[id] = time.Now().UTC().Format(time.RFC3339)
	if err := writeMigrationsState(projectRoot, state); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "Recorded migration %q as applied at %s\n", id, state.Applied[id])
	return nil
}

// writePendingMigrationsHuman renders the pending list as
// human-readable text, oldest release first:
//
//	Pending migrations (binary v0.7.0):
//
//	  [ ] v0.5.0
//	      Title:       Deploy target moved into KCL
//	      Release:     v0.5.0
//	      To load:     forge skill load migrations/v0.5.0
//	      Once done:   forge project upgrade apply v0.5.0
//
// The two empty cases are told apart on purpose. "This binary ships no
// migrations" is a fact about forge; "your project needs none of them"
// is a fact about the project. Collapsing both into "up to date" tells a
// user whose project genuinely is mid-upgrade that everything is fine.
func writePendingMigrationsHuman(out io.Writer, pending []pendingMigration, shipped int) error {
	if len(pending) == 0 {
		if shipped == 0 {
			_, err := fmt.Fprintf(out,
				"No migrations shipped by %s %s — no release in the supported upgrade window carries a breaking change.\n",
				Name(), buildinfo.Version())
			return err
		}
		_, err := fmt.Fprintln(out, "Project is up to date.")
		return err
	}
	cliName := Name()
	binary := buildinfo.Version()
	_, _ = fmt.Fprintf(out, "Pending migrations (binary %s):\n\n", binary)
	for _, p := range pending {
		marker := "[ ]"
		if p.Applied {
			marker = "[x]"
		}
		_, _ = fmt.Fprintf(out, "  %s %s\n", marker, p.Meta.ID)
		if p.Meta.Title != "" {
			_, _ = fmt.Fprintf(out, "      Title:       %s\n", p.Meta.Title)
		}
		if p.Meta.Description != "" {
			_, _ = fmt.Fprintf(out, "      Description: %s\n", p.Meta.Description)
		}
		if p.Meta.Version != "" {
			_, _ = fmt.Fprintf(out, "      Release:     %s\n", p.Meta.Version)
		}
		if p.Applied {
			_, _ = fmt.Fprintf(out, "      Applied:     %s\n", p.AppliedAt)
			continue
		}
		_, _ = fmt.Fprintf(out, "      To load:     %s skill load %s\n", cliName, p.Meta.SkillPath)
		_, _ = fmt.Fprintf(out, "      Once done:   %s project upgrade apply %s\n", cliName, p.Meta.ID)
	}
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "Apply them in the order listed — each assumes the one above it has landed.")
	return nil
}

// writePendingMigrationsJSON emits the pending list as JSON. The shape
// is `{"binary_version", "shipped_migrations", "pending"}` so callers can
// tell "forge ships none" from "none apply to you" without re-deriving it
// from an empty array.
func writePendingMigrationsJSON(out io.Writer, pending []pendingMigration, shipped int) error {
	body := struct {
		BinaryVersion     string             `json:"binary_version"`
		ShippedMigrations int                `json:"shipped_migrations"`
		Pending           []pendingMigration `json:"pending"`
	}{
		BinaryVersion:     buildinfo.Version(),
		ShippedMigrations: shipped,
		Pending:           pending,
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(body)
}
