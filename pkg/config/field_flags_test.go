package config

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// A non-serving command (an admin task) registers the flags of the fields it
// reads, not the server's whole surface: friction #25, where `ranger apikey
// create --help` listed ~60 CORS/OIDC/TLS/pprof flags beside its four own.
func TestRegisterFieldFlags_RegistersOnlyTheNamedFields(t *testing.T) {
	m := buildTestMessage(t)
	cmd := &cobra.Command{Use: "report", RunE: func(*cobra.Command, []string) error { return nil }}
	if err := RegisterFieldFlags(cmd, m.Interface(), "log_level", "timeout"); err != nil {
		t.Fatalf("RegisterFieldFlags: %v", err)
	}
	for _, want := range []string{"log-level", "timeout"} {
		if cmd.Flags().Lookup(want) == nil {
			t.Errorf("--%s not registered", want)
		}
	}
	for _, not := range []string{"port", "enabled", "ratio"} {
		if cmd.Flags().Lookup(not) != nil {
			t.Errorf("--%s registered, but the command named only log_level and timeout", not)
		}
	}
	if lookupConfigFlag(cmd) == nil {
		t.Error("--config not registered: the file layer must stay reachable")
	}

	// The registered flags load with the normal precedence, and an
	// unregistered field still loads from env.
	t.Setenv("API_KEY", "sekret")
	t.Setenv("PORT", "9000")
	cmd.SetArgs([]string{"--log-level", "debug", "--timeout", "3s"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if err := LoadInto(cmd, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	if got := strOf(m, "log_level"); got != "debug" {
		t.Errorf("log_level = %q, want the flag's debug", got)
	}
	if got := intOf(m, "port"); got != 9000 {
		t.Errorf("port = %d, want 9000 from env: a field with no flag here still loads", got)
	}
}

func TestRegisterFieldFlags_NoPathsRegistersOnlyTheConfigFlag(t *testing.T) {
	cmd := &cobra.Command{Use: "report"}
	if err := RegisterFieldFlags(cmd, buildTestMessage(t).Interface()); err != nil {
		t.Fatal(err)
	}
	var names []string
	cmd.LocalFlags().VisitAll(func(f *pflag.Flag) { names = append(names, f.Name) })
	if len(names) != 1 || names[0] != ConfigFlag {
		t.Errorf("registered %v, want --%s alone", names, ConfigFlag)
	}
}

func TestRegisterFieldFlags_ABlockPathRegistersTheBlocksLeaves(t *testing.T) {
	m := buildBinaryConfig(t, "config.fieldflags.v1", "ToolConfig", "region", "REGION", "region", "eu")
	cmd := &cobra.Command{Use: "tool"}
	if err := RegisterFieldFlags(cmd, m.Interface(), "base"); err != nil {
		t.Fatal(err)
	}
	if cmd.Flags().Lookup("log-level") == nil {
		t.Error("the base block's --log-level not registered")
	}
	if cmd.Flags().Lookup("region") != nil {
		t.Error("--region registered, but only the base block was named")
	}
	cmd2 := &cobra.Command{Use: "tool"}
	if err := RegisterFieldFlags(cmd2, m.Interface(), "base.log_level"); err != nil || cmd2.Flags().Lookup("log-level") == nil {
		t.Errorf("a dotted leaf path: err %v, --log-level registered %v", err, cmd2.Flags().Lookup("log-level") != nil)
	}
}

func TestRegisterFieldFlags_RefusesWhatCannotBeAFlag(t *testing.T) {
	for path, want := range map[string]string{
		"no_such":        "has no field no_such",
		"log_level.deep": "is not a config block",
		"max_bytes":      "declares no flag",
		"api_key":        "is sensitive",
	} {
		cmd := &cobra.Command{Use: "report"}
		err := RegisterFieldFlags(cmd, buildTestMessage(t).Interface(), "log_level", path)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want it to contain %q", path, err, want)
		}
		if cmd.Flags().Lookup("log-level") != nil {
			t.Errorf("%s: a refused call registered flags anyway", path)
		}
	}
}
