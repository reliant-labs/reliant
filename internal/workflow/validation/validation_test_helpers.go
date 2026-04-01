package validation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func assertErrorPath(t *testing.T, expectedSuffix []string, actualPath []string) {
	// The validation framework prepends the workflow name and node ID to the path.
	// We only care about the suffix of the path for these tests.
	if len(actualPath) < len(expectedSuffix) {
		assert.Fail(t, "actual path is shorter than expected suffix", "expected suffix: %v, actual path: %v", expectedSuffix, actualPath)
		return
	}
	actualSuffix := actualPath[len(actualPath)-len(expectedSuffix):]

	// Normalize the actual suffix to remove node IDs like (step1) from "[0](step1)"
	for i, part := range actualSuffix {
		if strings.Contains(part, "(") {
			actualSuffix[i] = part[:strings.Index(part, "(")]
		}
	}

	assert.Equal(t, expectedSuffix, actualSuffix)
}

func pathToString(path []string) string {
	if len(path) == 0 {
		return "(root)"
	}
	return strings.Join(path, ".")
}
