package cli

// The two hard invariants of the store-source secret commands:
//
//  1. NO COMMAND PRINTS A SECRET VALUE OR ANY DIGEST OF ONE. Every command is
//     driven with a canary value, on its success path and on its failure
//     paths, and everything it writes (stdout, stderr, the returned error) is
//     searched for the canary in every form a leak could take.
//  2. VALUES ARRIVE ON STDIN OR --from-file, NEVER AS AN ARGUMENT. Asserted on
//     the cobra command trees, so a flag or positional that could carry a
//     value fails the build of the test, not a code review.

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// leakForms are the shapes a leak of v could take: the value itself, its
// encodings, and every digest a careless "show a fingerprint" would print.
func leakForms(v string) map[string]string {
	s256 := sha256.Sum256([]byte(v))
	s512 := sha512.Sum512([]byte(v))
	s1 := sha1.Sum([]byte(v))
	forms := map[string]string{
		"raw":            v,
		"base64":         base64.StdEncoding.EncodeToString([]byte(v)),
		"base64-raw":     base64.RawStdEncoding.EncodeToString([]byte(v)),
		"base64-url":     base64.URLEncoding.EncodeToString([]byte(v)),
		"hex":            hex.EncodeToString([]byte(v)),
		"sha256-hex":     hex.EncodeToString(s256[:]),
		"sha256-base64":  base64.StdEncoding.EncodeToString(s256[:]),
		"sha512-hex":     hex.EncodeToString(s512[:]),
		"sha1-hex":       hex.EncodeToString(s1[:]),
		"sha256-hex-8":   hex.EncodeToString(s256[:])[:8],
		"sha256-hex-12+": hex.EncodeToString(s256[:])[:12],
	}
	return forms
}

func assertNoLeak(t *testing.T, what string, outputs ...string) {
	t.Helper()
	joined := strings.Join(outputs, "\n")
	for form, needle := range leakForms(canary) {
		if strings.Contains(joined, needle) {
			t.Errorf("%s leaked the value as %s:\n%s", what, form, joined)
		}
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// The env every scenario runs in: the canary is in the cluster Secret, the
// live and shadow copies, GSM, and on stdin, so any command that can reach a
// value is holding it.
func canaryFixture(t *testing.T) *storeFixture {
	f := installStoreFixture(t, verifyEnv(true))
	live := map[string]string{"github_app_private_key": canary, "webhook": canary, "dsn": canary}
	f.kube.put("control-plane-prod", "control-plane-secrets", secretWith(live, map[string]string{lastAppliedAnnotation: `{"data":{"k":"` + canary + `"}}`}, 0))
	f.kube.put("control-plane-prod", "control-plane-secrets-eso-shadow", secretWith(live, nil, 0))
	f.kube.put("control-plane-prod", "escrowed-undeclared", secretWith(live, nil, 0))
	f.gsm.secrets["acme-prod/GITHUB_APP_PRIVATE_KEY"] = []string{canary}
	f.gsm.secrets["acme-prod/GITHUB_WEBHOOK_SECRET"] = []string{canary + "-different"}
	return f
}

func TestInvariant_NoCommandPrintsAValueOrItsDigest(t *testing.T) {
	ctx := context.Background()
	scenarios := map[string]func(t *testing.T, out *bytes.Buffer) error{
		"set": func(t *testing.T, o *bytes.Buffer) error {
			return runSecretSet(ctx, "prod", "GITHUB_APP_PRIVATE_KEY", "", strings.NewReader(canary), o)
		},
		"set from file": func(t *testing.T, o *bytes.Buffer) error {
			p := filepath.Join(t.TempDir(), "v")
			if err := os.WriteFile(p, []byte(canary+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return runSecretSet(ctx, "prod", "GITHUB_APP_PRIVATE_KEY", p, nil, o)
		},
		"set twice (rotation)": func(t *testing.T, o *bytes.Buffer) error {
			for i := 0; i < 2; i++ {
				if err := runSecretSet(ctx, "prod", "GITHUB_WEBHOOK_SECRET", "", strings.NewReader(canary), o); err != nil {
					return err
				}
			}
			return nil
		},
		"list": func(t *testing.T, o *bytes.Buffer) error { return runSecretList(ctx, "prod", o) },
		"list --json": func(t *testing.T, o *bytes.Buffer) error {
			return runSecretListJSON(ctx, "prod", o)
		},
		"import from the cluster": func(t *testing.T, o *bytes.Buffer) error {
			return runSecretImport(ctx, "prod", "", importOptions{
				From: "control-plane-prod/control-plane-secrets",
				Map:  []string{"github_app_private_key=GITHUB_APP_PRIVATE_KEY"},
			}, nil, o)
		},
		"import re-run (exists)": func(t *testing.T, o *bytes.Buffer) error {
			opts := importOptions{From: "control-plane-prod/control-plane-secrets", Map: []string{"webhook=GITHUB_WEBHOOK_SECRET"}}
			for i := 0; i < 2; i++ {
				if err := runSecretImport(ctx, "prod", "", opts, nil, o); err != nil {
					return err
				}
			}
			return nil
		},
		"import --dry-run": func(t *testing.T, o *bytes.Buffer) error {
			return runSecretImport(ctx, "prod", "", importOptions{
				DryRun: true, From: "control-plane-prod/control-plane-secrets",
				Map: []string{"github_app_private_key=GITHUB_APP_PRIVATE_KEY"},
			}, nil, o)
		},
		"import --dry-run --to-bootstrap (container exists)": func(t *testing.T, o *bytes.Buffer) error {
			return runSecretImport(ctx, "prod", "", importOptions{
				DryRun: true, ToBootstrap: true, From: "control-plane-prod/control-plane-secrets",
				Map: []string{"github_app_private_key=GITHUB_APP_PRIVATE_KEY", "webhook=GITHUB_WEBHOOK_SECRET"},
			}, nil, o)
		},
		"verify against gsm with an explicit map (undeclared secret)": func(t *testing.T, o *bytes.Buffer) error {
			return runSecretsVerify(ctx, "prod", "control-plane-prod/escrowed-undeclared", secretsVerifyOptions{
				Against: "gsm:acme-prod", Map: []string{"github_app_private_key=GITHUB_APP_PRIVATE_KEY", "webhook=GITHUB_WEBHOOK_SECRET"},
			}, o)
		},
		"import from stdin": func(t *testing.T, o *bytes.Buffer) error {
			return runSecretImport(ctx, "prod", "GITHUB_APP_PRIVATE_KEY", importOptions{}, strings.NewReader(canary), o)
		},
		"verify shadow (match)": func(t *testing.T, o *bytes.Buffer) error {
			return runSecretsVerify(ctx, "prod", "", secretsVerifyOptions{}, o)
		},
		"verify against gsm (match and no-match)": func(t *testing.T, o *bytes.Buffer) error {
			return runSecretsVerify(ctx, "prod", "", secretsVerifyOptions{Against: "gsm:acme-prod"}, o)
		},
		"adopt": func(t *testing.T, o *bytes.Buffer) error {
			t.Helper()
			return runSecretsAdopt(ctx, "prod", "control-plane-prod/control-plane-secrets", "", false, o)
		},
	}
	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			f := canaryFixture(t)
			if name == "adopt" {
				f.env = verifyEnv(false)
			}
			var out bytes.Buffer
			err := scenario(t, &out)
			assertNoLeak(t, name, out.String(), errText(err))
		})
	}
}

// Failure paths are where a value escapes: a remote that echoes the request, an
// error wrapped around the value. Every store, with a remote that echoes.
func TestInvariant_AFailingWriteNeverEchoesTheValue(t *testing.T) {
	ctx := context.Background()

	t.Run("openbao echoes the request body", func(t *testing.T) {
		f := canaryFixture(t)
		f.bao.failKV, f.bao.echoOn = 400, true
		var out bytes.Buffer
		err := runSecretSet(ctx, "prod", "GITHUB_APP_PRIVATE_KEY", "", strings.NewReader(canary), &out)
		if err == nil {
			t.Fatal("the write should have failed")
		}
		assertNoLeak(t, "openbao failure", out.String(), errText(err))
	})

	t.Run("openbao echoes the request body on import", func(t *testing.T) {
		f := canaryFixture(t)
		f.bao.failKV, f.bao.echoOn = 400, true
		var out bytes.Buffer
		err := runSecretImport(ctx, "prod", "", importOptions{
			From: "control-plane-prod/control-plane-secrets", Map: []string{"github_app_private_key=GITHUB_APP_PRIVATE_KEY"},
		}, nil, &out)
		if err == nil {
			t.Fatal("the import should have failed")
		}
		assertNoLeak(t, "openbao import failure", out.String(), errText(err))
	})

	t.Run("hosted control plane echoes the request body", func(t *testing.T) {
		f := canaryFixture(t)
		f.hosted.failErr = fmt.Errorf("controlplane.v1.SecretStoreService/SetSecret failed: invalid secret_value %q (base64 %s)",
			canary, base64.StdEncoding.EncodeToString([]byte(canary)))
		var out bytes.Buffer
		err := runSecretSet(ctx, "prod", "STRIPE_SECRET_KEY", "", strings.NewReader(canary), &out)
		if err == nil {
			t.Fatal("the write should have failed")
		}
		assertNoLeak(t, "hosted failure", out.String(), errText(err))
	})

	t.Run("secret manager echoes the payload", func(t *testing.T) {
		f := canaryFixture(t)
		f.gsm.fail = fmt.Errorf("secret manager add version: payload %s rejected", canary)
		var out bytes.Buffer
		err := runSecretSet(ctx, "prod", "tls-cert-prod", "", strings.NewReader(canary), &out)
		if err == nil {
			t.Fatal("the write should have failed")
		}
		assertNoLeak(t, "gsm failure", out.String(), errText(err))
	})
}

// A redacted error still answers errors.Is / errors.As, so scrubbing the text
// never breaks the branching that depends on the original.
func TestInvariant_RedactionPreservesTheErrorChain(t *testing.T) {
	root := errors.New("boom " + canary)
	err := redactValue(fmt.Errorf("wrapped: %w", root), canary)
	if strings.Contains(err.Error(), canary) {
		t.Error("the value survived redaction")
	}
	if !errors.Is(err, root) {
		t.Error("errors.Is no longer reaches the original")
	}
	if got := redactValue(nil, canary); got != nil {
		t.Errorf("redactValue(nil) = %v", got)
	}
	plain := errors.New("no value here")
	if redactValue(plain, canary) != plain {
		t.Error("an error without the value should be returned untouched")
	}
}

// --- invariant 2: values never arrive as an argument -------------------------

// valueCarryingNames are flag names that would take a secret as an argument.
var valueCarryingNames = []string{"value", "secret", "password", "token", "key-value", "data", "content", "literal", "from-literal", "string"}

func TestInvariant_NoStoreSourceCommandTakesAValueAsAnArgument(t *testing.T) {
	root := newTestRootCmd(t)
	// Every command that can carry a store-source value.
	targets := [][]string{
		{"secret", "set"}, {"secret", "import"}, {"secret", "list"},
		{"env", "secrets", "verify"}, {"env", "secrets", "adopt"},
	}
	for _, path := range targets {
		cmd, _, err := root.Find(path)
		if err != nil || cmd == nil || cmd.Name() != path[len(path)-1] {
			t.Fatalf("command %v is not registered: %v", path, err)
		}
		t.Run(strings.Join(path, " "), func(t *testing.T) {
			cmd.Flags().VisitAll(func(f *pflag.Flag) {
				for _, bad := range valueCarryingNames {
					if f.Name == bad || strings.Contains(f.Name, "secret-value") {
						t.Errorf("flag --%s looks like it carries a value; values come from stdin or --from-file", f.Name)
					}
				}
				// A string flag is only acceptable if it names where something
				// is (a file, an env, a context), never a literal.
				if f.Value.Type() == "string" || f.Value.Type() == "stringSlice" {
					ok := false
					for _, s := range []string{"env", "from", "from-file", "map", "store", "kube-context", "against", "project"} {
						if f.Name == s {
							ok = true
						}
					}
					if !ok {
						t.Errorf("flag --%s is a string flag nobody vetted as value-free; vet it and add it here deliberately", f.Name)
					}
				}
			})
		})
	}

	// The positional arguments: set takes a NAME, import takes a NAME,
	// verify/adopt take an env and a Secret name. A value-length positional
	// check would be meaningless, so the contract is the arity and the Use line.
	for path, maxArgs := range map[string]int{"secret set": 1, "secret import": 1, "env secrets verify": 2, "env secrets adopt": 2} {
		cmd, _, _ := root.Find(strings.Fields(path))
		for n := maxArgs + 1; n <= maxArgs+2; n++ {
			args := make([]string, n)
			for i := range args {
				args[i] = canary
			}
			if err := cmd.Args(cmd, args); err == nil {
				t.Errorf("`forge %s` accepts %d positional arguments, so a value could be passed as one", path, n)
			}
		}
	}
}

// `secret set` has one positional (the NAME). Handing it the value as a second
// argument must fail before anything is written.
func TestInvariant_SetRejectsAValueGivenAsASecondArgument(t *testing.T) {
	f := canaryFixture(t)
	root := newTestRootCmd(t)
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"secret", "set", "--env", "prod", "GITHUB_APP_PRIVATE_KEY", canary})
	err := root.Execute()
	if err == nil {
		t.Fatal("set accepted a value as an argument")
	}
	if len(f.bao.kv) != 0 {
		t.Errorf("wrote %v", f.bao.kv)
	}
	assertNoLeak(t, "set with a value argument", out.String(), errOut.String())
	_ = err
}

// Every store-source command's own help must tell the truth about where values
// come from, and none may suggest an argument.
func TestInvariant_HelpNeverShowsAValueOnTheCommandLine(t *testing.T) {
	root := newTestRootCmd(t)
	for _, path := range [][]string{{"secret", "set"}, {"secret", "import"}, {"env", "secrets", "verify"}, {"env", "secrets", "adopt"}} {
		cmd, _, _ := root.Find(path)
		help := cmd.Long + "\n" + cmd.Example + "\n" + cmd.UseLine()
		for _, line := range strings.Split(help, "\n") {
			l := strings.TrimSpace(line)
			if strings.HasPrefix(l, "forge secret set") || strings.HasPrefix(l, "forge secret import") {
				// An example that names a value inline would train people to
				// paste one. The only inline forms are pipes and --from-file.
				if strings.Contains(l, "--value") || strings.Contains(l, "sk_") {
					t.Errorf("%v help shows a value on the command line: %q", path, l)
				}
			}
		}
	}
}
