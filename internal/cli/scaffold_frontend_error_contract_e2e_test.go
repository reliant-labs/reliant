//go:build e2e

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/naming"
)

// TestE2EGeneratedHooksExposeTheTypedErrorContract is the gate on the ERROR
// TYPE of the generated React Query hooks.
//
// The contract: the transport's error-normalize interceptor (wired in
// src/lib/connect.ts from @reliantlabs/forge-web-runtime) throws ConnectClientError
// on every failure, carrying `.code`, `.status`, `.retryable` and — stamped by
// the backend on every error pkg/crud returns — `.reason`. The documented rule
// is to branch on `.reason`/`.code` and render with `userMessage(err)`, never
// to match on message text.
//
// The shipped defect this gate exists for: the hooks typed their error as
// React Query's default `Error`. Every one of those four fields was then
// invisible at the call site, and `err.message.includes(...)` — the one thing
// the runtime's own docs forbid — was the ONLY expression that compiled. A
// measured unit wrote exactly that, and it was not ignorance: it was obeying
// the type checker.
//
// Why this is a COMPILE gate and not a `strings.Contains(tmpl,
// "ConnectClientError")` assertion: a string check passes over a template that
// emits the symbol into a file that does not compile — wrong package, wrong
// export name, unresolvable specifier under the app's moduleResolution. The
// only check that can tell a working type from a broken one is tsc, run
// against the REAL package as installed.
//
// And it is positive AND negative on purpose. A positive-only compile gate
// cannot distinguish a correctly typed error from `any`: both accept
// `err.reason`. The negative half — a field that exists on no type — is what
// proves the type is load-bearing. Both halves matter; deleting either one
// leaves a gate that reports green on a broken contract.
//
// COST: one scaffold + one `npm install` + three tsc runs (~2-4 min).
func TestE2EGeneratedHooksExposeTheTypedErrorContract(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	requireTool(t, "node", "npm")

	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmdTimeout(t, dir, 10*time.Minute, forgeBin,
		"project", "new", "errapp",
		"--mod", "example.com/errapp",
		"--frontend", "web",
	)
	projectDir := filepath.Join(dir, "errapp")
	addCorpusForgePkgReplace(t, projectDir)

	// The service comes FIRST. An entity births its CRUD messages + RPCs
	// into an existing service proto; against a project with zero services
	// there is no proto to birth into, and everything downstream — the
	// proto, the hooks, this whole gate — then quietly does not exist.
	runCmd(t, projectDir, forgeBin, "scaffold", "service", "item")

	// A CRUD entity gives the generator both a query and a mutation hook,
	// which are separately typed (UseQueryOptions vs UseMutationOptions) and
	// so are separately breakable. The entity is declared in the PROTO — the
	// `field:type` flag grammar was removed so the proto is the single place
	// an entity is declared.
	itemProtoPath := filepath.Join(projectDir, "proto", "services", "item", "v1", "item.proto")
	bareProto := readFileE2E(t, itemProtoPath)
	if err := os.WriteFile(itemProtoPath, []byte(bareProto+`
// forge:entity
message Item {
  string id = 1;
  string name = 2;
  string description = 3;
  int64 price_cents = 4;
  bool active = 5;
}
`), 0o644); err != nil {
		t.Fatalf("declare the Item entity in the item proto: %v", err)
	}
	runCmd(t, projectDir, forgeBin, "scaffold")

	// Non-vacuousness precondition, checked before `generate` so the failure
	// names the cause: no RPCs means no hooks means nothing for tsc to gate.
	// The zero-service skip above is a WARNING, not an error, so exit codes
	// alone cannot catch it.
	protoPath := filepath.Join(projectDir, "proto", "services", "item", "v1", "item.proto")
	proto := readFileE2E(t, protoPath)
	for _, rpc := range []string{"rpc CreateItem(", "rpc ListItems("} {
		if !strings.Contains(proto, rpc) {
			t.Fatalf("scaffold entity did not inject %q into %s — with no CRUD RPCs the generator "+
				"emits no hooks and every assertion below is vacuous:\n%s", rpc, protoPath, proto)
		}
	}

	runCmd(t, projectDir, forgeBin, "generate")

	webDir := filepath.Join(projectDir, "frontends", "web")

	// The hooks file is named from the SERVICE, not the entity, and the name
	// is derived — so derive it the way the generator does (naming.
	// ServiceHookFile is the single call every hook emitter goes through)
	// rather than pinning a literal that goes stale the day the rule moves.
	hooksFile := naming.ServiceHookFile("ItemService")
	hooksModule := "@/hooks/" + strings.TrimSuffix(hooksFile, ".ts")

	// A smoke read, not the gate: name the invariant so a failure below reads
	// as "the error type regressed" instead of as a wall of tsc output.
	//
	// The hooks no longer name ConnectClientError themselves. They are built
	// by the runtime's createQueryHook / createMutationHook factories, whose
	// RETURN types carry it (UseQueryResult<Response, ConnectClientError>),
	// so the typed-error contract now rides on the factory import instead of
	// a direct type import. That is the thing whose absence would make every
	// assertion below vacuous, so it is what this reads for; whether the
	// error type actually reaches a call site is settled by the positive and
	// negative tsc runs, which is the real gate either way.
	hooks := readFileE2E(t, filepath.Join(webDir, "src", "hooks", hooksFile))
	for _, want := range []string{"createQueryHook", "createMutationHook", `from "@reliantlabs/forge-web-runtime/service-hooks"`} {
		if !strings.Contains(hooks, want) {
			t.Errorf("generated hooks do not go through the runtime's typed hook factories (missing %q):\n%s", want, hooks)
		}
	}

	// Both halves below are `npx --no-install tsc`, which resolves tsc from
	// the scaffold's node_modules. `forge project new` installs those deps,
	// but a failed install is only a WARNING there (see runNpmInstall's call
	// site in new.go) — so the scaffold can reach this point with no
	// TypeScript. When that happens npx does not say so: with no local tsc it
	// falls back to the registry and dies on `npx canceled due to missing
	// packages and no YES option: ["tsc@2.0.4"]`, naming a 2016 package
	// unrelated to anything here. That failure is indistinguishable from a
	// real contract regression at a glance, and it cost a full CI cycle to
	// tell apart. Assert the precondition so a missing install says so.
	//
	// Look in BOTH layouts, because which one holds the binary is npm's
	// business, not this test's. A dev build of forge writes a gitignored npm
	// workspace root above the frontends (internal/generator/
	// frontend_webruntime_devlink.go) and npm then HOISTS every member's
	// dependencies to <project>/node_modules, leaving
	// frontends/<name>/node_modules absent entirely. Checking only the
	// frontend-local path reported "npm install did not complete" for a
	// perfectly good install — a false environment failure that hid the real
	// state of the tree. npx itself walks up, so either location works.
	tscCandidates := []string{
		filepath.Join(webDir, "node_modules", ".bin", "tsc"),
		filepath.Join(projectDir, "node_modules", ".bin", "tsc"),
	}
	haveTSC := false
	for _, candidate := range tscCandidates {
		if _, err := os.Stat(candidate); err == nil {
			haveTSC = true
			break
		}
	}
	if !haveTSC {
		t.Fatalf("scaffold has no local tsc — `npm install` during `forge project new` did not "+
			"complete (it only warns on failure), so this gate cannot run. This is an environment "+
			"failure, NOT an error-contract regression. Looked in: %v", tscCandidates)
	}

	// ── POSITIVE: the documented contract must typecheck ──────────────
	// Every field the runtime promises, plus the render path the docs
	// prescribe. If any of these is unreachable through the type system, the
	// call site is pushed back to matching on message prose.
	writeFileE2E(t, filepath.Join(webDir, "src", "app", "errorcontract", "page.tsx"),
		fmt.Sprintf(errorContractPositiveFixture, hooksModule))
	runCmdTimeout(t, webDir, 3*time.Minute, "npx", "--no-install", "tsc", "--noEmit")

	// ── NEGATIVE: a field that exists on no type must FAIL ────────────
	// Without this half, the positive half passes just as green when the
	// error is `any`.
	negativePath := filepath.Join(webDir, "src", "app", "errorcontract", "negative.ts")
	writeFileE2E(t, negativePath, fmt.Sprintf(errorContractNegativeFixture, hooksModule))
	out, err := runCmdCombined(webDir, 3*time.Minute, "npx", "--no-install", "tsc", "--noEmit")
	if err == nil {
		t.Fatalf("tsc accepted a nonexistent field on the hook's error — the error type is `any` "+
			"or otherwise not load-bearing, and the positive half above proves nothing:\n%s", out)
	}
	if !strings.Contains(out, "nonexistentField") {
		t.Errorf("tsc failed, but not on the bogus field — the negative half is not testing what it claims:\n%s", out)
	}
	// The type NAME in the diagnostic is what pins the error to the runtime's
	// shape rather than to React Query's default `Error` (the shipped defect)
	// or to a structural stand-in.
	if !strings.Contains(out, "ConnectClientError") {
		t.Errorf("tsc rejected the bogus field, but not against ConnectClientError — "+
			"the hook's error is some other type:\n%s", out)
	}
}

// runCmdCombined runs a command and returns its combined output and error
// WITHOUT failing the test — the must-fail half of a compile gate needs the
// non-zero exit as data, not as a fatal.
func runCmdCombined(dir string, timeout time.Duration, name string, args ...string) (string, error) {
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	go func() {
		out, err := cmd.CombinedOutput()
		done <- result{out: string(out), err: err}
	}()
	select {
	case r := <-done:
		return r.out, r.err
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("command %q timed out after %s in %s",
			append([]string{name}, args...), timeout, dir)
	}
}

// errorContractPositiveFixture reads the whole documented error contract off a
// generated hook: the machine-readable reason the UI branches on, the Connect
// code, the retry verdict, the status, and userMessage() for display.
//
// %s is the generated hooks module specifier, derived from
// naming.ServiceHookFile so the fixture and the file on disk cannot drift.
const errorContractPositiveFixture = `"use client";

import { userMessage } from "@reliantlabs/forge-web-runtime";

import { useCreateItem, useListItems } from "%s";

export default function ErrorContractPage() {
  const create = useCreateItem();
  const list = useListItems({});

  if (create.error) {
    // Branching on the stable machine code — the thing the runtime's docs
    // say to key off, and the thing a bare Error made unreachable.
    switch (create.error.reason) {
      case "duplicate":
        return <p>That name is taken.</p>;
      case "reference_missing":
        return <p>Pick a category that still exists.</p>;
      default:
        return <p>{userMessage(create.error)}</p>;
    }
  }
  if (list.error) {
    const retry: boolean = list.error.retryable;
    const status: number = list.error.status;
    const code: string = list.error.codeName;
    // A ConnectError subclass: the numeric Code is still .code.
    const numeric: number = list.error.code;
    return <p>{` + "`${code} ${numeric} ${status} ${retry} ${userMessage(list.error)}`" + `}</p>;
  }
  return <button onClick={() => create.mutate({ name: "x" })}>create</button>;
}
`

// errorContractNegativeFixture MUST NOT compile. It is the half that
// distinguishes a real ConnectClientError from `any`. %s is the generated
// hooks module specifier, as above.
const errorContractNegativeFixture = `import { useCreateItem } from "%s";

export function bogus() {
  const create = useCreateItem();
  return create.error?.nonexistentField;
}
`
