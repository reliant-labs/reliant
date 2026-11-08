package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestBufExceptionEditPreservesPolicy(t *testing.T) {
	for _, input := range []string{
		"version: v2\n", "version: v2\nlint:\n  use: [STANDARD]\n",
		"version: v2\nlint:\n  except: [] # untouched\n",
		"version: v2\nlint:\n  except: [PACKAGE_VERSION_SUFFIX] # untouched ]\n",
		"version: v2\nlint:\n  except: [\n    PACKAGE_VERSION_SUFFIX, # retain [comment]\n  ] # untouched\n",
		"version: v2\nlint:\n  except:\n    - PACKAGE_SAME_DIRECTORY # untouched\n",
	} {
		t.Run(input, func(t *testing.T) {
			rules := []string{"PACKAGE_SAME_DIRECTORY", "PACKAGE_SAME_GO_PACKAGE"}
			got, err := appendBufExceptions([]byte(input), rules)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := yaml.Unmarshal(got, &doc); err != nil {
				t.Fatalf("invalid YAML: %v\n%s", err, got)
			}
			for _, line := range strings.Split(strings.TrimSuffix(input, "\n"), "\n") {
				if strings.Contains(line, "except: [") || strings.HasPrefix(strings.TrimSpace(line), "]") {
					continue
				}
				if !strings.Contains(string(got), line) {
					t.Errorf("lost original line %q:\n%s", line, got)
				}
			}
			again, err := appendBufExceptions(got, rules)
			if err != nil {
				t.Fatal(err)
			}
			if string(again) != string(got) {
				t.Fatal("non-idempotent edit")
			}
		})
	}
}

func TestDistinctProtoPackagesLeaveBufUntouched(t *testing.T) {
	root := t.TempDir()
	writeServiceProto(t, root, "trading", "services.trading.v1")
	before := "version: v2\nlint:\n  use: [STANDARD]\n"
	path := filepath.Join(root, "buf.yaml")
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateServiceFiles(root, "example.com/app", "access", "app"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != before {
		t.Fatalf("distinct packages weakened Buf policy:\n%s", got)
	}
}
