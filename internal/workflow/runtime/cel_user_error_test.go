// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/errclass"
)

// A workflow expression that fails on its own data (a missing key) or does
// not compile is its author's error. In prod each one was logged at ERROR by
// every layer that passed the run's failure up, and opened an obs issue
// (fce68f36 "[InlineLoop] Parallel iteration failed", 3b4272c4 "failed to
// resolve workflow templates: … no such key"). The text is unchanged; only the
// class says whose it is.
func TestCELFailuresAreTheWorkflowAuthorsError(t *testing.T) {
	t.Parallel()
	builder := NewCELContextBuilder().WithInputs(map[string]interface{}{"n": 1})

	_, evalErr := builder.EvalBool("inputs.missing == 1")
	_, compileErr := builder.EvalBool("inputs.n ==")
	for name, tc := range map[string]struct {
		err    error
		prefix string
	}{
		"missing key": {evalErr, "CEL evaluation error: "},
		"syntax":      {compileErr, "CEL compilation error: "},
	} {
		require.Error(t, tc.err, name)
		assert.True(t, strings.HasPrefix(tc.err.Error(), tc.prefix), "%s: text unchanged, got %q", name, tc.err)
		wrapped := fmt.Errorf("failed to resolve workflow templates: key 'concurrency': %w", tc.err)
		assert.Equal(t, svcerr.ClassUser, errclass.Classify(wrapped), name)
	}
}
