package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/internal/toolpin"
	"github.com/reliant-labs/forge/pkg/toolpins"
)

// toolPins are the tool versions this forge pins, keyed by binary name and
// spelled exactly as the templates constants are — the strings the scaffolded
// workflows render. It is what `forge tools pins` prints, so a CI workflow
// asks the forge it runs instead of grepping forge's source out of the module
// cache.
func toolPins() map[string]string {
	root, _ := projectRoot()
	return map[string]string{
		"golangci-lint": templates.GolangciLintVersion,
		// The project's buf when its go.mod declares one (internal/toolpin).
		"buf":         toolpin.BufFor(root).Bare(),
		"govulncheck": templates.GovulncheckVersion,
		"go-licenses": templates.GoLicensesVersion,
		"prettier":    templates.PrettierVersion,
		"kcl":         templates.KCLVersion,
		// Release binaries, each archive sha256-verified (internal/toolfetch).
		"syft":  templates.SyftVersion,
		"grype": templates.GrypeVersion,
	}
}

func newToolsPinsCmd() *cobra.Command {
	var asJSON, archives bool
	cmd := &cobra.Command{
		Use:   "pins [tool]",
		Short: "Print the tool versions this forge pins (golangci-lint, buf, …), for CI to read",
		Long: `Print the tool versions this forge pins: the golangci-lint and buf that
'forge lint' gates with, and the other tools forge installs or renders into
scaffolded CI.

With a tool name, print only its version — the form a CI step reads:

  GOLANGCI_LINT_VERSION="$(forge tools pins golangci-lint)"

The versions are this forge binary's, so a workflow that runs the forge its
go.mod pins reads the matching tool pins with no second place to bump. The one
the project can move is buf: inside a project whose go.mod requires
github.com/bufbuild/buf (` + "`go get -tool github.com/bufbuild/buf/cmd/buf@vX`" + `),
buf is that version, the one ` + "`forge generate`" + ` and ` + "`forge ci gate`" + ` run.

syft (SBOM generation) and grype (vulnerability scanning) are installed from
their upstream release archives, each verified against a sha256 compiled into
this forge, into forge's tool cache ($FORGE_TOOLS_CACHE/release) on first use
or by ` + "`forge tools install syft grype`" + `.

--archives prints, as JSON, the release archives of the tools forge
installs as binaries (syft, grype): per platform, the archive's name, URL
and sha256, and the sha256 of the executable inside it — the same data the public Go package
github.com/reliant-labs/forge/pkg/toolpins exports, for a Dockerfile or a CI
step that installs the same binary forge pins. --json keeps its shape, a
flat object of tool → version.

Examples:
  forge tools pins
  forge tools pins golangci-lint
  forge tools pins --json
  forge tools pins grype --archives`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if archives {
				return printToolArchives(args)
			}
			pins := toolPins()
			if len(args) == 1 {
				v, ok := pins[args[0]]
				if !ok {
					return cliutil.UserErr("forge tools pins", fmt.Sprintf("no pinned tool named %q", args[0]), "",
						"one of: "+strings.Join(sortedKeys(pins), ", "))
				}
				if asJSON {
					return json.NewEncoder(os.Stdout).Encode(map[string]string{args[0]: v})
				}
				fmt.Println(v)
				return nil
			}
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(pins)
			}
			for _, name := range sortedKeys(pins) {
				fmt.Printf("%-14s %s\n", name, pins[name])
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print a JSON object of tool → version")
	cmd.Flags().BoolVar(&archives, "archives", false, "Print the release archives (name, URL, sha256 per platform) of the binary tools, syft and grype, as JSON")
	return cmd
}

// toolArchives is one tool of `forge tools pins --archives`: pkg/toolpins'
// Tool with each archive's download URL beside it.
type toolArchives struct {
	toolpins.Tool
	Archives []toolArchive `json:"archives"`
}

type toolArchive struct {
	toolpins.Archive
	URL string `json:"url"`
}

// printToolArchives prints every binary tool's archives, or only the named
// tool's (one object rather than an array).
func printToolArchives(args []string) error {
	tools := toolpins.All()
	if len(args) == 1 {
		t, ok := toolpins.Lookup(args[0])
		if !ok {
			names := make([]string, 0, len(tools))
			for _, t := range tools {
				names = append(names, t.Name)
			}
			return cliutil.UserErr("forge tools pins --archives", fmt.Sprintf("%q is not a tool forge installs from release archives", args[0]), "",
				"one of: "+strings.Join(names, ", "))
		}
		tools = []toolpins.Tool{t}
	}
	out := make([]toolArchives, 0, len(tools))
	for _, t := range tools {
		ta := toolArchives{Tool: t, Archives: make([]toolArchive, 0, len(t.Archives))}
		for _, a := range t.Archives {
			url, _ := t.ReleaseURL(a.GOOS, a.GOARCH)
			ta.Archives = append(ta.Archives, toolArchive{Archive: a, URL: url})
		}
		out = append(out, ta)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if len(args) == 1 {
		return enc.Encode(out[0])
	}
	return enc.Encode(out)
}
