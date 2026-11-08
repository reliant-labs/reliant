// Command mockgen regenerates every contract mock (mock_gen.go) under a
// project's internal/ tree WITHOUT building forge.
//
// It exists for forge's own repository (forge#1261). forge's binary imports
// packages that carry generated mocks, so a contract interface that gains a
// method leaves its mock_gen.go stale, that package stops compiling, and so
// does the forge binary whose `forge generate` would refresh the mock. This
// tool links only the contract generator and the packages it needs, none of
// which carries generated contract code (TestMockgenImportsNoContractCodegen
// holds that), so a stale mock can never stop it from compiling. `task
// generate` runs it before building forge:
//
//	go run ./internal/tools/mockgen
//
// The mocks come from the same code `forge generate` runs
// (contract.Targets, contract.GenerateMock, the same forge.yaml options), so
// they are byte-identical to forge's own render and the `forge generate` that
// follows changes nothing it wrote. It refreshes mocks only: a package's
// observability decorator (middleware_gen.go) needs the rest of forge's
// codegen, and forge's own packages have none.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator/contract"
)

func main() {
	root := flag.String("root", ".", "project root (the directory holding forge.yaml and internal/)")
	flag.Parse()
	if err := run(*root, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mockgen:", err)
		os.Exit(1)
	}
}

// run regenerates the mocks under root/internal and reports each one it
// changed on out.
func run(root string, out io.Writer) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	// forge.yaml is optional, exactly as it is to the contracts step: no
	// file means nothing is excluded and no extra interface types.
	var contracts config.ContractsConfig
	cfg, err := config.LoadProjectDir(root)
	switch {
	case err == nil:
		contracts = cfg.Contracts
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("load forge.yaml: %w", err)
	}
	// Disowned mocks stay the user's: the write path consults the ownership
	// state just as it does under `forge generate`.
	cs, err := checksums.Load(root)
	if err != nil {
		return fmt.Errorf("load .forge ownership state: %w", err)
	}

	targets := contract.Targets(filepath.Join(root, "internal"), root, contracts.IsExcluded)
	opts := contract.Options{
		ExtraInterfaceTypes: contracts.InterfaceTypeSet(),
		ProjectRoot:         root,
		Checksums:           cs,
		Resolver:            contract.NewResolver(targets...),
	}
	generated, changed := 0, 0
	for _, target := range targets {
		dir := filepath.Dir(target)
		if filepath.Base(target) == "contract.go" && contract.ExcludedByDirective(dir) {
			continue
		}
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			return err
		}
		generated++
		before := contract.SnapshotArtifacts(dir)
		if err := contract.GenerateMock(target, opts); err != nil {
			return fmt.Errorf("generate mock for %s: %w", filepath.ToSlash(rel), err)
		}
		if before.ChangedIn(dir) {
			fmt.Fprintf(out, "mockgen: refreshed %s\n", filepath.ToSlash(filepath.Join(rel, "mock_gen.go")))
			changed++
		}
	}
	fmt.Fprintf(out, "mockgen: %d contract mock(s), %d refreshed\n", generated, changed)
	return nil
}
