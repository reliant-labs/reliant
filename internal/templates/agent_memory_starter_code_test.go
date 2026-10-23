// agent_memory_starter_code_test.go — keeps the "the scaffolded UI is
// starter code" note in the channels an agent actually reads.
//
// The note exists because of a specific, repeatable failure: forge prints
// it at the end of `forge project new`, and agents routinely run that
// command with stdout redirected to /dev/null. The guidance is then never
// read by anyone, the agent treats forge's entity-tile dashboard and
// generated CRUD pages as finished product work, and that is what ships.
//
// stdout is a broadcast nobody is required to receive. The project memory
// file is the durable channel: it is auto-loaded into EVERY agent session
// for the scaffolded project, so a note there is read whether or not the
// scaffold's output survived. That is why this guard is on the TEMPLATE
// and not on the printed block — the printed line is a courtesy, the file
// is the contract.
//
// Same philosophy as frontend_design_contract_test.go: a rule the scaffold
// states and then breaks is worse than no rule. Here the failure mode is
// quieter — a rule the scaffold never states at all, in the one file
// guaranteed to be read.
package templates

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// The agent-memory template forge ships is reliant.md.tmpl. For
// `--harness=reliant` (the default) Reliant renders it in-memory at session
// start (forgecli.RenderProjectMemory) and no file is written; for the other
// harnesses it is written to CLAUDE.md / AGENTS.md / .cursorrules /
// .github/copilot-instructions.md. Either way it is the one memory channel.
const memoryTemplate = "reliant.md.tmpl"

// starterCodeSources are the places the note must appear, and the reason
// each one matters. A reader arrives at exactly one of them depending on
// what they were doing, so the note has to be in all three.
func starterCodeSources() []struct{ path, why string } {
	return []struct{ path, why string }{
		{
			path: memoryTemplate,
			why: "the top-level agent-memory file (CLAUDE.md / AGENTS.md / .cursorrules), auto-loaded into " +
				"every session. `forge project new` also prints this guidance, but agents habitually " +
				"redirect that stdout to /dev/null — this file is the channel that survives",
		},
		{
			path: "skills/forge/SKILL.md",
			why:  "the entry skill, where a reader lands right after scaffolding with the frontend in front of them for the first time",
		},
		{
			path: "skills/forge/frontend/SKILL.md",
			why:  "where a reader lands when they are about to WRITE frontend code, which is the moment the distinction bites",
		},
	}
}

// renderStarterCodeSource returns one source's text. The memory file is a
// template (it interpolates the project and CLI names); the skills are
// plain files.
func renderStarterCodeSource(t *testing.T, path string) string {
	t.Helper()
	if path != memoryTemplate {
		content, err := ProjectTemplates().Get(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(content)
	}
	content, err := ProjectTemplates().Render(memoryTemplate, struct {
		Name string
		CLI  string
	}{Name: "testproject", CLI: "forge"})
	if err != nil {
		t.Fatalf("render %s: %v", memoryTemplate, err)
	}
	return string(content)
}

// TestScaffoldGuidanceCallsTheFrontendStarterCode asserts each source says
// the scaffolded UI is starter code AND points at the skill that explains
// how to finish it. Saying only the first half leaves a reader told to
// throw the UI away with no account of what replacing it involves.
func TestScaffoldGuidanceCallsTheFrontendStarterCode(t *testing.T) {
	t.Parallel()

	for _, src := range starterCodeSources() {
		text := renderStarterCodeSource(t, src.path)
		lower := strings.ToLower(text)

		if !strings.Contains(lower, "starter code") {
			t.Errorf("%s never calls the scaffolded frontend \"starter code\".\n\nThis is %s.\n\n"+
				"Without the note an agent reads forge's entity-tile dashboard and generated CRUD pages as a finished "+
				"design and ships them. Restore the note, or delete this guard if forge has genuinely stopped making "+
				"the claim — but do not leave the claim unmade in a file this load-bearing.",
				src.path, src.why)
		}
		if !strings.Contains(text, "frontend/design") {
			t.Errorf("%s calls the scaffold starter code but never names the `frontend/design` skill.\n\nThis is %s.\n\n"+
				"\"Rewrite this\" without a next step is an instruction to improvise, which is the exact outcome "+
				"`frontend/design` exists to prevent — it requires a brief from the user before any aesthetic is invented.",
				src.path, src.why)
		}
	}
}

// TestAgentMemoryNamesWhatIsPlaceholder holds the memory file to the
// specific version of the claim.
//
// "The UI is starter code" alone is too abstract to act on: a reader who
// cannot tell which files are placeholder and which are load-bearing
// either rewrites nothing or rewrites the generated hooks. The note has to
// name the surfaces, and it has to say that rewriting them is expected —
// otherwise an agent trained to leave scaffolds alone reads a full rewrite
// as going off-script.
func TestAgentMemoryNamesWhatIsPlaceholder(t *testing.T) {
	t.Parallel()

	text := renderStarterCodeSource(t, memoryTemplate)
	lower := strings.ToLower(text)

	// The placeholder surfaces, each named so a reader knows where to look.
	for _, surface := range []string{"dashboard", "nav", "sign-in"} {
		if !strings.Contains(lower, surface) {
			t.Errorf("%s does not name the %s among the placeholder surfaces. "+
				"A reader who cannot tell placeholder from load-bearing rewrites the wrong files, or none of them.",
				memoryTemplate, surface)
		}
	}

	// Rewriting is sanctioned, not a deviation.
	if !strings.Contains(lower, "expected") {
		t.Errorf("%s names the placeholder surfaces but never says rewriting them is EXPECTED. "+
			"Agents are told everywhere else not to fight the scaffold; without that word the note reads as "+
			"a description rather than permission, and the placeholder UI ships.", memoryTemplate)
	}

	// Why forge ships something obviously neutral. Without this a reader
	// concludes forge simply has no design opinion and fills the vacuum.
	if !strings.Contains(lower, "neutral") {
		t.Errorf("%s does not explain that forge withholds a visual identity DELIBERATELY (an invented one is harder "+
			"to replace than an obviously neutral one). Missing that, a reader reads the plain scaffold as an "+
			"oversight and invents an aesthetic to correct it — with no brief.", memoryTemplate)
	}
}

// TestAgentMemoryStarterCodeNoteStaysShort budgets the note.
//
// Every line of this file is re-read in every session of every agent
// working on the scaffolded project, so a note that sprawls taxes the work
// it is trying to improve. The section earns its place at roughly a
// screenful; past that it should be cut down and the detail moved to
// `frontend/design`, which is loaded on demand.
func TestAgentMemoryStarterCodeNoteStaysShort(t *testing.T) {
	t.Parallel()

	const maxNoteLines = 22

	text := renderStarterCodeSource(t, memoryTemplate)
	lines := strings.Split(text, "\n")

	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "## ") && strings.Contains(strings.ToLower(line), "starter code") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s has no `## …starter code…` section heading — TestScaffoldGuidanceCallsTheFrontendStarterCode "+
			"explains why the note must be there; this guard cannot measure what is absent", memoryTemplate)
	}

	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	if n := end - start; n > maxNoteLines {
		t.Errorf("the starter-code section of %s is %d lines, over the %d-line budget. This file is auto-loaded into "+
			"EVERY agent session, so each line is a permanent context cost. Tighten it and move the detail into "+
			"`frontend/design`, which is loaded only when someone is actually doing design work.",
			memoryTemplate, n, maxNoteLines)
	}
}

// TestNoScaffoldGuidanceClaimsTheUIIsFinished is the contradiction guard.
//
// The note only works if nothing else in the shipped guidance tells the
// opposite story. A skill that describes the scaffolded UI as
// production-ready would leave two standing claims, and a reader who hits
// that one first has forge's own word that the work is done.
//
// Scoped to phrases ABOUT the scaffolded UI: `frontend/design` calls the
// component LIBRARY "production-ready", which is a different subject and
// true — those components are finished; the screens assembled from them
// for your product are not.
func TestNoScaffoldGuidanceClaimsTheUIIsFinished(t *testing.T) {
	t.Parallel()

	claims := []string{
		"production-ready ui",
		"production ready ui",
		"production-ready frontend",
		"production-ready dashboard",
		"ready to ship ui",
		"finished design",
		"polished ui",
	}

	for _, src := range starterCodeSources() {
		lower := strings.ToLower(renderStarterCodeSource(t, src.path))
		for _, claim := range claims {
			idx := strings.Index(lower, claim)
			if idx < 0 {
				continue
			}
			// "not a finished design" is the note itself; only an
			// unqualified claim contradicts it.
			window := lower[max(0, idx-40):idx]
			if strings.Contains(window, "not ") || strings.Contains(window, "never ") {
				continue
			}
			t.Errorf("%s claims %q about the scaffolded UI while the same guidance calls it starter code. "+
				"Two standing claims means the reader believes whichever they hit first, and this one says the "+
				"work is done:\n  %s",
				src.path, claim, excerptAround(lower, idx))
		}
	}
}

// excerptAround returns a short window of text around idx for error output.
func excerptAround(s string, idx int) string {
	start := max(0, idx-60)
	end := min(len(s), idx+60)
	return fmt.Sprintf("…%s…", strings.TrimSpace(strings.ReplaceAll(s[start:end], "\n", " ")))
}

// TestStarterCodeNoteReachesEveryHarnessMemoryFile pins the reason ONE
// guard on ONE template is enough.
//
// `forge project new --harness claude` writes CLAUDE.md, `--harness codex`
// writes AGENTS.md, and so on. If any of those ever gained its own body
// template, this file would be guarding a document some harnesses never
// receive — and the agents on those harnesses would be exactly the ones
// shipping the placeholder UI.
func TestStarterCodeNoteReachesEveryHarnessMemoryFile(t *testing.T) {
	t.Parallel()

	// The per-harness memory FILENAMES (generator.Harness.MemoryFilePath).
	// Named here rather than imported to keep internal/templates free of a
	// dependency on internal/generator.
	for _, name := range []string{"CLAUDE.md", "AGENTS.md", ".cursorrules", "copilot-instructions.md"} {
		variant := filepath.Base(name) + ".tmpl"
		if variant == memoryTemplate {
			continue // the shared body itself, which is the thing being guarded
		}
		if _, err := ProjectTemplates().Get(variant); err == nil {
			t.Errorf("a harness-specific memory template %s now exists. writeProjectMetadata used to render %s for "+
				"EVERY harness, which is why the starter-code note only had to be written once. Either add %s to "+
				"starterCodeSources() so its copy is guarded too, or delete it and keep the single shared body.",
				variant, memoryTemplate, variant)
		}
	}
}
