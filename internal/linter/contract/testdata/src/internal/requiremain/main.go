// A command under internal/: exported methods and an exported var, no
// contract.go and no //forge:exclude-contract. Nothing can import a main
// package, so neither requirecontract nor exportedvars may report it — no
// `want` comments here.
package main

import (
	"flag"
	"strings"
)

// Verbose is exported, but no package can import this one to read it.
var Verbose = false

// dirs is a repeatable flag: flag.Value's Set is the exported method the
// stdlib requires by name.
type dirs []string

func (d *dirs) String() string     { return strings.Join(*d, ",") }
func (d *dirs) Set(v string) error { *d = append(*d, v); return nil }

type runner struct{}

// Run is an ordinary exported method on a command's own type.
func (runner) Run() error { return nil }

func main() {
	var d dirs
	flag.Var(&d, "dir", "a directory (repeatable)")
	flag.Parse()
	_ = runner{}.Run()
}
