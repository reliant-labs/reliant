package codegen

import (
	"net"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/configproto"
	"github.com/reliant-labs/forge/internal/templates"
)

// The pprof wire has TWO ends and they have to agree.
//
// The incident this guards against: a project's config schema projected a
// PPROF_ADDR env var that nothing ever set, so it was declared on one side of
// the wire and read by neither. A gateway then sat at ~1 GB of anonymous
// memory being OOMKilled, with no way to ask the process WHAT it was holding
// — the cgroup's memory.stat says how much and never what, and the binary had
// no pprof listener at all. A thirty-second heap profile would have answered
// it.
//
// Three artifacts have to carry the same fact for pprof to be genuinely on by
// default in a scaffolded project:
//
//   - proto/config/v1/config.proto (config.proto.tmpl) — the field the
//     runtime loader reads the default off, and the source the KCL schema +
//     env projection are generated from.
//   - DefaultConfigMessages() — the same table, used at initial scaffold
//     before any descriptor exists.
//   - `forge env status`'s pprof check, which resolves an address by reading
//     PPROF_ADDR off the service's env (internal/cli, pprofPortFromAddr).
//
// Any one of them drifting reintroduces the failure silently: pprof looks
// declared and is not actually served. So they are pinned together here.
func TestScaffoldDefaultsPprofOn(t *testing.T) {
	// The proto template's default_value.
	out, err := templates.ProjectTemplates().Render("config.proto.tmpl", configproto.For("proj").TemplateData("example.com/proj"))
	if err != nil {
		t.Fatalf("render config.proto.tmpl: %v", err)
	}
	protoDefault := pprofDefaultFromProto(t, string(out))

	// The Go-side table's default_value.
	var tableDefault string
	var found bool
	for _, m := range DefaultConfigMessages() {
		for _, f := range m.Fields {
			if f.EnvVar == "PPROF_ADDR" {
				tableDefault, found = f.DefaultValue, true
			}
		}
	}
	if !found {
		t.Fatal("DefaultConfigMessages() declares no PPROF_ADDR field — the scaffold cannot wire pprof at all")
	}

	if protoDefault != tableDefault {
		t.Fatalf("pprof default drifted between the two scaffold sources:\n"+
			"  config.proto.tmpl      = %q\n"+
			"  DefaultConfigMessages  = %q\n"+
			"They generate the same field; a mismatch means a project scaffolded before "+
			"its descriptor exists gets a different pprof address than one scaffolded after.",
			protoDefault, tableDefault)
	}

	// ON by default — an empty default is what "declared and never served"
	// looked like.
	if protoDefault == "" {
		t.Fatal("pprof_addr has no default_value, so a scaffolded binary starts no pprof listener. " +
			"A profiler you must redeploy to switch on is a profiler you do not have when you need it.")
	}

	// LOOPBACK by default — that is what makes always-on safe: the listener
	// exists in every environment and is routable from none of them.
	host, port, err := net.SplitHostPort(protoDefault)
	if err != nil {
		t.Fatalf("pprof default %q is not a host:port Go listen address: %v", protoDefault, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Errorf("pprof default host = %q, want a loopback IP. A wildcard bind makes an "+
			"always-on heap-dump endpoint reachable from every other pod in the namespace "+
			"and from the LAN on a laptop; the container-boundary override belongs in "+
			"docker-compose.yml, not in the default.", host)
	}

	// And the port has to be one `forge env status` can actually extract, or
	// the check goes back to reporting UNDETERMINED against a scaffold that
	// is in fact serving profiles.
	if n, convErr := strconv.Atoi(port); convErr != nil || n <= 0 {
		t.Errorf("pprof default port = %q, which the env-status resolver cannot read as a port", port)
	}
}

// pprofDefaultFromProto pulls default_value out of the rendered config
// proto's pprof_addr option block.
func pprofDefaultFromProto(t *testing.T, proto string) string {
	t.Helper()
	idx := strings.Index(proto, "string pprof_addr = ")
	if idx < 0 {
		t.Fatal("the rendered config.proto declares no pprof_addr field")
	}
	rest := proto[idx:]
	end := strings.Index(rest, "}]")
	if end < 0 {
		t.Fatal("config.proto's pprof_addr has an unterminated option block")
	}
	block := rest[:end]
	if !strings.Contains(block, `env_var: "PPROF_ADDR"`) {
		t.Fatalf("pprof_addr is not bound to PPROF_ADDR — env status reads that name:\n%s", block)
	}
	m := regexp.MustCompile(`default_value:\s*"([^"]*)"`).FindStringSubmatch(block)
	if m == nil {
		return ""
	}
	return m[1]
}
