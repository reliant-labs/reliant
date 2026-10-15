// Copyright (c) 2025 Reliant Labs
package buildmode_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// reliant must never require the Zitadel SERVER module. We run Zitadel
// unmodified (AGPL-3.0) and reach it only over its HTTP/gRPC APIs; importing
// github.com/zitadel/zitadel (or any /vN) would pull the server's licence into
// our binaries. Other github.com/zitadel/* modules (oidc, zitadel-go: Apache-2.0)
// are separate modules and are not denied.
//
// go.mod and go.sum are read here, so the test cache notices an edit; go.sum
// lists every module of the graph that contributed a package or go.mod.
var zitadelServerModule = regexp.MustCompile(`^github\.com/zitadel/zitadel(/v[0-9]+)?$`)

func zitadelServerRefs(file, content string) []string {
	var hits []string
	for i, line := range strings.Split(content, "\n") {
		if before, _, ok := strings.Cut(line, "//"); ok {
			line = before
		}
		for _, f := range strings.Fields(line) {
			if zitadelServerModule.MatchString(f) {
				hits = append(hits, file+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
				break
			}
		}
	}
	return hits
}

func TestZitadelServerMatcher(t *testing.T) {
	for _, bad := range []string{
		"require github.com/zitadel/zitadel v1.0.0\n",
		"\tgithub.com/zitadel/zitadel/v2 v2.0.0 // indirect\n",
		"replace example.com/a => github.com/zitadel/zitadel v1.0.0\n",
		"github.com/zitadel/zitadel/v3 v3.0.0/go.mod h1:abc=\n",
	} {
		if len(zitadelServerRefs("go.mod", bad)) == 0 {
			t.Errorf("matcher missed the server module in %q", bad)
		}
	}
	ok := "\tgithub.com/zitadel/oidc/v3 v3.49.2\n\tgithub.com/zitadel/zitadel-go/v3 v3.0.0\n// github.com/zitadel/zitadel in a comment\n"
	if hits := zitadelServerRefs("go.mod", ok); len(hits) != 0 {
		t.Errorf("matcher flagged allowed modules: %v", hits)
	}
}

func TestGoModAndSumDoNotMentionZitadelServer(t *testing.T) {
	root := repoRoot(t)
	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, hit := range zitadelServerRefs(name, string(data)) {
			t.Errorf("%s — the AGPL Zitadel server module must never enter reliant's module graph; "+
				"drive Zitadel over its API", hit)
		}
	}
}
