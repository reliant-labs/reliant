package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/deploytarget"
)

// recordDocMonitoring is the env document `forge obs init` writes for prod,
// on a control plane of the team's own.
const recordDocMonitoring = `apiVersion: forge.dev/env/v1
project: shop
env: prod
control_plane:
  endpoint: "https://cp.acme.example"
observability:
  enabled: true
  destination: {token_from_control_plane: true}
`

// recordDocExternal is that document grown by the record-deploys step: the
// workloads the team's pipeline deploys, bound to the external runtime.
const recordDocExternal = recordDocMonitoring + `workloads:
  - name: api
    runtime: {type: external}
  - name: worker
    kind: worker
    runtime: {type: external}
`

func writeRecordFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A repo whose only forge file is deploy/envs/prod.yaml records: the project
// comes from the document, the env's declaration is the document's render
// (every workload external, so admitted), and the record goes to the control
// plane the document declares, as SELF_MANAGED. Run from a subdirectory, as a
// CI step may be: the project is found by walking up to deploy/envs.
func TestReleaseRecord_DocumentOnlyRepoRecords(t *testing.T) {
	dir := t.TempDir()
	writeRecordFile(t, dir, "deploy/envs/prod.yaml", recordDocExternal)
	writeRecordFile(t, dir, "services/api/main.py", "print('hi')\n")
	t.Chdir(filepath.Join(dir, "services", "api"))
	t.Setenv(ledgerSelectionEnv, "")

	cp := newFakeRecordCP()
	var target recordLedger
	var openedDir, openedProject string
	useRecordLedger(t, func(ctx context.Context, projectDir, project, env, _ string, declared *KCLEntities) (recordLedger, error) {
		openedDir, openedProject = projectDir, project
		// The real resolution of where the record goes, with a token so it
		// is hosted; only the transport is the fake.
		var err error
		if target, err = defaultRecordLedger(ctx, projectDir, project, env, "rlat_test", declared); err != nil {
			return recordLedger{}, err
		}
		s := target.ledger.Bindings.(*hostedStore)
		return recordLedger{ledger: hostedLedger(cp, target.endpoint, project, s.kind), endpoint: target.endpoint}, nil
	})

	res, err := recordRelease(context.Background(), releaseRecordOptions{env: "prod", version: "v1.4.0",
		commit: recordCommitA, run: runOptions{None: true},
		workloads: []string{"api"}, images: []string{"ghcr.io/acme/api@" + recordDigestA}}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("record in a document-only repo: %v", err)
	}
	if want, _ := filepath.EvalSymlinks(dir); evalSymlinks(t, openedDir) != want {
		t.Errorf("project dir = %q, want %q (the directory holding deploy/envs)", openedDir, dir)
	}
	if openedProject != "shop" || res.Project != "shop" {
		t.Errorf("project = %q / %q, want the document's \"shop\"", openedProject, res.Project)
	}
	if target.endpoint != "https://cp.acme.example" {
		t.Errorf("endpoint = %q, want the control plane the document declares", target.endpoint)
	}
	if s := target.ledger.Bindings.(*hostedStore); s.kind != deploytarget.HostedEnvSelfManaged {
		t.Errorf("kind = %q, want SELF_MANAGED", s.kind)
	}
	if res.Ledger != "hosted" || !res.ReleaseCreated || !res.Promoted {
		t.Errorf("result = %+v, want a hosted record that cut and promoted", res)
	}
	if n := len(cp.called("CutRelease")); n != 1 {
		t.Errorf("CutRelease called %d times, want 1", n)
	}
	if n := len(cp.called("Promote")); n != 1 {
		t.Errorf("Promote called %d times, want 1", n)
	}
	for _, e := range cp.called("EnsureEnvironment") {
		spec := e["spec"].(map[string]any)
		if spec["project"] != "shop" || !strings.Contains(strings.ToUpper(spec["kind"].(string)), "SELF_MANAGED") {
			t.Errorf("EnsureEnvironment spec = %v, want project shop, SELF_MANAGED", spec)
		}
	}
}

func evalSymlinks(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// What `forge obs init` writes, before the record-deploys step names any
// workload, declares no workload bound to the external runtime: the record is
// refused before anything is cut, saying which file and what to add to it.
func TestReleaseRecord_MonitoringOnlyDocumentSaysWhatToAdd(t *testing.T) {
	dir := t.TempDir()
	writeRecordFile(t, dir, "deploy/envs/prod.yaml", recordDocMonitoring)
	t.Chdir(dir)
	cp := newFakeRecordCP()
	useRecordLedger(t, hostedRecordTarget(cp))

	_, err := recordRelease(context.Background(), releaseRecordOptions{env: "prod", version: "v1",
		commit: recordCommitA, run: runOptions{None: true}}, &bytes.Buffer{})
	var refused *promoteRefusedError
	if !errors.As(err, &refused) || refused.Reason != reasonRecordOnlyRelease {
		t.Fatalf("err = %v, want the record_only_release refusal", err)
	}
	for _, want := range []string{"deploy/envs/prod.yaml", "no workloads", "runtime: {type: external}"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal lacks %q:\n%v", want, err)
		}
	}
	if len(cp.calls) != 0 {
		t.Fatalf("a refused record called the control plane: %v", cp.calls)
	}
}

func TestResolveRecordProject(t *testing.T) {
	for _, c := range []struct {
		name    string
		files   map[string]string
		env     string
		project string
		errHas  []string
	}{{
		name:    "forge.yaml name wins over the env documents",
		files:   map[string]string{"forge.yaml": "name: shop\n", "deploy/envs/staging.yaml": strings.ReplaceAll(recordDocMonitoring, "project: shop", "project: legacy")},
		env:     "prod",
		project: "shop",
	}, {
		name:    "forge.yaml name and the env's document agree",
		files:   map[string]string{"forge.yaml": "name: shop\n", "deploy/envs/prod.yaml": recordDocExternal},
		env:     "prod",
		project: "shop",
	}, {
		name:   "forge.yaml name and the env's document disagree",
		files:  map[string]string{"forge.yaml": "name: store\n", "deploy/envs/prod.yaml": recordDocExternal},
		env:    "prod",
		errHas: []string{"deploy/envs/prod.yaml", `"shop"`, `"store"`},
	}, {
		name:    "a forge.yaml without name: takes the env's document",
		files:   map[string]string{"forge.yaml": "lint: {}\n", "deploy/envs/prod.yaml": recordDocExternal},
		env:     "prod",
		project: "shop",
	}, {
		name:    "an env with no document takes the project every document names",
		files:   map[string]string{"deploy/envs/prod.yaml": recordDocExternal},
		env:     "qa",
		project: "shop",
	}, {
		name:   "nothing names a project",
		files:  map[string]string{"README.md": "hi\n"},
		env:    "prod",
		errHas: []string{"cannot tell which project", "deploy/envs/prod.yaml", "obs init prod", "name: <project>"},
	}, {
		name:   "a forge.yaml without name: and no documents",
		files:  map[string]string{"forge.yaml": "lint: {}\n"},
		env:    "prod",
		errHas: []string{"cannot tell which project"},
	}} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for rel, body := range c.files {
				writeRecordFile(t, dir, rel, body)
			}
			t.Chdir(dir)
			_, project, err := resolveRecordProject(dir, c.env)
			if c.errHas != nil {
				if err == nil {
					t.Fatalf("project = %q, want an error", project)
				}
				for _, want := range c.errHas {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error lacks %q:\n%v", want, err)
					}
				}
				return
			}
			if err != nil || project != c.project {
				t.Fatalf("project = %q, err = %v; want %q", project, err, c.project)
			}
		})
	}
}
