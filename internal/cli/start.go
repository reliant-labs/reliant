// Package cli — `forge start`: the greenfield brief, in one call.
//
// WHY THIS EXISTS. A measured dogfood run spent six documentation calls
// (`forge --help`, `forge project --help`, `forge skill --help`,
// `forge skill list`, `forge skill load forge`, `forge project new --help`,
// `forge project annotations`,
// `forge skill load proto`) before authoring its first proto. Every one of
// those surfaces is correct and none is hidden; the cost is that reaching
// the four facts an agent actually needs first — how to lay out the
// project, how an entity is marked, that custom rpcs are never injected,
// and how a FK diamond is declared — takes one invocation per surface.
//
// Concatenating those sources is not an option: measured together they run
// past the consumer's 24 KB output ceiling, so the tail (the pointers
// onward) would be cut. This command is therefore a CURATION — the
// pre-first-proto subset, and nothing else.
//
// NOTHING HERE IS TRANSCRIBED. A second hand-written copy of the greenfield
// prose would drift from the skill that owns it, so every section is
// composed at run time from the source that already owns it:
//
//   - the sequence, the three authoring rules and the shape          the
//     embedded skills/forge/SKILL.md, extracted by heading. A retitled or
//     deleted heading is a hard error, never a silent omission.
//   - the `project new` flags     looked up on the real cobra command, so a
//     renamed flag fails loudly instead of advertising one that is gone.
//   - the proto→column vocabulary  scaffold.ProtoSQLMappings(), the same
//     iteration `forge project annotations` publishes.
//   - the entity markers          markerSpecs(), whose names come from the
//     registries the proto scanner spells its markers from.
//
// The deliberate omissions are everything an agent does not need before its
// first proto: the scaffold tree, the component catalog, deploy, ports,
// project kinds, and the skill catalog itself. Each is named, not inlined.
package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	entityscaffold "github.com/reliant-labs/forge/internal/scaffold"
)

// briefSkillPath is the skill whose prose this brief composes from.
const briefSkillPath = "forge"

// briefSections are the headings lifted verbatim from briefSkillPath, in
// output order. They are the pre-first-proto half of that skill: the
// sequence, the three authoring rules, and the two-truths shape.
//
// Extraction is by exact heading text. That is deliberately brittle:
// renaming a heading in the skill must break this command loudly rather
// than quietly ship a brief with a hole where the custom-rpc rule was.
var briefSections = []string{
	"Greenfield, end to end",
	"Authoring a service proto",
	"The shape, in four lines",
}

// briefNewFlags are the `forge project new` flags a greenfield invocation
// actually needs, with the gloss this brief prints for each.
//
// The NAMES are looked up on the real command (see newProjectFlagLines);
// only the gloss is a literal, because the real flag usage strings are
// paragraph-length and written for `--help`, not for a brief. The rest of
// the flag set is a `--help` call away and none of it is load-bearing
// before the first proto.
var briefNewFlags = []struct {
	name  string
	gloss string
}{
	{"mod", "Go module path (`github.com/acme/my-app`). Defaults to the project name; set it to the path you will publish under."},
	{"service", "Repeatable. One empty proto stub per service — **and the only way to get `cmd/<bin>/main.go` wired for you.** Name after domain entities (`customers`, `jobs`), never after the binary."},
	{"frontend", "Repeatable. One Next.js frontend each."},
	{"in-place", "Scaffold into the current directory instead of creating a subdirectory. Takes no positional name."},
	{"name", "Name an `--in-place` project whose directory is a worktree or branch name rather than the product."},
}

// newStartCmd builds `forge start`. The brief is forge's own vocabulary
// plus forge's own prose, so it reads no project files and runs anywhere —
// including in the empty directory it tells you how to fill.
func newStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Print the greenfield brief: empty directory to authored protos, in one call",
		Long: `Print the greenfield brief.

Everything needed to go from an empty directory to authored protos, and
nothing else: the end-to-end sequence, the 'project new' flags that matter,
how an entity is marked and what scaffold births from it, the rule that
forge injects CRUD rpcs ONLY, the proto->column vocabulary, and the FK
diamond declaration.

This command PRINTS. It creates nothing and changes nothing.

Depth is named rather than inlined — 'forge skill load <name>' prints the
copy this binary ships. Three sibling dumps cover the rest of the surface:

  forge project capabilities  every verb, analyzer and marker
  forge project annotations   the full entity-authoring spec
  forge skill list            the skill catalog`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStart(cmd.OutOrStdout())
		},
	}
}

func runStart(w io.Writer) error {
	brief, err := buildStartBrief()
	if err != nil {
		return err
	}
	// Match `forge skill load`: rewrite bare `forge ` command references
	// when this binary is mounted under another name (`reliant forge`),
	// so every command in the brief is copy-pasteable as printed.
	_, err = w.Write(rewriteForgeCommands([]byte(brief), cmdutil.Name()))
	return err
}

// buildStartBrief assembles the whole document.
func buildStartBrief() (string, error) {
	skill, err := loadForgeShippedSkill(briefSkillPath)
	if err != nil {
		return "", fmt.Errorf("load the %q skill this brief composes from: %w", briefSkillPath, err)
	}

	sections := make(map[string]string, len(briefSections))
	for _, heading := range briefSections {
		body, ok := extractMarkdownSection(string(skill), heading)
		if !ok {
			return "", fmt.Errorf(
				"the %q skill no longer has a %q section, so `forge start` cannot compose the brief from it "+
					"(fix: restore the heading, or update briefSections in internal/cli/start.go to the new one)",
				briefSkillPath, heading)
		}
		sections[heading] = body
	}

	flags, err := newProjectFlagLines()
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(briefHeader)
	b.WriteString(sections["Greenfield, end to end"])
	b.WriteString("\n## `forge project new` — the flags that matter\n\n")
	b.WriteString(flags)
	b.WriteString("\nEverything else (`--kind`, `--harness`, `--disable`, `--binary`, …) has a\nworking default: `forge project new --help`.\n\n")
	b.WriteString(sections["Authoring a service proto"])
	b.WriteString(fieldTypeBriefTable())
	b.WriteString(entityMarkerBriefTable())
	b.WriteString("\n")
	b.WriteString(sections["The shape, in four lines"])
	b.WriteString(briefFooter)
	return b.String(), nil
}

const briefHeader = `# forge — greenfield brief

Empty directory to authored protos. Read once, then build. Depth is named at
the end; do not load it preemptively.

`

const briefFooter = `
## Where depth lives

Load only when you reach the topic. ` + "`forge skill load <name>`" + ` prints the copy
this binary ships, which is authoritative over any copy on disk.

| Load | When |
|---|---|
| ` + "`forge`" + ` | the entry skill in full: the scaffold noun catalog, running the stack, the pre-flight checklist |
| ` + "`proto`" + ` | the full proto ruleset: field numbering, ` + "`optional`" + ` on list filters, one service per file |
| ` + "`db`" + ` | migrations, evolving a schema, queries the generated CRUD cannot express |
| ` + "`db/seeding`" + ` | FK diamonds in depth, and ` + "`forge db seed`" + ` |
| ` + "`api`" + ` | writing a custom rpc's body on the handler ` + "`*Service`" + ` |
| ` + "`services`" + ` | adding a service after ` + "`project new`" + `, and the other scaffold nouns |
| ` + "`architecture`" + ` | where logic goes, and the composition root |
| ` + "`frontend/design`" + ` | before showing any scaffolded UI to a user — it asks for a brief first |
| ` + "`testing`" + ` | the test harness and the generated per-rpc test rows |
| ` + "`forge-libraries`" + ` | before writing any utility — adopt from ` + "`forge/pkg`" + ` rather than port |

Three dumps answer "what does forge already do?" without prose:

    forge project capabilities   every verb, lint analyzer and marker
    forge project annotations    the full entity-authoring spec
    forge project libraries      every forge/pkg package; add a name for its full API

` + "`forge skill list`" + ` is the whole catalog; ` + "`forge skill search <keyword>`" + ` finds one
by topic.

## Rules

- Draft the proto, run ` + "`forge scaffold`" + `, read the error, fix. Never reverse-engineer forge's ` + "`internal/**`" + ` for syntax.
- Never hand-edit ` + "`gen/`" + ` or any ` + "`*_gen.go`" + `. Fix the proto or the migration and regenerate.
- Declare schema in migrations, never in proto.
- Run ` + "`forge lint && go build ./...`" + ` before calling a phase done.
`

// extractMarkdownSection returns the `## <heading>` section of doc,
// including the heading line and every nested `###` subsection, up to the
// next same-level `## ` heading or end of document.
func extractMarkdownSection(doc, heading string) (string, bool) {
	want := "## " + heading
	lines := strings.Split(doc, "\n")

	start := -1
	for i, line := range lines {
		if strings.TrimRight(line, " \t") == want {
			start = i
			break
		}
	}
	if start < 0 {
		return "", false
	}

	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		// A same-level heading ends the section; `###` and deeper are
		// part of it. Guard against `###` matching the `## ` prefix.
		if strings.HasPrefix(lines[i], "## ") && !strings.HasPrefix(lines[i], "### ") {
			end = i
			break
		}
	}

	body := strings.Join(lines[start:end], "\n")
	return strings.TrimRight(body, "\n") + "\n", true
}

// newProjectFlagLines renders the briefNewFlags table, resolving every
// flag against the real `forge project new` command so a rename cannot
// leave the brief advertising a flag that no longer parses.
func newProjectFlagLines() (string, error) {
	cmd := newNewCmd()

	var b strings.Builder
	b.WriteString("| Flag | Purpose |\n|---|---|\n")
	for _, f := range briefNewFlags {
		if cmd.Flags().Lookup(f.name) == nil {
			return "", fmt.Errorf(
				"`forge project new` has no --%s flag, so `forge start` would advertise a flag that does not parse "+
					"(fix: update briefNewFlags in internal/cli/start.go)", f.name)
		}
		b.WriteString(fmt.Sprintf("| `--%s` | %s |\n", f.name, f.gloss))
	}
	return b.String(), nil
}

// fieldTypeBriefTable renders the proto→column vocabulary from the same
// iteration `forge project annotations` publishes, so a change to what a
// birth emits changes this brief with no edit here.
func fieldTypeBriefTable() string {
	var b strings.Builder
	b.WriteString("\n### Field type → column\n\n")
	b.WriteString("Applied once, at birth. The migration is yours from that moment: edit any\ncolumn freely afterwards, and evolve with a new migration.\n\n")
	b.WriteString("| Proto field | Column |\n|---|---|\n")
	for _, m := range entityscaffold.ProtoSQLMappings() {
		row := fmt.Sprintf("| %#q | %#q", m.Proto, m.SQL)
		if m.Notes != "" {
			row += " — " + m.Notes
		}
		b.WriteString(row + " |\n")
	}
	b.WriteString("\n`(buf.validate.field)` rules project onto the column as a CHECK and onto the\ngenerated form as a zod rule: `required`, `len`, `min_len`, `max_len`, `pattern`,\n`email`, `lt`/`lte`/`gt`/`gte`. Import `buf/validate/validate.proto` and forge\nvendors it on the next generate. Fixed-length codes take `string.len = N`, never\nequal `min_len`/`max_len` — `buf lint` rejects that. Full spec, plus the\nfield-level markers (`forge:read-only`, `forge:secret`) and the column markers\n(`forge:immutable`, `forge:version`, `forge:fill`): `forge project annotations`.\n")
	return b.String()
}

// entityMarkerBriefTable renders the message-level markers from
// markerSpecs(), whose names come from the registries the proto scanner
// reads. These three are the ones that shape the entity model, so they
// belong in a brief read before the model is authored.
func entityMarkerBriefTable() string {
	var b strings.Builder
	b.WriteString("\n### Entity markers\n\n")
	b.WriteString("A full-line comment immediately above `message X {`.\n\n")
	b.WriteString("| Marker | Effect |\n|---|---|\n")
	for _, m := range filterMarkers(markerSpecs(), "entity") {
		b.WriteString(fmt.Sprintf("| `// %s` | %s |\n", m.Name, m.Effect))
	}
	return b.String()
}
