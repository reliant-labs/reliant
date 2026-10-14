package wfcel

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every expression in the "Logic a script usually carries" table of
// docs/reference/cel-expressions.mdx must compile and evaluate: a doc whose
// examples fail teaches the reader that the language does not work.
func TestDocumentedHelperExpressionsEvaluate(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/reference/cel-expressions.mdx")
	require.NoError(t, err)
	doc := string(raw)
	start := strings.Index(doc, "### Logic a script usually carries")
	end := strings.Index(doc[start:], "(In a markdown table")
	require.True(t, start >= 0 && end > 0, "section not found")
	section := doc[start : start+end]

	row := regexp.MustCompile("(?m)^\\| ([^|]+) \\| `(.+)` \\|$")
	matches := row.FindAllStringSubmatch(section, -1)
	require.GreaterOrEqual(t, len(matches), 15, "the table lost rows")

	ctx := helperCtx()
	ctx.Inputs["service"] = "Reliant-API-Server"
	ctx.Inputs["expires"] = "2099-01-01T00:00:00Z"
	ctx.Nodes = map[string]interface{}{"start": map[string]interface{}{"stdout": `{"status":"ready"}`}}
	for _, m := range matches {
		need, expr := strings.TrimSpace(m[1]), strings.ReplaceAll(m[2], `\|`, "|")
		t.Run(need, func(t *testing.T) {
			_, err := EvaluateValue(expr, ctx)
			assert.NoError(t, err, expr)
		})
	}
}
