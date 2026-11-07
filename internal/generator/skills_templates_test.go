package generator_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/templates"
)

// forgeCommandRE matches a `forge ` invocation followed by 1-3 lowercase
// sub-command tokens. Tokens are [a-z][a-z-]* so anything starting with a
// non-letter (flags like -t, paths like handlers/..., quoted strings, comments
// starting with #, positional placeholders like <name>) terminates the match.
//
// `forge` must NOT be preceded by an identifier char or `-`, so names like
// "cp-forge" in example output (e.g. namespace tables) don't get parsed as a
// `forge` invocation. We can't use \b here — \b matches between `-` and `f`,
// causing "cp-forge cp-forge-dev" to be read as "forge cp-forge-dev".
//
// The separators are `[ \t]+`, never `\s+`: a command is one line. With `\s+`
// a KCL block opening `import forge` glued the next line's `output = ...` on
// and reported a phantom `forge output` command.
//
// Examples of what this matches:
//
//	"forge generate"              -> ["generate"]
//	"forge db migrate up"         -> ["db", "migrate", "up"]
//	"forge debug break file:42"   -> ["debug", "break"]
//	"forge env up --background"   -> ["env", "up"]
//	"forge package new <name>"    -> ["package", "new"]
var forgeCommandRE = regexp.MustCompile(`(?:^|[^\w-])forge[ \t]+([a-z][a-z-]*(?:[ \t]+[a-z][a-z-]*){0,2})`)

// A command reference never spans lines. `import forge` is how every KCL env
// file opens, and the line after it is usually `output = ...` — read across
// the newline, that is a `forge output` command that does not exist.
func TestForgeCommandREStaysOnOneLine(t *testing.T) {
	if m := forgeCommandRE.FindAllStringSubmatch("import forge\n\noutput = forge.render(b)\n", -1); len(m) != 0 {
		t.Fatalf("a KCL `import forge` line must not match a command, got %q", m)
	}
	m := forgeCommandRE.FindStringSubmatch("forge env deploy prod\nnext line")
	if m == nil || m[1] != "env deploy prod" {
		t.Fatalf("a one-line command must still match whole, got %q", m)
	}
}

// extractFencedBlocks returns the concatenated contents of every fenced code
// block (``` ... ```) in a markdown document. We only scan for `forge`
// commands inside fenced blocks because those are shell-runnable examples a
// user could copy-paste; prose outside fences is allowed to mention commands
// that don't exist (e.g. "there is no `forge rename`").
func extractFencedBlocks(md string) string {
	var out strings.Builder
	lines := strings.Split(md, "\n")
	inFence := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

// TestSkillTemplatesReferOnlyToRealCommands walks every SKILL.md in the
// embedded project template FS and verifies that every `forge ...` command
// reference inside a fenced code block resolves to a real cobra subcommand.
// This catches hallucinated commands, typos (like "step-in" vs "stepin"), and
// drift between the CLI and the scaffolded skill docs. Prose outside fenced
// blocks is allowed to mention missing commands for teaching purposes.
func TestSkillTemplatesReferOnlyToRealCommands(t *testing.T) {
	root := cli.NewRootCmd()

	skillFiles, err := templates.ProjectTemplates().List("skills")
	if err != nil {
		t.Fatalf("ListProjectTemplates(skills) error = %v", err)
	}
	if len(skillFiles) == 0 {
		t.Fatalf("no skill templates found")
	}

	var failures []string

	for _, rel := range skillFiles {
		if !strings.HasSuffix(rel, "SKILL.md") {
			continue
		}
		content, err := templates.ProjectTemplates().Get("skills/" + rel)
		if err != nil {
			t.Fatalf("GetProjectTemplate(%s) error = %v", rel, err)
		}

		fenced := extractFencedBlocks(string(content))
		// The canonical Tier-2 ownership banner ("yours: scaffolded once,
		// never touched again — forge will not overwrite this file") is
		// quoted verbatim in skill code examples; "forge will not ..." is
		// prose inside that fixed sentence, not a command reference.
		fenced = strings.ReplaceAll(fenced, "forge will not overwrite this file", "")
		matches := forgeCommandRE.FindAllStringSubmatch(fenced, -1)
		for _, m := range matches {
			tokens := strings.Fields(m[1])
			if len(tokens) == 0 {
				continue
			}

			// Try to resolve the full token path against the cobra
			// command tree. Progressively shorten from the right: if
			// "db migrate up" doesn't resolve, we don't want to claim
			// "db" is wrong — we want to report the full failing path.
			// But if "debug stepin" fully resolves, we stop there and
			// don't try "debug stepin locals".
			//
			// Use Find: it returns the deepest matching command and
			// leftover args. A fully-resolved path has no leftover
			// non-flag tokens.
			found, leftover, ferr := root.Find(tokens)
			if ferr != nil {
				failures = append(failures, fmt.Sprintf("%s: forge %s -> Find error: %v", rel, strings.Join(tokens, " "), ferr))
				continue
			}
			// leftover may contain trailing positional args; strip any
			// that aren't recognized subcommands of `found`. We allow
			// leftover tokens here because many skills write things
			// like `forge env deploy prod` where `prod` is a positional
			// arg, not a subcommand. The invariant we want is: every
			// token that IS a command name resolved to a real command.
			// So: check that Find walked as deep as possible, meaning
			// no leftover token is a child of `found`.
			for _, lt := range leftover {
				if child, _, _ := found.Find([]string{lt}); child != nil && child != found {
					failures = append(failures, fmt.Sprintf("%s: forge %s -> leftover %q is a child of %q but Find stopped short", rel, strings.Join(tokens, " "), lt, found.CommandPath()))
				}
			}

			// If Find returned the root command itself, no match at all.
			if found == root {
				failures = append(failures, fmt.Sprintf("%s: forge %s -> no matching subcommand (root returned)", rel, strings.Join(tokens, " ")))
				continue
			}

			// Sanity: the found command's name must equal one of the tokens.
			nameFound := false
			for _, tok := range tokens {
				if found.Name() == tok {
					nameFound = true
					break
				}
			}
			if !nameFound {
				failures = append(failures, fmt.Sprintf("%s: forge %s -> resolved to %q which is not in the token list", rel, strings.Join(tokens, " "), found.CommandPath()))
			}
		}
	}

	if len(failures) > 0 {
		t.Fatalf("skill templates reference commands that do not exist in the CLI:\n  %s", strings.Join(failures, "\n  "))
	}
}

// TestSkillTemplatesHaveNoGoTemplateDirectives verifies that skill files are
// treated as plain markdown, not Go templates. Skills contain literal examples
// like {{.Name}} in prose, so accidentally rendering them as templates would
// silently blank out those examples or fail noisily. This test guards against
// someone renaming a skill to SKILL.md.tmpl or adding template directives.
func TestSkillTemplatesHaveNoGoTemplateDirectives(t *testing.T) {
	skillFiles, err := templates.ProjectTemplates().List("skills")
	if err != nil {
		t.Fatalf("ListProjectTemplates(skills) error = %v", err)
	}

	for _, rel := range skillFiles {
		if strings.HasSuffix(rel, ".tmpl") {
			t.Errorf("skill file %q has .tmpl suffix; skills must be copied verbatim, not rendered", rel)
		}
	}
}

// TestProjectGeneratorWritesMCPConfigExample verifies that the opt-in example
// file is written alongside the active .mcp.json. The example is
// documentation — it lists servers a user may want to enable but whose
// dependencies aren't required for basic development.
func TestProjectGeneratorWritesMCPConfigExample(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mcp-example-app")
	g := generator.NewProjectGenerator("mcp-example-app", root, "example.com/mcp-example-app")
	if err := g.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	examplePath := filepath.Join(root, ".mcp.json.example")
	if _, err := os.Stat(examplePath); err != nil {
		t.Fatalf("expected %s to exist, err = %v", examplePath, err)
	}

	b, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", examplePath, err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		t.Fatalf(".mcp.json.example must not be empty")
	}
}
