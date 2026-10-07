// Copyright (c) 2025 Reliant Labs
package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docsSkipMarker, on the line directly above a fenced YAML block, exempts it
// from TestDocsWorkflowExamplesValidate. It is an MDX comment so it renders as
// nothing. Use it only for blocks that are deliberately not a whole workflow
// (a fragment that references nodes defined elsewhere, a before/after pair
// where one half is intentionally not self-contained) and say why:
//
//	{/* docs-validate: skip — fragment, "start" is defined by the caller */}
var docsSkipMarker = regexp.MustCompile(`^\s*\{/\*\s*docs-validate:\s*skip\b`)

var (
	docsFenceOpen = regexp.MustCompile("^(\\s*)(`{3,})\\s*(ya?ml)\\b")
	// A block is a workflow definition (as opposed to config, an edge list, or
	// a lone node) when it declares nodes or the sequence: sugar at top level.
	docsWorkflowKey = regexp.MustCompile(`(?m)^(nodes|sequence):`)
	docsNameKey     = regexp.MustCompile(`(?m)^name:`)
	docsEntryKey    = regexp.MustCompile(`(?m)^(entry|sequence):`)
	docsFirstNodeID = regexp.MustCompile(`(?m)^nodes:\s*\n\s*-\s+id:\s*([A-Za-z0-9_-]+)`)
)

// wrapDocsFragment gives a workflow fragment the two things a page may leave
// out for brevity: a name, and an entry point (the first node). Everything
// else — node types, edges, reachability, CEL — is validated exactly as written.
func wrapDocsFragment(body string) string {
	var prefix string
	if !docsNameKey.MatchString(body) {
		prefix += "name: docs-example\n"
	}
	if !docsEntryKey.MatchString(body) {
		if m := docsFirstNodeID.FindStringSubmatch(body); m != nil {
			prefix += "entry: [" + m[1] + "]\n"
		}
	}
	return prefix + body
}

type docsYAMLBlock struct {
	file    string
	line    int
	body    string
	skipped bool
}

func extractDocsYAMLBlocks(t *testing.T, docsRoot string) []docsYAMLBlock {
	t.Helper()
	var blocks []docsYAMLBlock
	err := filepath.WalkDir(docsRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".mdx") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, _ := filepath.Rel(docsRoot, path)
		lines := strings.Split(string(data), "\n")
		for i := 0; i < len(lines); i++ {
			m := docsFenceOpen.FindStringSubmatch(lines[i])
			if m == nil {
				continue
			}
			indent, fence := m[1], m[2]
			start := i + 1
			skipped := false
			for j := i - 1; j >= 0; j-- {
				if strings.TrimSpace(lines[j]) == "" {
					continue
				}
				skipped = docsSkipMarker.MatchString(lines[j])
				break
			}
			var body []string
			i++
			for i < len(lines) {
				trimmed := strings.TrimSpace(lines[i])
				if strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, "`") == "" {
					break
				}
				// Blocks nested in <Tab>/<Step> are indented as a unit.
				body = append(body, strings.TrimPrefix(lines[i], indent))
				i++
			}
			blocks = append(blocks, docsYAMLBlock{file: rel, line: start, body: strings.Join(body, "\n"), skipped: skipped})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", docsRoot, err)
	}
	return blocks
}

// TestDocsWorkflowExamplesValidate runs every complete workflow YAML block in
// docs/**/*.mdx through the same validation as `reliant workflow validate`, so
// a docs example that main would reject fails here instead of in a reader's
// first custom workflow.
func TestDocsWorkflowExamplesValidate(t *testing.T) {
	docsRoot := filepath.Join("..", "..", "..", "docs")
	blocks := extractDocsYAMLBlocks(t, docsRoot)

	tmp := t.TempDir()
	validated, skipped := 0, 0
	for _, b := range blocks {
		if !docsWorkflowKey.MatchString(b.body) {
			continue
		}
		if b.skipped {
			skipped++
			continue
		}
		validated++
		name := fmt.Sprintf("%s:%d", b.file, b.line)
		t.Run(name, func(t *testing.T) {
			// One project per example: examples share names (most are
			// "docs-example"), and two files with one name in a project
			// is itself an error.
			dir := filepath.Join(tmp, fmt.Sprintf("example-%d", validated))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "example.yaml")
			if err := os.WriteFile(path, []byte(wrapDocsFragment(b.body)), 0o644); err != nil {
				t.Fatal(err)
			}
			res := validateWorkflowFile(path, dir)
			if !res.Valid {
				t.Errorf("docs example %s does not validate:\n  %s", name, strings.Join(res.Errors, "\n  "))
			}
		})
	}

	// Guard against the extractor silently matching nothing (a fence-syntax
	// change would otherwise turn this test into a no-op).
	if validated < 20 {
		t.Fatalf("only %d workflow examples found under %s (skipped %d); the extractor is probably broken", validated, docsRoot, skipped)
	}
	t.Logf("validated %d workflow examples, skipped %d", validated, skipped)
}
