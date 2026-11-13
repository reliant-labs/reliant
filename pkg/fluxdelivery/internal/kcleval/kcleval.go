// Package kcleval evaluates a KCL program against the forge module embedded in
// this binary — the module a released forge renders with — for fluxdelivery's
// generator and its Go/KCL agreement tests.
//
// It is a separate package so that pkg/fluxdelivery itself, which
// control-plane imports, never links the KCL runtime.
package kcleval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// kclMod is the throwaway project's kcl.mod; `import forge` resolves to the
// embedded module (internal/kclvendor), not to a dependency.
const kclMod = "[package]\nname = \"fluxdelivery\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n"

// Eval renders main (a KCL program that may `import forge`) in a fresh
// directory under tmp and decodes its top-level result into out.
func Eval(tmp, main string, out any) error {
	dir, err := os.MkdirTemp(tmp, "fluxdelivery-kcl-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for name, body := range map[string]string{"kcl.mod": kclMod, "main.k": main} {
		if err := atomicfile.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	kclplugin.Register()
	raw, err := kclrender.Run(dir, dir, nil)
	if err != nil {
		return fmt.Errorf("kcleval: render: %w", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("kcleval: decoding the render: %w\n%s", err, raw)
	}
	return nil
}

// Template is kcl/lib/flux_delivery.k's FLUX_DELIVERY_TEMPLATE, evaluated.
func Template(tmp string) (string, error) {
	var out struct {
		Template string `json:"template"`
	}
	if err := Eval(tmp, "import forge\n\ntemplate = forge.FLUX_DELIVERY_TEMPLATE\n", &out); err != nil {
		return "", err
	}
	if out.Template == "" {
		return "", fmt.Errorf("kcleval: forge.FLUX_DELIVERY_TEMPLATE evaluated to an empty string")
	}
	return out.Template, nil
}
