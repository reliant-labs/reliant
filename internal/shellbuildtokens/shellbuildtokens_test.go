package shellbuildtokens

import (
	"strings"
	"testing"
)

// The rule's reason for existing: a raw-string ${TARGETARCH} survives KCL
// (raw strings disable interpolation) and reaches the shell unset, where
// `GOARCH=` silently means "host arch" rather than failing. Nothing else in
// the pipeline notices, so this check is the only thing between that command
// and an `exec format error` crash-loop on the cluster.
func TestCheckFlagsRawStringTargetArch(t *testing.T) {
	got := Check("reliant", `GOARCH=${TARGETARCH} go build -o bin/reliant ./cmd/reliant`, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for ${TARGETARCH}, got %d: %+v", len(got), got)
	}
	if got[0].Token != "TARGETARCH" {
		t.Errorf("token = %q, want TARGETARCH", got[0].Token)
	}
	// The finding must name the KCL replacement, not just complain.
	if !strings.Contains(got[0].Remediation(), "forge.target_arch()") {
		t.Errorf("remediation does not name forge.target_arch(): %s", got[0].Remediation())
	}
	if !strings.Contains(got[0].Message(), "reliant") {
		t.Errorf("message does not name the workload: %s", got[0].Message())
	}
}

// Declaring the key in the build's env map is the supported way to keep the
// token spelling: forge merges that map onto the process env, so the shell
// resolves it. Flagging it would be a false positive on correct code.
func TestCheckPassesWhenTokenIsDeclaredInEnv(t *testing.T) {
	cmd := `GOARCH=${TARGETARCH} go build ./cmd/reliant`
	if got := Check("reliant", cmd, map[string]string{"TARGETARCH": "arm64"}); len(got) != 0 {
		t.Errorf("TARGETARCH is declared in env, so it resolves — want no findings, got %+v", got)
	}
}

// Every retired token is covered, and each names its own replacement. A gap
// here is a token that migrates silently.
func TestCheckFlagsEveryRetiredToken(t *testing.T) {
	for token, want := range Replacements {
		got := Check("svc", "echo ${"+token+"}", nil)
		if len(got) != 1 {
			t.Errorf("${%s}: expected 1 finding, got %d", token, len(got))
			continue
		}
		if got[0].Replacement != want {
			t.Errorf("${%s}: replacement = %q, want %q", token, got[0].Replacement, want)
		}
	}
}

// A variable the SHELL owns must pass. The rule flags forge's retired
// vocabulary only — a command is a shell program and most of its ${...} is
// none of forge's business.
func TestCheckIgnoresOrdinaryShellVariables(t *testing.T) {
	cmd := `W=$(mktemp -d); cp -r "${HOME}/src" "$W/src"; PATH=${PATH}:/opt/bin make -C "$W"`
	if got := Check("svc", cmd, nil); len(got) != 0 {
		t.Errorf("shell variables are not forge tokens — want no findings, got %+v", got)
	}
}

// A bare `$TAG` is the script's own variable, not a forge token: forge only
// ever substituted the braced form, so flagging the bare one would report a
// correct shell script as broken. The same goes for a name that merely STARTS
// with a token — `$TAG_SUFFIX` and `${IMAGE_NAME}` are one variable each, and
// reading them as a token plus trailing text is how a naive matcher corrupts a
// working command.
func TestCheckIgnoresBareAndPrefixedNames(t *testing.T) {
	for _, cmd := range []string{
		`docker build -t $IMAGE:$TAG .`,
		`docker build -t ${IMAGE_NAME}:${TAG_SUFFIX} .`,
		`echo "$TAGGED"`,
	} {
		if got := Check("svc", cmd, nil); len(got) != 0 {
			t.Errorf("%s: want no findings (these are the shell's own variables), got %+v", cmd, got)
		}
	}
}

// One finding per token however many times it appears: the fix is one edit.
func TestCheckReportsEachTokenOnce(t *testing.T) {
	cmd := `docker build -t ${IMAGE}:${TAG} . && docker push ${IMAGE}:${TAG}`
	got := Check("svc", cmd, nil)
	if len(got) != 2 {
		t.Fatalf("expected 2 findings (IMAGE, TAG), got %d: %+v", len(got), got)
	}
	// Sorted, so the order is part of the contract.
	if got[0].Token != "IMAGE" || got[1].Token != "TAG" {
		t.Errorf("findings are not sorted by token: %+v", got)
	}
}

// Error is what `forge generate` returns, so it must carry every finding and
// say why forge no longer substitutes.
func TestErrorNamesEveryFindingAndTheReason(t *testing.T) {
	if err := Error(nil); err != nil {
		t.Fatalf("no findings must produce no error, got %v", err)
	}
	err := Error(Check("svc", "echo ${REGISTRY}/${IMAGE}", nil))
	if err == nil {
		t.Fatal("expected an error for two retired tokens")
	}
	for _, want := range []string{"REGISTRY", "IMAGE", "verbatim"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}
