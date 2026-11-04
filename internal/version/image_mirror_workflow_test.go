// Copyright (c) 2025 Reliant Labs
package version_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type mirrorStep struct {
	Name     string `yaml:"name"`
	Uses     string `yaml:"uses"`
	Run      string `yaml:"run"`
	If       string `yaml:"if"`
	Continue bool   `yaml:"continue-on-error"`
}

func mirrorWorkflow(t *testing.T) ([]mirrorStep, []byte) {
	t.Helper()
	// Read by the test process: workflow bytes participate in Go's test cache.
	b, err := os.ReadFile(filepath.Join(repoRootFromTest(t), ".github/workflows/build-images.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []mirrorStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(b, &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow.Jobs["build-push"].Steps, b
}

// Execute the ACTUAL workflow shell bodies, not a rewritten test copy. Fake
// registry tools model a complete index with two platforms and attestations;
// docker pull deliberately selects amd64, reproducing the original defect.
func runMirror(t *testing.T, scenario string) (string, string, error) {
	t.Helper()
	steps, _ := mirrorWorkflow(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"crane", "docker", "gcloud"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(mirrorToolMock), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "MIRROR_TEST_DIR="+dir, "SCENARIO="+scenario,
		"RUNNER_TEMP="+dir, "GITHUB_OUTPUT="+filepath.Join(dir, "output"), "WIF_PROVIDER_PROD=provider", "WIF_SA_PROD=account")
	if scenario == "missing-provider" {
		env = append(env, "WIF_PROVIDER_PROD=")
	}
	if scenario == "missing-account" {
		env = append(env, "WIF_SA_PROD=")
	}
	setup := exec.Command("python3", "-c", mirrorFixture)
	setup.Env = env
	if out, err := setup.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, out)
	}
	digestBytes, err := os.ReadFile(filepath.Join(dir, "source-digest"))
	if err != nil {
		t.Fatal(err)
	}
	digest := string(digestBytes)
	tag := "sha-1234567"
	if scenario == "latest-tag" {
		tag = "latest"
	}
	if scenario == "unsafe-tag" {
		tag = "bad/tag"
	}
	if scenario == "invalid-digest" {
		digest = "not-a-digest"
	}
	env = append(env, "SOURCE_DIGEST="+digest, "MIRROR_TAG="+tag)
	replace := strings.NewReplacer(
		"${{ env.GAR_NONPROD }}", "registry.test/nonprod", "${{ env.GAR_PROD }}", "registry.test/prod",
		"${{ steps.build.outputs.digest }}", digest, "${{ steps.tag.outputs.tag }}", "sha-1234567")
	var output strings.Builder
	active := false
	for _, step := range steps {
		if step.Name == "Check prod WIF credentials are configured" {
			active = true
		}
		if !active {
			continue
		}
		if strings.HasPrefix(step.Uses, "google-github-actions/auth@") {
			if scenario == "auth-failure" {
				if step.Continue {
					continue
				}
				return dir, output.String(), fmt.Errorf("prod auth failed")
			}
			if err := os.WriteFile(filepath.Join(dir, "prod-auth"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if step.Run == "" {
			continue
		}
		cmd := exec.Command("bash", "--noprofile", "--norc", "-e", "-o", "pipefail", "-c", replace.Replace(step.Run))
		cmd.Env, cmd.Dir = env, dir
		out, err := cmd.CombinedOutput()
		output.Write(out)
		if err != nil && !step.Continue {
			return dir, output.String(), fmt.Errorf("%s: %w", step.Name, err)
		}
	}
	return dir, output.String(), nil
}

func TestImageMirrorPreservesCompleteIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("executes workflow bash/Python subprocess harness; runs in full package tests")
	}
	dir, out, err := runMirror(t, "success")
	if err != nil {
		t.Fatalf("mirror: %v\n%s", err, out)
	}
	want, err := os.ReadFile(filepath.Join(dir, "source-digest"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "target-digest"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("prod tag lost source index: got %s, want %s (host-platform pull/tag/push is not a mirror)", got, want)
	}
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "pull --format=oci registry.test/prod/reliant@"+string(want)) {
		t.Fatal("full prod graph was not read back")
	}
	for _, call := range strings.Split(string(calls), "\n") {
		if strings.HasPrefix(call, "docker ") {
			t.Fatalf("mirror used host-platform docker path: %s", calls)
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "prod", "blobs", "sha256", strings.TrimPrefix(string(want), "sha256:")))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Manifests []json.RawMessage `json:"manifests"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Manifests) != 4 {
		t.Fatalf("lost platform or attestation descriptors: %s", b)
	}
}

func TestImageMirrorRejectsIncompleteOrFailedPublication(t *testing.T) {
	if testing.Short() {
		t.Skip("executes workflow bash/Python subprocess harness; runs in full package tests")
	}
	for _, scenario := range []string{"missing-provider", "missing-account", "auth-failure", "configure-failure", "pull-failure", "push-failure", "single-platform-source", "missing-source-child", "wrong-target-digest", "missing-prod-attestation", "missing-prod-layer", "changed-prod-index", "corrupt-prod-blob", "latest-tag", "unsafe-tag", "invalid-digest"} {
		t.Run(scenario, func(t *testing.T) {
			_, out, err := runMirror(t, scenario)
			if err == nil {
				t.Fatalf("%s silently succeeded\n%s", scenario, out)
			}
		})
	}
}

func TestImageMirrorIsRequiredAfterInterpreterChecks(t *testing.T) {
	steps, workflow := mirrorWorkflow(t)
	interpreter, stage, auth, mirror := -1, -1, -1, -1
	active := false
	for i, s := range steps {
		if s.Name == "Verify image interpreters" {
			interpreter = i
		}
		if s.Name == "Check prod WIF credentials are configured" {
			active = true
		}
		if !active {
			continue
		}
		if s.Continue || s.If != "" {
			t.Errorf("required mirror step %q suppresses or conditionally skips failure", s.Name)
		}
		if s.Name == "Stage full OCI index for the prod mirror" {
			stage = i
		}
		if s.Name == "Authenticate to GCP (prod)" {
			auth = i
		}
		if s.Name == "Mirror digest to prod GAR" {
			mirror = i
		}
	}
	if interpreter < 0 || stage <= interpreter || auth <= stage || mirror <= auth {
		t.Fatalf("unsafe interpreter/staging/auth/mirror ordering: %d/%d/%d/%d", interpreter, stage, auth, mirror)
	}
	if !strings.Contains(string(workflow), "platforms: linux/amd64,linux/arm64") || !strings.Contains(string(workflow), `tags: ["v*.*.*"]`) {
		t.Fatal("existing build platforms or release trigger changed")
	}
}

const mirrorFixture = `
import hashlib,json,os,pathlib
p=pathlib.Path(os.environ['MIRROR_TEST_DIR']); root=p/'source'/'blobs'/'sha256';root.mkdir(parents=True)
def blob(o):
 b=json.dumps(o,separators=(',',':')).encode(); h=hashlib.sha256(b).hexdigest();(root/h).write_bytes(b);return 'sha256:'+h,len(b)
def descriptor(o,**extra):
 d,n=blob(o);return dict(digest=d,size=n,mediaType='application/vnd.oci.image.manifest.v1+json',**extra)
children=[]
for arch in ['amd64','arm64']:
 layer,n=blob({'layer':arch}); config,c=blob({'architecture':arch,'os':'linux'})
 child=descriptor({'schemaVersion':2,'config':{'digest':config},'layers':[{'digest':layer}]},platform={'os':'linux','architecture':arch})
 children.append(child)
 children.append(descriptor({'schemaVersion':2,'config':{'digest':config},'layers':[{'digest':layer}],'attestation':arch},platform={'os':'unknown','architecture':'unknown'},annotations={'vnd.docker.reference.type':'attestation-manifest','vnd.docker.reference.digest':child['digest']}))
if os.environ['SCENARIO']=='single-platform-source':children=children[:2]
d,n=blob({'schemaVersion':2,'mediaType':'application/vnd.oci.image.index.v1+json','manifests':children,'annotations':{'retain':'exact'}})
(p/'source'/'index.json').write_text(json.dumps({'schemaVersion':2,'manifests':[{'mediaType':'application/vnd.oci.image.index.v1+json','digest':d,'size':n}]}))
(p/'source'/'oci-layout').write_text('{"imageLayoutVersion":"1.0.0"}')
(p/'source-digest').write_text(d)
(p/'amd64-digest').write_text(children[0]['digest'])
if os.environ['SCENARIO']=='missing-source-child':(root/children[1]['digest'].split(':')[1]).unlink()
`

const mirrorToolMock = `#!/usr/bin/env python3
import hashlib,json,os,pathlib,shutil,sys
p=pathlib.Path(os.environ['MIRROR_TEST_DIR']);a=sys.argv[1:];tool=pathlib.Path(sys.argv[0]).name;s=os.environ['SCENARIO']
with (p/'calls').open('a') as f:f.write(tool+' '+' '.join(a)+'\n')
def fail(msg):print(msg,file=sys.stderr);sys.exit(1)
def graph(root,d):
 b=(root/'blobs'/'sha256'/d.split(':')[1]).read_bytes()
 if 'sha256:'+hashlib.sha256(b).hexdigest()!=d:fail('corrupt blob')
 o=json.loads(b)
 for child in o.get('manifests',[])+o.get('layers',[])+([o['config']] if 'config' in o else []):graph(root,child['digest'])
if tool=='gcloud':
 if s=='configure-failure':fail('credential helper failed')
 sys.exit(0)
if tool=='docker':
 if a[0]=='pull':(p/'pulled').write_text((p/'amd64-digest').read_text())
 elif a[0]=='tag':(p/'tagged').write_text((p/'pulled').read_text())
 elif a[0]=='push':(p/'target-digest').write_text((p/'tagged').read_text())
 else:fail('unexpected docker')
 sys.exit(0)
if a[0]=='pull':
 source='nonprod' in a[2]
 if source and (p/'prod-auth').exists():fail('prod cannot read nonprod')
 if not source and not (p/'prod-auth').exists():fail('nonprod cannot read prod')
 if source and s=='pull-failure':fail('source read denied')
 if a[1]!='--format=oci' or '@'+(p/'source-digest').read_text() not in a[2]:fail('must stage exact full OCI index')
 if '--platform' in a:fail('platform filtering loses children')
 root=p/('source' if source else 'prod')
 graph(root,(p/'source-digest').read_text());shutil.copytree(root,a[3])
 if not source and s=='corrupt-prod-blob':
  idx=json.loads((root/'blobs'/'sha256'/(p/'source-digest').read_text().split(':')[1]).read_bytes());(pathlib.Path(a[3])/'blobs'/'sha256'/idx['manifests'][1]['digest'].split(':')[1]).write_text('corrupt')
elif a[0]=='push':
 if not (p/'prod-auth').exists():fail('nonprod cannot write prod')
 if s=='push-failure':fail('destination denied or immutable tag conflict')
 if '--index' in a:fail('wrapper changes source digest')
 shutil.copytree(a[1],p/'prod');(p/'target-digest').write_text((p/'source-digest').read_text())
 idx=json.loads((p/'prod'/'blobs'/'sha256'/(p/'source-digest').read_text().split(':')[1]).read_bytes())
 if s=='missing-prod-attestation':(p/'prod'/'blobs'/'sha256'/idx['manifests'][1]['digest'].split(':')[1]).unlink()
 if s=='missing-prod-layer':
  child=json.loads((p/'prod'/'blobs'/'sha256'/idx['manifests'][0]['digest'].split(':')[1]).read_bytes());(p/'prod'/'blobs'/'sha256'/child['layers'][0]['digest'].split(':')[1]).unlink()
 if s=='wrong-target-digest':(p/'target-digest').write_text((p/'amd64-digest').read_text())
elif a[0]=='digest':print((p/'target-digest').read_text())
elif a[0]=='manifest':
 b=(p/'prod'/'blobs'/'sha256'/a[1].split('@sha256:')[1]).read_bytes()
 if s=='changed-prod-index':b=b+b' '
 sys.stdout.buffer.write(b)
else:fail('unexpected crane command')
`
