package contractcheck

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestLintDepsAreInterfaces_ValueStructWithFuncSeamsIsADep pins a decision.
// reliant's model_endpoint Deps carries `Policy netguard.Policy`, a VALUE
// struct of a bool and an exported func field (LookupIP), which looks like a
// bundle of func seams — and func fields are exempt (isFuncSeam). It is not
// one: its methods fall back to the system resolver when LookupIP is nil and
// build a dialing *http.Transport, so the zero value a test passes is live.
// The rule keeps reporting it, and says what fits this shape: derive the
// value from configuration in New. A pointer to a concrete type gets the
// ordinary remedy only.
func TestLintDepsAreInterfaces_ValueStructWithFuncSeamsIsADep(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	must(t, writeFile(filepath.Join(tmp, "go.mod"), "module example.com/app\n\ngo 1.24\n"))

	guardDir := filepath.Join(tmp, "internal", "netguard")
	must(t, mkdirAll(guardDir))
	must(t, writeFile(filepath.Join(guardDir, "netguard.go"), `package netguard

import (
	"context"
	"net"
	"net/http"
)

type Policy struct {
	AllowPrivate bool
	LookupIP     func(ctx context.Context, host string) ([]net.IP, error)
}

func (p Policy) lookup(ctx context.Context, host string) ([]net.IP, error) {
	if p.LookupIP != nil {
		return p.LookupIP(ctx, host)
	}
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

func (p Policy) CheckHost(ctx context.Context, host string) error {
	_, err := p.lookup(ctx, host)
	return err
}

func (p Policy) Transport() *http.Transport {
	return &http.Transport{DialContext: (&net.Dialer{}).DialContext}
}

type Repo struct{ dsn string }

func (r *Repo) Save(ctx context.Context, id string) error { return nil }
`))

	svcDir := filepath.Join(tmp, "internal", "endpoints")
	must(t, mkdirAll(svcDir))
	must(t, writeFile(filepath.Join(svcDir, "service.go"), `package endpoints

import "example.com/app/internal/netguard"

type Deps struct {
	Policy netguard.Policy
	Repo   *netguard.Repo
}
`))

	fs, err := Inspect(context.Background(), tmp, Options{Rules: []Rule{RuleDepsAreInterfaces}})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleDepsAreInterfaces))
	byField := map[string]string{}
	for _, f := range got {
		for _, name := range []string{"Policy", "Repo"} {
			if strings.Contains(f.Message, `"`+name+`"`) {
				byField[name] = f.Remediation
			}
		}
	}
	if len(got) != 2 || byField["Policy"] == "" || byField["Repo"] == "" {
		t.Fatalf("want Policy (a value struct with a live zero value) and Repo both reported, got:\n%s",
			AsResult(fs).FormatText())
	}
	if !strings.Contains(byField["Policy"], "struct VALUE") || !strings.Contains(byField["Policy"], "derive it in New") ||
		!strings.Contains(byField["Policy"], "type the field with an interface") {
		t.Errorf("a value-struct finding must lead with the derive-from-config fix and keep the interface one: %s",
			byField["Policy"])
	}
	if strings.Contains(byField["Repo"], "struct VALUE") {
		t.Errorf("a concrete pointer is not a value struct: %s", byField["Repo"])
	}
}
