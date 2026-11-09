package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/codegen"
)

// descriptorIgnoreBlock is what ensureDescriptorIgnored appends to a
// project's .gitignore, and what the scaffold's .gitignore carries.
const descriptorIgnoreBlock = `
# forge's proto descriptor: derived from proto/ by whichever forge command
# reads it, and never committed (commitpolicy: only what the compiler needs is
# committed). Added by ` + "`forge generate`" + `.
/` + codegen.DescriptorDir + `/
`

// ensureDescriptorIgnored makes the project's git ignore gen/forge_descriptor/:
// the migration for a project scaffolded while the descriptor was committed.
// A no-op outside a git work tree, or when a rule already ignores it. It
// appends to the project's own .gitignore (creating it if absent), the file
// the scaffold writes that rule into.
func ensureDescriptorIgnored(projectDir string) error {
	if out, err := exec.Command("git", "-C", projectDir, "rev-parse", "--is-inside-work-tree").Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		return nil
	}
	probe := codegen.DescriptorDir + "/services/x/v1/x.json"
	// --no-index: the answer must come from the rules, not from whether an
	// older forge's files are still tracked.
	if exec.Command("git", "-C", projectDir, "check-ignore", "-q", "--no-index", probe).Run() == nil {
		return nil
	}
	path := filepath.Join(projectDir, ".gitignore")
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read .gitignore: %w", err)
	}
	body := string(old)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += descriptorIgnoreBlock
	if err := atomicfile.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("ignore %s/ in .gitignore: %w", codegen.DescriptorDir, err)
	}
	fmt.Printf("  ✏️  .gitignore: now ignores %s/ (derived on demand, never committed)\n", codegen.DescriptorDir)
	return nil
}
