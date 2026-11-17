package generator

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/checksums"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Shared wire packages deliberately have separate Go packages and directories.
// Reconcile only the two Buf rules that conflict with that layout, leaving all
// other project policy and formatting owned by the author.
func reconcileSharedPackageBuf(root, protoPath string, progress io.Writer) error {
	pkg := readProtoPackage(protoPath)
	if pkg == "" {
		return nil
	}
	paths, err := filepath.Glob(filepath.Join(root, "proto", "services", "*", "*", "*.proto"))
	if err != nil {
		return err
	}
	shared := false
	for _, path := range paths {
		if filepath.Dir(path) != filepath.Dir(protoPath) && readProtoPackage(path) == pkg {
			shared = true
			break
		}
	}
	if !shared {
		return nil
	}
	cs, err := checksums.Load(root)
	if err != nil {
		return err
	}
	rules := []string{"PACKAGE_SAME_DIRECTORY", "PACKAGE_SAME_GO_PACKAGE"}
	if cs.IsDisowned("buf.yaml") {
		fmt.Fprintf(progressOrDiscard(progress), "  ⚠ buf.yaml is disowned; shared proto package %s needs lint.except:\n    - %s\n    - %s\n", pkg, rules[0], rules[1])
		return nil
	}
	path := filepath.Join(root, "buf.yaml")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		fmt.Fprintf(progressOrDiscard(progress), "  ⚠ buf.yaml is missing; shared proto package %s needs lint.except: [%s, %s]\n", pkg, rules[0], rules[1])
		return nil
	}
	if err != nil {
		return err
	}
	updated, err := appendBufExceptions(raw, rules)
	if err != nil {
		return fmt.Errorf("buf.yaml shared-package exceptions: %w", err)
	}
	if bytes.Equal(updated, raw) {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(path, updated, info.Mode().Perm()); err != nil {
		return err
	}
	fmt.Fprintf(progressOrDiscard(progress), "  ✓ buf.yaml lint.except updated for shared proto package %s (%s, %s)\n", pkg, rules[0], rules[1])
	return nil
}

// Locate via YAML nodes; splice only at the exception list, never serialize
// the document (which would erase comments, unknown keys and user formatting).
func appendBufExceptions(raw []byte, rules []string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected a mapping")
	}
	root := doc.Content[0]
	lint := mappingValue(root, "lint")
	if lint == nil {
		return []byte(strings.TrimRight(string(raw), "\n") + "\nlint:\n  except: [" + strings.Join(rules, ", ") + "]\n"), nil
	}
	if lint.Kind != yaml.MappingNode || lint.Style&yaml.FlowStyle != 0 {
		return nil, fmt.Errorf("lint must be a block mapping; add lint.except: [%s] manually", strings.Join(rules, ", "))
	}
	except := mappingValue(lint, "except")
	var missing []string
	for _, rule := range rules {
		found := false
		if except != nil {
			for _, n := range except.Content {
				if n.Value == rule {
					found = true
				}
			}
		}
		if !found {
			missing = append(missing, rule)
		}
	}
	if len(missing) == 0 {
		return raw, nil
	}
	lines := strings.Split(string(raw), "\n")
	if except == nil {
		indent := lint.Content[0].Column - 1
		at := blockEndLine(lint)
		addition := strings.Repeat(" ", indent) + "except: [" + strings.Join(missing, ", ") + "]"
		lines = append(lines[:at], append([]string{addition}, lines[at:]...)...)
	} else if except.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("lint.except must be a sequence")
	} else if except.Style&yaml.FlowStyle != 0 {
		// Find the first closing bracket outside a quoted scalar or comment;
		// a trailing comment may itself contain brackets.
		end, close, err := bufFlowSequenceEnd(lines, except.Line-1, except.Column-1)
		if err != nil {
			return nil, err
		}
		prefix := bufFlowAppendPrefix(lines, except)
		lines[end] = lines[end][:close] + prefix + strings.Join(missing, ", ") + lines[end][close:]
	} else {
		at := blockEndLine(except)
		indent := except.Column - 1
		var additions []string
		for _, rule := range missing {
			additions = append(additions, strings.Repeat(" ", indent)+"- "+rule)
		}
		lines = append(lines[:at], append(additions, lines[at:]...)...)
	}
	return []byte(strings.Join(lines, "\n")), nil
}

func bufFlowAppendPrefix(lines []string, sequence *yaml.Node) string {
	if len(sequence.Content) == 0 {
		return ""
	}
	// Multiline YAML flow lists may already have a trailing comma.
	last := sequence.Content[len(sequence.Content)-1]
	tail := lines[last.Line-1][last.Column-1:]
	if comment := strings.Index(tail, "#"); comment >= 0 {
		tail = tail[:comment]
	}
	if strings.HasSuffix(strings.TrimSpace(tail), ",") {
		return ""
	}
	return ", "
}

func bufFlowSequenceEnd(lines []string, startLine, startColumn int) (int, int, error) {
	var quote rune
	escaped := false
	for line := startLine; line < len(lines); line++ {
		for column, r := range []rune(lines[line]) {
			if line == startLine && column <= startColumn {
				continue
			}
			if escaped {
				escaped = false
				continue
			}
			if quote == '"' && r == '\\' {
				escaped = true
				continue
			}
			if quote != 0 {
				if r == quote {
					quote = 0
				}
				continue
			}
			if r == '#' {
				break
			}
			if r == '\'' || r == '"' {
				quote = r
				continue
			}
			if r == ']' {
				// Columns above are rune offsets; string splicing needs bytes.
				return line, len(string([]rune(lines[line])[:column])), nil
			}
		}
	}
	return 0, 0, fmt.Errorf("cannot locate lint.except closing bracket")
}
