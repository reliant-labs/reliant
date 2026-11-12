// Copyright (c) 2025 Reliant Labs

package viewertest

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	_ "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// Every rpc of the app that takes a chat or a project has a Matrix row, and
// every row is such an rpc: a new one cannot ship without deciding what it
// answers a viewer.
func TestMatrixCoversEveryChatOrProjectRPC(t *testing.T) {
	want := map[string]bool{}
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if fd.Package() != "reliant.v1" {
			return true
		}
		for i := 0; i < fd.Services().Len(); i++ {
			sd := fd.Services().Get(i)
			for j := 0; j < sd.Methods().Len(); j++ {
				md := sd.Methods().Get(j)
				for _, key := range matrixKeys {
					if md.Input().Fields().ByName(protoreflect.Name(key)) != nil {
						want[string(sd.FullName())+"/"+string(md.Name())] = true
						break
					}
				}
			}
		}
		return true
	})
	var missing, stale []string
	for key := range want {
		if _, ok := Matrix[key]; !ok {
			missing = append(missing, key)
		}
	}
	for key := range Matrix {
		if !want[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	for _, key := range missing {
		t.Errorf("%s takes a chat or project and has no Matrix row: decide what it answers a viewer", key)
	}
	for _, key := range stale {
		t.Errorf("Matrix[%q] names no rpc that takes a chat or project: remove it", key)
	}
}

// Every service with a row a handler answers (Answered or AsOutsider) is run
// through RunService by a test in its handler package, so every row is
// exercised, not only declared.
func TestEveryMatrixServiceIsRun(t *testing.T) {
	root := filepath.Join("..", "handlers")
	run := regexp.MustCompile(`viewertest\.RunService\([^"]*"(reliant\.v1\.\w+)"`)
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range run.FindAllSubmatch(src, -1) {
			seen[string(m[1])] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	services := map[string]bool{}
	for key, row := range Matrix {
		if row == Answered || row == AsOutsider {
			svc, _, _ := strings.Cut(key, "/")
			services[svc] = true
		}
	}
	var missing []string
	for svc := range services {
		if !seen[svc] {
			missing = append(missing, svc)
		}
	}
	sort.Strings(missing)
	for _, svc := range missing {
		t.Errorf("no handler test runs viewertest.RunService(t, f, svc, %q, ...)", svc)
	}
}
