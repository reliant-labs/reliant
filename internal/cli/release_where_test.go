package cli

// Tests for `forge release where <version>` — which environments run this
// release. It is NOT env-scoped, which is why it survived the merge of the
// env read verbs into `forge env status`: the question is about a RELEASE,
// and the answer spans every environment.
//
// The hosted case runs the real hostedStore over cloud.Client against
// fakeDeployService.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// release where: hosted answers from GetRelease.current_environment_ids,
// RESOLVED TO NAMES; a release cut but bound nowhere is exit 1; one never cut
// is exit 1 with a different reason.
func TestReleaseWhere_Hosted(t *testing.T) {
	fake, store := hostedPromoteFixture(t, "v1", "v2")
	run := func(version string) (string, error) {
		var err error
		out := captureStdout(t, func() {
			err = runReleaseWhere(context.Background(), version, []whereSource{{Location: "cp", Hosted: store.client}}, true, &bytes.Buffer{})
		})
		return out, err
	}
	out, err := run("v2")
	if err != nil {
		t.Fatalf("v2 is current on prod: %v", err)
	}
	var doc releaseWhereDocument
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("decode: %v\n%s", jerr, out)
	}
	if len(doc.Environments) != 1 || doc.Environments[0].Env != "prod" {
		t.Fatalf("environments = %+v, want [prod] by NAME", doc.Environments)
	}
	if _, err := run("v1"); exitCodeForError(err) != exitWrong {
		t.Errorf("v1 (cut, superseded) must exit %d, got %v", exitWrong, err)
	}
	if _, err := run("v404"); exitCodeForError(err) != exitWrong {
		t.Errorf("a never-cut release must exit %d, got %v", exitWrong, err)
	}
	_ = fake
}

func TestReleaseWhere_File(t *testing.T) {
	dir := t.TempDir()
	writeBinding(t, dir, "staging", "v2", map[string]string{"api": sha("b")})
	writeBinding(t, dir, "prod", "v1", map[string]string{"api": sha("a")})
	writeBinding(t, dir, "prod", "v2", map[string]string{"api": sha("b")})
	store := testBindings(t, dir)
	src := whereSource{Location: dir, Files: map[string]bindingStore{"staging": store, "prod": store, "dev": store}}

	var buf bytes.Buffer
	if err := runReleaseWhere(context.Background(), "v2", []whereSource{src}, false, &buf); err != nil {
		t.Fatalf("v2: %v", err)
	}
	if !strings.Contains(buf.String(), "prod, staging") {
		t.Errorf("want both envs, sorted, got:\n%s", buf.String())
	}
	if err := runReleaseWhere(context.Background(), "v1", []whereSource{src}, false, &bytes.Buffer{}); exitCodeForError(err) != exitWrong {
		t.Errorf("v1 (superseded on prod) must exit %d, got %v", exitWrong, err)
	}
}
