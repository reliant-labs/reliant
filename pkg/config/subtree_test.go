package config

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// A subtree config's flags are persistent on the subtree's command, so a
// command beneath it loads the flag a user typed after either of them, and
// the generated loader refuses a command outside every subtree.

func subtreeTree() (root, daemon, start, server, api *cobra.Command) {
	root = &cobra.Command{Use: "reliant"}
	daemon = &cobra.Command{Use: "daemon"}
	start = &cobra.Command{Use: "start", RunE: func(*cobra.Command, []string) error { return nil }}
	server = &cobra.Command{Use: "server"}
	api = &cobra.Command{Use: "api", RunE: func(*cobra.Command, []string) error { return nil }}
	daemon.AddCommand(start)
	server.AddCommand(api)
	root.AddCommand(daemon, server)
	return
}

func TestRegisterInheritedFlags_ALeafBelowTheSubtreeLoadsTheFlag(t *testing.T) {
	root, daemon, start, _, _ := subtreeTree()
	m := buildBlockMessage(t)
	if err := RegisterInheritedFlags(daemon, m.Interface()); err != nil {
		t.Fatal(err)
	}
	if daemon.PersistentFlags().Lookup("port") == nil {
		t.Fatal("RegisterInheritedFlags must register persistent flags on the subtree's command")
	}
	if daemon.PersistentFlags().Lookup(ConfigFlag) != nil {
		t.Errorf("RegisterInheritedFlags must leave --%s to the root config", ConfigFlag)
	}
	root.SetArgs([]string{"daemon", "start", "--port", "7070"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if err := LoadInto(start, m.Interface()); err != nil {
		t.Fatalf("LoadInto from `daemon start`: %v", err)
	}
	if got := m.Get(m.Descriptor().Fields().ByName("port")).Int(); got != 7070 {
		t.Errorf("port = %d, want 7070 from the inherited flag", got)
	}
}

func TestCheckCommandSubtree(t *testing.T) {
	_, daemon, start, server, api := subtreeTree()
	subtrees := [][]string{{"daemon"}, {"open"}}
	for _, ok := range []*cobra.Command{daemon, start} {
		if err := CheckCommandSubtree(ok, "CLIConfig", subtrees...); err != nil {
			t.Errorf("%s is under a subtree: %v", ok.CommandPath(), err)
		}
	}
	for _, bad := range []*cobra.Command{server, api} {
		err := CheckCommandSubtree(bad, "CLIConfig", subtrees...)
		if err == nil {
			t.Errorf("%s is under no subtree of CLIConfig; want a refusal", bad.CommandPath())
			continue
		}
		for _, want := range []string{`"daemon", "open"`, "`" + bad.CommandPath() + "` loaded it", "LoadCLIConfig"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal missing %q: %v", want, err)
			}
		}
	}
	// A prefix of a NAME is not a prefix of the path: `daemonx` is not under `daemon`.
	root := &cobra.Command{Use: "reliant"}
	dx := &cobra.Command{Use: "daemonx"}
	root.AddCommand(dx)
	if err := CheckCommandSubtree(dx, "CLIConfig", subtrees...); err == nil {
		t.Error("`daemonx` must not count as under `daemon`")
	}
	if err := CheckCommandSubtree(&cobra.Command{Use: "detached"}, "CLIConfig", subtrees...); err != nil {
		t.Errorf("a detached command is not checked: %v", err)
	}
}
