package testdocker

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestParsePublishedPort(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want int
		ok   bool
	}{
		{"127.0.0.1:49153", 49153, true},
		{"127.0.0.1:49153\n[::1]:49153\n", 49153, true},
		{"", 0, false},
		{"Error: No public port '18080/tcp' published for c", 0, false},
	} {
		got, ok := parsePublishedPort(tc.out)
		if got != tc.want || ok != tc.ok {
			t.Errorf("parsePublishedPort(%q) = %d, %t; want %d, %t", tc.out, got, ok, tc.want, tc.ok)
		}
	}
}

// prePickedHostPort matches a test publishing a container port on a host port
// it picked itself: a `-p` flag built with fmt.Sprintf from a loopback
// address and a %d port.
var prePickedHostPort = regexp.MustCompile(`"-p",\s*fmt\.Sprintf\("127\.0\.0\.1:%d:`)

// TestNoPrePickedDockerHostPorts keeps the free-port race (see the package
// doc) out of forge's tests: every test that publishes a container port lets
// docker pick the host port.
func TestNoPrePickedDockerHostPorts(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root %s: %v", root, err)
	}
	var found []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", "vendor", ".forge", ".tools":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if prePickedHostPort.Match(src) {
			rel, _ := filepath.Rel(root, path)
			found = append(found, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(found)
	if len(found) > 0 {
		t.Errorf("these tests publish a container port on a host port they picked first:\n\n  %s\n\n"+
			"Let docker pick it (internal/testdocker):\n"+
			"  args = append(args, testdocker.Publish(<container port>)...)\n"+
			"  port := testdocker.PublishedPort(t, <container>, <container port>)\n"+
			"A port picked first can be taken before docker binds it.", strings.Join(found, "\n  "))
	}
}
