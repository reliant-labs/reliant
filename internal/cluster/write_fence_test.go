package cluster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKubectlWritesClassifiesEveryVerbForgeRuns(t *testing.T) {
	cases := []struct {
		args   []string
		writes bool
	}{
		{[]string{"--context", "prod", "get", "deploy", "-o", "json"}, false},
		{[]string{"-n", "ns", "--context=prod", "describe", "pod", "x"}, false},
		{[]string{"--context", "prod", "diff", "-R", "-f", "dir"}, false},
		{[]string{"config", "--kubeconfig", "k", "rename-context", "a", "b"}, false},
		{[]string{"--context", "prod", "wait", "--for=condition=Ready", "nodes", "--all"}, false},
		{[]string{"--context", "prod", "rollout", "status", "deployment/x"}, false},
		{[]string{"--context", "prod", "rollout", "restart", "deployment/x"}, true},
		{[]string{"--context", "prod", "apply", "--server-side", "--force-conflicts", "-f", "-"}, true},
		{[]string{"--context", "prod", "apply", "--server-side", "--dry-run=server", "-f", "-"}, false},
		{[]string{"apply", "--dry-run=client", "-f", "-"}, false},
		{[]string{"apply", "--dry-run", "none", "-f", "-"}, true},
		{[]string{"--context", "prod", "create", "--validate=false", "-f", "-"}, true},
		{[]string{"--context", "prod", "patch", "configmap", "coredns", "-p", "{}"}, true},
		{[]string{"--context", "prod", "delete", "job", "x"}, true},
		{[]string{"--context", "prod", "label", "ns", "x", "a=b"}, true},
		// Unknown verbs fail closed.
		{[]string{"--context", "prod", "frobnicate"}, true},
		// Boolean globals never consume the next argument.
		{[]string{"--insecure-skip-tls-verify", "get", "ns"}, false},
		{[]string{"--warnings-as-errors", "apply", "-f", "-"}, true},
		// Attached short values.
		{[]string{"-nkube-system", "get", "pods"}, false},
		{[]string{"-v5", "apply", "-f", "-"}, true},
		// An unknown flag before the verb fails closed: its value could be
		// read as the verb.
		{[]string{"--some-future-flag", "get", "apply", "-f", "-"}, true},
		{[]string{"--some-future-flag", "get", "ns"}, true},
		{[]string{"--context"}, true},
		{[]string{"--", "get", "ns"}, true},
		{[]string{"--context", "prod", "rollout", "--unknown", "x", "status"}, true},
	}
	for _, c := range cases {
		verb, writes := kubectlWrites(c.args)
		if writes != c.writes {
			t.Errorf("kubectlWrites(%q) = (%q, %v), want writes=%v", c.args, verb, writes, c.writes)
		}
	}
}

// THE GUARANTEE: under the fence a write never starts a kubectl process. A
// fake kubectl on PATH would leave a marker if it ran; the apply must refuse
// with ErrWritesFenced and leave none.
func TestWriteFenceRefusesApplyWithoutRunningKubectl(t *testing.T) {
	requirePOSIXFake(t, "kubectl")
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	script := "#!/bin/sh\necho \"$@\" >> " + marker + "\n"
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	ctx := WithWritesFenced(context.Background(), "--plan-only writes nothing to any cluster")
	err := KubectlApply(ctx, "prod", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n")
	if !errors.Is(err, ErrWritesFenced) {
		t.Fatalf("fenced KubectlApply = %v, want ErrWritesFenced", err)
	}
	if err := ApplySecrets(ctx, "prod", "ns", "forge", "apiVersion: v1\nkind: Secret\n"); !errors.Is(err, ErrWritesFenced) {
		t.Fatalf("fenced ApplySecrets = %v, want ErrWritesFenced", err)
	}
	if b, _ := os.ReadFile(marker); len(b) > 0 {
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if verb, writes := kubectlWrites(strings.Fields(line)); writes {
				t.Fatalf("a fenced write started kubectl %s: %s", verb, line)
			}
		}
	}

	// Reads still run under the fence: a preview must be able to look.
	if err := KubectlCommand(ctx, "--context", "prod", "get", "ns").Run(); err != nil {
		t.Fatalf("fenced read refused: %v", err)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatal("a fenced read did not run kubectl")
	}
}

// Every value-taking global flag, given a read verb as its value and followed
// by apply, must classify as a write: the value is never mistaken for the verb.
func TestKubectlWritesNeverReadsAFlagValueAsTheVerb(t *testing.T) {
	for flag, takesValue := range kubectlGlobalFlags {
		if !takesValue {
			continue
		}
		for _, args := range [][]string{
			{flag, "get", "apply", "-f", "-"},
			{flag + "=get", "apply", "-f", "-"},
		} {
			if verb, writes := kubectlWrites(args); !writes {
				t.Errorf("kubectlWrites(%q) = (%q, false), want a write", args, verb)
			}
		}
		if verb, writes := kubectlWrites([]string{flag, "x", "get", "ns"}); writes {
			t.Errorf("kubectlWrites(%s x get ns) = (%q, true), want a read", flag, verb)
		}
	}
}

func TestKubectlCommandUnfencedLeavesCmdRunnable(t *testing.T) {
	cmd := KubectlCommand(context.Background(), "--context", "prod", "apply", "-f", "-")
	if errors.Is(cmd.Err, ErrWritesFenced) {
		t.Fatal("an unfenced apply was fenced")
	}
}
