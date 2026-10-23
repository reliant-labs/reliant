package kclplugin

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	kcl "kcl-lang.io/kcl-go"
)

func TestInvokeShapes(t *testing.T) {
	saved := methods
	t.Cleanup(func() { methods = saved })
	methods = map[string]methodSpec{
		"ok":    {Body: func(a *methodArgs) (any, error) { return a.intArg(0) + a.intArg(1), nil }},
		"fail":  {Body: func(*methodArgs) (any, error) { return nil, os.ErrInvalid }},
		"panic": {Body: func(a *methodArgs) (any, error) { return a.strArg(5), nil }},
	}
	cases := []struct{ method, args, want string }{
		{"kcl_plugin.forge.ok", "[1,2]", "3"},
		{"kcl_plugin.forge.fail", "", `{"__kcl_PanicInfo__":"invalid argument"}`},
		{"kcl_plugin.forge.nope", "", `{"__kcl_PanicInfo__":"invalid method: kcl_plugin.forge.nope, not found"}`},
		{"other.ns.ok", "", `{"__kcl_PanicInfo__":"invalid method: other.ns.ok, not found"}`},
	}
	for _, c := range cases {
		if got := invoke(c.method, c.args, ""); got != c.want {
			t.Errorf("%s: got %s want %s", c.method, got, c.want)
		}
	}
	if got := invoke("kcl_plugin.forge.panic", "[]", ""); !strings.Contains(got, "__kcl_PanicInfo__") {
		t.Errorf("panic not recovered into PanicInfo: %s", got)
	}
}

// TestSerializedInstallsBridgeBeforeFirstKCLCall runs in a FRESH process so
// the native client singleton is uninitialized: the first KCL call the
// process makes goes through Serialized with no explicit Register. If
// Serialized did not install the bridge first, the client would initialize
// plugin-less and `import kcl_plugin.forge` would fail.
func TestSerializedInstallsBridgeBeforeFirstKCLCall(t *testing.T) {
	if os.Getenv("KCLPLUGIN_FRESH_CHILD") == "1" {
		res, err := Serialized(func() (*kcl.KCLResultList, error) {
			return kcl.Run("main.k", kcl.WithCode("import kcl_plugin.forge as fp\nport = fp.resolve_port(\"fresh-proc-probe\", 0)\n"))
		})
		if err != nil {
			t.Fatalf("first KCL call in a fresh process failed: %v", err)
		}
		if out := res.GetRawYamlResult(); !strings.Contains(out, "port:") {
			t.Fatalf("unexpected result: %q", out)
		}
		return
	}
	if testing.Short() {
		t.Skip("re-execs the test binary")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSerializedInstallsBridgeBeforeFirstKCLCall$")
	cmd.Env = append(os.Environ(), "KCLPLUGIN_FRESH_CHILD=1", "FORGE_HOME="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fresh child failed: %v\n%s", err, out)
	}
}
