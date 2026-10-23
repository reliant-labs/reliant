package lint

import (
	"github.com/reliant-labs/forge/internal/cli/factory"
)

// testFactory returns a Factory for constructing the lint command in tests.
// newCmd does not consult the Factory (the lint substrate reaches the
// project-store loader package-level), so an empty Factory suffices.
func testFactory() *factory.Factory { return &factory.Factory{} }
