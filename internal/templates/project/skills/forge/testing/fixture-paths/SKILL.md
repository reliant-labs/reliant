---
name: fixture-paths
description: "Find a test's testdata and the module root from the package directory, never from runtime.Caller — which -trimpath, forge's default build mode, turns into a module path — and the caller-paths lint lane that flags it."
---

# Find testdata from the package directory, never from runtime.Caller

`-trimpath` is forge's default build mode (the scaffolded Taskfile adds it to
`GOFLAGS`), and under it `runtime.Caller` reports a module-relative path such as
`example.com/app/internal/x/x_test.go`, not a file on disk. So a test that finds
its testdata or the repository root from its own source path passes in a plain
`go test` and fails in a trimmed one. `go test` runs each test in its package's
source directory, so resolve paths against that instead:

```go
fixture, err := filepath.Abs("testdata/orders.json") // the package dir is the cwd

// The module root: walk up from the cwd to the go.mod.
root, _ := os.Getwd()
for {
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
		break
	}
	parent := filepath.Dir(root)
	if parent == root {
		t.Fatal("no go.mod above the package directory")
	}
	root = parent
}
```

`forge lint` flags the other shape (the `caller-paths` lane, also
`forge lint --caller-paths` alone), in every `.go` file:

| Rule | Flags | Does not flag |
|---|---|---|
| `forge-runtime-caller-path` | a `runtime.Caller` call whose file result is kept in a variable (`_, file, _, ok := runtime.Caller(0)`) and used as anything but text: joined, passed to `filepath.Dir` or a helper, returned, stored in a field | the file discarded (`_`); a file only displayed (`filepath.Base`, or `fmt` / `log` / `slog` / `errors`) |

A deliberate use is suppressed at its line with the reason
(`// forge:lint-disable-next-line forge-runtime-caller-path: <why>`). The rule
warns in a project pinned below v1.12.0, then gates; `lint.rules` raises or
lowers it like any rule.
