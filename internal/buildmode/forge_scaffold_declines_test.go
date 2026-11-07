// Copyright (c) 2025 Reliant Labs
package buildmode_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// declinedScaffolds is every path reliant has told forge never to scaffold
// (`forge project disown <path> --absent`), by the ledger record
// .forge/scaffolded/<path>.json whose "declined" field carries the reason.
//
// A decline is the only thing standing between a forge bump and these files
// reappearing: the pinned forge scaffolds them into every project that has no
// record for them. reliant's dev stack runs no containers, no local ClickStack
// and no Zitadel IdP (docker-compose.yml was deleted on purpose in #1539), so
// the observability collector configs, the compose file and the IdP steps would
// be dead files that read as live deployment configuration. Dropping a record —
// a merge that loses it, a `forge project rescaffold` — must fail here, not
// surface as new files on the next `forge generate`.
var declinedScaffolds = []string{
	".gitattributes",
	"deploy/observability/dashboards/README.md",
	"deploy/observability/otel-collector.yaml",
	"deploy/observability/otel-profiles.yaml",
	"deploy/observability/profiles-schema.sql",
	"docker-compose.yml",
	"frontends/web/eslint.config.mjs",
	"idp-steps.yaml",
}

// keptObservability is the observability configuration reliant does deploy.
// The declines above sit in the same directory; declining a sibling must
// never take these with it.
var keptObservability = []string{
	"deploy/observability/alert-defaults_gen.json",
	"deploy/observability/dashboards/reliant-overview.json",
	"deploy/observability/dashboards/reliant-frontend_gen.json",
}

// TestForgeScaffoldDeclinesHold reads the scaffold ledger as forge does (the
// `declined` field of forge's checksums.ScaffoldEntry) and proves the declined
// set is exactly declinedScaffolds, that every declined path is absent from the
// tree, and that the observability configuration reliant keeps is still there.
//
// It walks and stats the ledger and the paths itself, so Go's test cache sees
// every input and a cached PASS cannot outlive an edit to them.
func TestForgeScaffoldDeclinesHold(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	ledger := filepath.Join(root, ".forge", "scaffolded")

	var got []string
	err := filepath.WalkDir(ledger, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var entry struct {
			Declined *string `json:"declined"`
		}
		if err := json.Unmarshal(data, &entry); err != nil {
			t.Errorf("%s: not a scaffold ledger record: %v", path, err)
			return nil
		}
		if entry.Declined == nil {
			return nil
		}
		rel, err := filepath.Rel(ledger, strings.TrimSuffix(path, ".json"))
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.TrimSpace(*entry.Declined) == "" {
			t.Errorf("%s: declined with no reason; forge treats an empty reason as no decline and scaffolds the path", rel)
		}
		got = append(got, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", ledger, err)
	}

	sort.Strings(got)
	want := append([]string(nil), declinedScaffolds...)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("declined scaffolds in .forge/scaffolded changed:\n got: %v\nwant: %v\n\n"+
			"A path that lost its record is scaffolded again by the next `forge generate`.\n"+
			"Restore it with `forge project disown <path> --absent --reason ...`, or, for a\n"+
			"deliberate change, update declinedScaffolds and say why.", got, want)
	}

	for _, rel := range declinedScaffolds {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			t.Errorf("%s is declined in .forge/scaffolded but exists in the tree: delete it, or drop the decline on purpose", rel)
		} else if !os.IsNotExist(err) {
			t.Errorf("stat %s: %v", rel, err)
		}
	}

	for _, rel := range keptObservability {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s: reliant's deployed observability configuration is missing: %v", rel, err)
		}
	}
}
