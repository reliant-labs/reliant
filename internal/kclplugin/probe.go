package kclplugin

import (
	"fmt"
	"strings"

	kcl "kcl-lang.io/kcl-go"
)

// probeToken is echoed through kcl_plugin.forge.probe. Distinctive so a
// wrong-but-successful evaluation cannot pass by accident.
const probeToken = "forge-kcl-plugin-probe-7f3a"

// Probe evaluates a one-line KCL program that calls kcl_plugin.forge.probe
// and checks the value comes back. It answers "can this process render an
// environment?" by doing the smallest possible render, so it fails for every
// reason a real render would fail before reaching project code:
//
//   - libkcl (kcl.dll / libkcl.so / libkcl.dylib) could not be extracted into
//     the cache dir or loaded — antivirus quarantine, a read-only TEMP, a
//     KCL_LIB_HOME pointing nowhere;
//   - the plugin callback is not installed, so kcl_plugin.forge is "not found";
//   - the callback is installed but the round trip (args in, result out) is
//     broken.
//
// It goes through Serialized, the same entry every real evaluation uses, so it
// exercises the same native client and the same callback — a probe with its
// own code path would prove nothing about renders. A runtime that cannot load
// arrives here as Serialized's error (see install), not a panic.
func Probe() error {
	code := "import kcl_plugin.forge as fp\nprobe = fp.probe(\"" + probeToken + "\")\n"
	res, err := Serialized(func() (*kcl.KCLResultList, error) {
		return kcl.Run("probe.k", kcl.WithCode(code))
	})
	if err != nil {
		return err
	}
	if got := res.GetRawYamlResult(); !strings.Contains(got, probeToken) {
		return fmt.Errorf("kcl_plugin.forge.probe returned %q, want a result containing %q", strings.TrimSpace(got), probeToken)
	}
	return nil
}
