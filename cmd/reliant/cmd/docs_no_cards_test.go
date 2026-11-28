// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docsCardTag is Mintlify's <Card> or <CardGroup> opening tag.
var docsCardTag = regexp.MustCompile(`<Card(Group)?\b`)

// TestDocsUseNoCards keeps Mintlify's <Card> and <CardGroup> out of the docs
// (owner, 2026-11-25: the boxes read badly, and docs rarely need them). A
// "Next steps" or related-pages block is a Markdown link list:
//
//   - [Page title](/path/to/page): what the reader gets there
func TestDocsUseNoCards(t *testing.T) {
	docsRoot := filepath.Join("..", "..", "..", "docs")
	scanned := 0
	err := filepath.WalkDir(docsRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".mdx") {
			return nil
		}
		scanned++
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		rel, _ := filepath.Rel(docsRoot, path)
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		inFence := false // a code sample may show a React <Card>
		for n := 1; sc.Scan(); n++ {
			line := sc.Text()
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence = !inFence
				continue
			}
			if inFence {
				continue
			}
			if m := docsCardTag.FindString(line); m != "" {
				t.Errorf("docs/%s:%d: %s> is not used in the docs: write a Markdown link list instead, one `- [Title](/path): description` per page", rel, n, m)
			}
		}
		return sc.Err()
	})
	if err != nil {
		t.Fatalf("walking %s: %v", docsRoot, err)
	}
	if scanned < 100 {
		t.Fatalf("only %d .mdx files found under %s; the walk is probably broken", scanned, docsRoot)
	}
}
