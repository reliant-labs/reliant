// Copyright (c) 2025 Reliant Labs
package catalog

import (
	"context"
	"io"
	"os"
	"testing"

	forgecli "github.com/reliant-labs/forge/cli"
	"github.com/reliant-labs/reliant/internal/forgepin"
)

// TestMain keeps every test off a real pinned forge. Resolving one can build
// forge (`go install`, a minute), and a GOWORK in the environment that links a
// forge checkout makes every temp-dir project "linked" to it, so each body
// load would exec a checkout build: the package timed out that way. The
// embedded forge serves every test unless the test installs a fake
// (useFakePinnedForge).
func TestMain(m *testing.M) {
	pinnedForge = &forgepin.Source{
		Resolve: func(context.Context, string, string, io.Writer) (forgecli.PinnedForge, bool, error) {
			return forgecli.PinnedForge{}, false, nil
		},
	}
	os.Exit(m.Run())
}
