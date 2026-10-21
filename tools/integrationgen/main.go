// Copyright (c) 2025 Reliant Labs

// Command integrationgen derives an integration's webhook event typing from
// one source: a pinned upstream schema plus per-event projection specs.
//
//	internal/integrations/webhook/<provider>/projection/   the specs
//	tools/integrationgen/upstream/<package>/               the vendored upstream schema
//
// From them it writes
//
//	internal/integrations/webhook/<provider>/zz_projection_generated.go   the parser's projections
//	internal/integrations/catalog/<provider>/events/<id>.yaml       each event's data schema and attributes
//	internal/integrations/catalog/<provider>/schemas/*_schema.yaml  the shared objects
//
// so the schema a filter is checked against and the code that fills it cannot
// drift. See dev-docs/integration-manifests.md.
//
// Usage: go run ./tools/integrationgen [-check] [provider...]
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	check := flag.Bool("check", false, "write nothing; fail when a generated file is out of date")
	rootFlag := flag.String("root", "", "repository root (default: found from the working directory)")
	flag.Parse()
	root := *rootFlag
	if root == "" {
		var err error
		if root, err = findRoot(); err != nil {
			fatal(err)
		}
	}
	providers := flag.Args()
	if len(providers) == 0 {
		providers = []string{"github"}
	}
	var stale []string
	for _, p := range providers {
		out, err := generate(root, p)
		if err != nil {
			fatal(fmt.Errorf("%s: %w", p, err))
		}
		s, err := apply(root, out, *check)
		if err != nil {
			fatal(err)
		}
		stale = append(stale, s...)
	}
	if *check && len(stale) > 0 {
		fatal(fmt.Errorf("generated integration files are out of date; run `make generate-integrations` and commit:\n  %s", strings.Join(stale, "\n  ")))
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "integrationgen:", err)
	os.Exit(1)
}

func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above the working directory")
		}
		dir = parent
	}
}

// apply writes the output, or with check reports the files that differ. A
// file the generator marked in a directory it owns, which no output covers any
// more (an event whose spec was deleted), is stale too.
func apply(root string, out *output, check bool) (stale []string, err error) {
	paths := make([]string, 0, len(out.files))
	for p := range out.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		full := filepath.Join(root, p)
		old, rerr := os.ReadFile(full)
		if rerr == nil && bytes.Equal(old, out.files[p]) {
			continue
		}
		if check {
			stale = append(stale, p)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(full, out.files[p], 0o644); err != nil {
			return nil, err
		}
	}
	for _, dir := range out.owned {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			rel := filepath.ToSlash(filepath.Join(dir, e.Name()))
			if _, ok := out.files[rel]; ok || e.IsDir() {
				continue
			}
			b, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil || !bytes.Contains(b, []byte(generatedMarker)) {
				continue
			}
			if check {
				stale = append(stale, rel+" (no spec generates it)")
				continue
			}
			if err := os.Remove(filepath.Join(root, rel)); err != nil {
				return nil, err
			}
		}
	}
	return stale, nil
}
