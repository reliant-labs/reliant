// Command npm-overlay serves this checkout's npm packages beside the public
// registry (package npmoverlay) until it is stopped. It is CI plumbing, run
// with `go run ./internal/npmoverlay/cmd/npm-overlay`, and no part of the
// forge binary.
//
//	npm-overlay -pack DIR [-pack DIR...] -env-file FILE [-upstream URL]
//
// Each DIR is packed (npm pack, lifecycle scripts included, as npm publish
// runs them) before anything is written, so a build failure fails the step
// that started the overlay rather than a later install. Then FILE receives
// one KEY=VALUE line per variable that routes npm here — the format of
// $GITHUB_ENV, and of `set -a; . FILE` — written atomically, so a poller sees
// it whole or not at all. The process then serves until SIGINT or SIGTERM.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/npmoverlay"
)

type dirs []string

func (d *dirs) String() string     { return strings.Join(*d, ",") }
func (d *dirs) Set(v string) error { *d = append(*d, v); return nil }

func main() {
	var packs dirs
	flag.Var(&packs, "pack", "a package checkout to overlay (repeatable)")
	envFile := flag.String("env-file", "", "write the routing environment here once ready")
	upstream := flag.String("upstream", npmoverlay.DefaultUpstream, "the registry everything else comes from")
	flag.Parse()
	if len(packs) == 0 || *envFile == "" {
		fmt.Fprintln(os.Stderr, "usage: npm-overlay -pack DIR [-pack DIR...] -env-file FILE [-upstream URL]")
		os.Exit(2)
	}
	if err := run(packs, *envFile, *upstream); err != nil {
		fmt.Fprintln(os.Stderr, "npm-overlay:", err)
		os.Exit(1)
	}
}

func run(packs []string, envFile, upstream string) error {
	var pkgs []npmoverlay.Package
	for _, d := range packs {
		abs, err := filepath.Abs(d)
		if err != nil {
			return err
		}
		pkgs = append(pkgs, npmoverlay.Package{Dir: abs})
	}
	o, err := npmoverlay.Start(npmoverlay.Options{Upstream: upstream, Packages: pkgs})
	if err != nil {
		return err
	}
	defer func() { _ = o.Close() }()
	if err := o.Pack(); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(envFile, []byte(strings.Join(o.Env(), "\n")+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "npm-overlay: serving %s at %s; routing environment in %s\n", strings.Join(packs, ", "), o.URL, envFile)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	return nil
}
