// Copyright (c) 2025 Reliant Labs
package diff

import (
	"strings"

	"github.com/aymanbagabas/go-udiff"
)

// GenerateDiff creates a unified diff from two file contents
// workingDir should be passed from the calling context
func GenerateDiff(beforeContent, afterContent, fileName, workingDir string) (string, int, int) {
	// remove the working directory prefix and ensure consistent path format
	// this prevents issues with absolute paths in different environments
	if workingDir != "" {
		fileName = strings.TrimPrefix(fileName, workingDir)
	}
	fileName = strings.TrimPrefix(fileName, "/")

	var (
		unified   = udiff.Unified("a/"+fileName, "b/"+fileName, beforeContent, afterContent)
		additions = 0
		removals  = 0
	)

	lines := strings.SplitSeq(unified, "\n")
	for line := range lines {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			additions++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removals++
		}
	}

	return unified, additions, removals
}
