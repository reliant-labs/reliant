package hostlaunch

import (
	"slices"
	"testing"
)

// TestWithPGO: a binary built with PGO disabled gets -pgo=off in GOFLAGS when
// its runner compiles it on launch (go-run, air), keeping the user's GOFLAGS;
// a -pgo the user already set wins, and runners that execute a forge-built
// binary (or a verbatim command) are left alone.
func TestWithPGO(t *testing.T) {
	base := []string{"PATH=/bin", "GOFLAGS=-mod=mod"}
	cases := []struct {
		name string
		spec RunnerSpec
		env  []string
		want []string
	}{
		{"go-run", RunnerSpec{PGOOff: true, GoPkg: "./cmd/app"}, base, []string{"PATH=/bin", "GOFLAGS=-mod=mod -pgo=off"}},
		{"air", RunnerSpec{Runner: "air", PGOOff: true}, []string{"PATH=/bin"}, []string{"PATH=/bin", "GOFLAGS=-pgo=off"}},
		{"enabled", RunnerSpec{GoPkg: "./cmd/app"}, base, base},
		{"user -pgo wins", RunnerSpec{PGOOff: true, GoPkg: "./cmd/app"}, []string{"GOFLAGS=-pgo=auto"}, []string{"GOFLAGS=-pgo=auto"}},
		{"binary runner", RunnerSpec{Runner: "binary", PGOOff: true, GoPkg: "./cmd/app"}, base, base},
		{"delve runner", RunnerSpec{Runner: "delve", PGOOff: true, GoPkg: "./cmd/app"}, base, base},
		{"verbatim command", RunnerSpec{PGOOff: true, Command: []string{"go", "run", "../sib/cmd/x"}}, base, base},
	}
	for _, c := range cases {
		in := slices.Clone(c.env)
		if got := WithPGO(c.spec, c.env); !slices.Equal(got, c.want) {
			t.Errorf("%s: env = %q, want %q", c.name, got, c.want)
		}
		if !slices.Equal(c.env, in) {
			t.Errorf("%s: WithPGO modified its input", c.name)
		}
	}
}
