package wfyaml

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestNoRawCoreYAMLKeyLiteralsInParserCodecFiles(t *testing.T) {
	targetFiles := []string{
		"nodes.go",
		"edges.go",
		"draft_parser.go",
	}

	allowList := map[string][]string{
		"nodes.go": {
			"Oneofs().ByName(protoreflect.Name(yamlKeyArgs))",
			"Find the \"args\" oneof descriptor",
			"The \"args\" key is NOT skipped",
			"have an \"args\" field",
			// Structural-arg promotion heuristic: "thread"/"project" are checked
			// as arg field names, not as YAML top-level keys.
			"hasInputArgsMapField",
		},
		"draft_parser.go": {
			"node top level instead of under \"args\"",
		},
	}

	for _, fileName := range targetFiles {
		filePath := filepath.Join(fileName)
		contentBytes, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatalf("read %s: %v", fileName, err)
		}
		content := string(contentBytes)

		coreKeys := []string{"id", "type", "condition", "thread", "timeout", "save_message", "outcome", "args", "from", "cases", "default", "to", "label"}
		for _, key := range coreKeys {
			matches := regexp.MustCompile(`"`+regexp.QuoteMeta(key)+`"`).FindAllStringIndex(content, -1)
			if len(matches) == 0 {
				continue
			}
			for _, match := range matches {
				lineStart := strings.LastIndex(content[:match[0]], "\n") + 1
				lineEnd := strings.Index(content[match[1]:], "\n")
				if lineEnd < 0 {
					lineEnd = len(content)
				} else {
					lineEnd += match[1]
				}
				lineText := content[lineStart:lineEnd]
				if strings.Contains(lineText, "yamlKey") {
					continue
				}
				if strings.Contains(lineText, "generated") {
					continue
				}
				if isAllowedLine(fileName, lineText, allowList[fileName]) {
					continue
				}
				t.Fatalf("raw core YAML key literal %q found in %s: %s", key, fileName, strings.TrimSpace(lineText))
			}
		}
	}
}

func isAllowedLine(fileName string, lineText string, allowedSubstrings []string) bool {
	for _, allow := range allowedSubstrings {
		if strings.Contains(lineText, allow) {
			return true
		}
	}
	return false
}
