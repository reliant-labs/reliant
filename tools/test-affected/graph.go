// Copyright (c) 2025 Reliant Labs
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

type listPkg struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	EmbedFiles []string
	Deps       []string
}

const listFields = "ImportPath,Dir,GoFiles,EmbedFiles,Deps"

// goListGenerators lists forge.yaml's generate-hook generators and every
// module package they compile (go list -deps): the code whose change makes a
// hook write something different. It is not the module's whole graph: that is
// forge's (`forge ci gate --plan`).
func goListGenerators(ctx context.Context, root string) ([]listPkg, error) {
	args := []string{"list", "-e", "-deps", "-json=" + listFields}
	for _, g := range forgeHookGenerators {
		args = append(args, "./"+g)
	}
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go %s: %w\n%s", strings.Join(args, " "), err, tail(stderr.String(), 20))
	}
	return decodeList(out)
}

func decodeList(out []byte) ([]listPkg, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	var pkgs []listPkg
	for {
		var p listPkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode go list: %w", err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// FileInfo is a Go or embedded file a hook generator compiles.
type FileInfo struct {
	Pkg string
}

// Graph is what Classify needs beyond forge's decision: the web import graph,
// the electron tests that name files outside electron/, and the code forge.yaml's
// generate hooks compile. LoadGraph builds it; the table tests build it by hand.
type Graph struct {
	Module string
	// DirPkg maps a repo-relative directory to the package there, for the
	// packages a hook generator compiles.
	DirPkg map[string]string
	// Files maps a repo-relative path to its package: the non-test Go and
	// embedded files of those packages.
	Files map[string]FileInfo
	// BuildDeps[G] is the set of module packages hook generator G compiles
	// (G included).
	BuildDeps map[string]map[string]bool
	// Web is the import graph of frontends/web/ (nil when frontends/web/ is absent).
	Web *WebGraph
	// ElectronText holds the source of electron tests that name files outside
	// electron/src (repo-relative path -> text).
	ElectronText map[string]string
}

// LoadGraph reads the hook generators' build closure with `go list`.
func LoadGraph(ctx context.Context, root, module string) (*Graph, error) {
	list, err := goListGenerators(ctx, root)
	if err != nil {
		return nil, err
	}
	return buildGraph(root, module, list), nil
}

func buildGraph(root, module string, list []listPkg) *Graph {
	g := &Graph{Module: module, DirPkg: map[string]string{}, Files: map[string]FileInfo{}, BuildDeps: map[string]map[string]bool{}}
	own := func(p listPkg) bool {
		return p.Dir != "" && (p.ImportPath == module || strings.HasPrefix(p.ImportPath, module+"/"))
	}
	for _, p := range list {
		if !own(p) {
			continue
		}
		rel, err := filepath.Rel(root, p.Dir)
		if err != nil {
			continue
		}
		dir := filepath.ToSlash(rel)
		g.DirPkg[dir] = p.ImportPath
		for _, names := range [][]string{p.GoFiles, p.EmbedFiles} {
			for _, n := range names {
				g.Files[path.Join(dir, n)] = FileInfo{Pkg: p.ImportPath}
			}
		}
		set := map[string]bool{p.ImportPath: true}
		for _, d := range p.Deps {
			if d == module || strings.HasPrefix(d, module+"/") {
				set[d] = true
			}
		}
		g.BuildDeps[p.ImportPath] = set
	}
	return g
}
