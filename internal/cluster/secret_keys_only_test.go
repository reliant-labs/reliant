package cluster

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"text/template"
)

// secretValue is the one value the fake cluster holds. If it ever reaches
// forge, the read was not keys-only.
const secretValue = "hunter2-prod-db-password"

// fakeSecretKubectl installs a `kubectl` that answers like a real one for a
// Secret holding {password: secretValue, username: app}: it prints the whole
// object — values included — for `-o json` or a jsonpath over `.data`, and
// the template's output for `-o go-template=` (what secretKeysTemplate
// renders is pinned separately, by TestSecretKeysTemplate_PrintsNoValue).
// Every invocation's argv is appended to the returned log.
func fakeSecretKubectl(t *testing.T) string {
	t.Helper()
	requirePOSIXFake(t, "kubectl")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kubectl.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + logPath + "\n" +
		"case \" $* \" in\n" +
		"  *' missing '*) echo 'Error from server (NotFound): secrets \"missing\" not found' >&2; exit 1 ;;\n" +
		"  *' -o go-template='*) printf 'password\\nusername\\n' ;;\n" +
		"  *) printf '{\"data\":{\"password\":\"" + secretValue + "\",\"username\":\"app\"}}' ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake kubectl: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// A presence check reads KEY NAMES. The value must never be requested from
// the cluster in a form that returns it, so it cannot sit in forge's memory,
// an error string or a log.
func TestKubectlSecretGetter_ReadsKeysNeverValues(t *testing.T) {
	logPath := fakeSecretKubectl(t)

	keys, exists, err := KubectlSecretGetter{}.GetSecretKeys(context.Background(), "prod-ctx", "app", "db-credentials")
	if err != nil || !exists {
		t.Fatalf("GetSecretKeys: exists=%v err=%v", exists, err)
	}
	if want := map[string]struct{}{"password": {}, "username": {}}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.TrimSpace(string(raw))
	if !strings.Contains(argv, "--context prod-ctx") || !strings.Contains(argv, "-n app") {
		t.Errorf("kubectl %s: the declared context and namespace must be explicit", argv)
	}
	for _, valueBearing := range []string{"-o json", "jsonpath", "-o yaml"} {
		if strings.Contains(argv, valueBearing) {
			t.Errorf("kubectl %s: %q returns the Secret's values; a presence check must ask for key names only", argv, valueBearing)
		}
	}
}

// kubectl renders `-o go-template` with Go's text/template over the decoded
// object, so the template can be pinned here exactly as kubectl runs it: keys,
// one per line, and no value.
func TestSecretKeysTemplate_PrintsNoValue(t *testing.T) {
	tmpl, err := template.New("keys").Parse(secretKeysTemplate)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var out strings.Builder
	secret := map[string]any{"kind": "Secret", "data": map[string]any{"password": secretValue, "username": "YXBw"}}
	if err := tmpl.Execute(&out, secret); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := out.String(); got != "password\nusername\n" {
		t.Fatalf("template output = %q, want the keys alone", got)
	}
	if strings.Contains(out.String(), secretValue) {
		t.Fatal("the template printed a value")
	}
}

// Absent is a definite answer, not an error: the preflight and the plan both
// report it as missing rather than as "could not look".
func TestKubectlSecretGetter_NotFoundIsAbsent(t *testing.T) {
	fakeSecretKubectl(t)
	keys, exists, err := KubectlSecretGetter{}.GetSecretKeys(context.Background(), "prod-ctx", "app", "missing")
	if err != nil || exists || keys != nil {
		t.Fatalf("missing Secret: keys=%v exists=%v err=%v, want absent with no error", keys, exists, err)
	}
}
