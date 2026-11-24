package lint

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObserveInvalid_NamesTheNodeAndThePlace(t *testing.T) {
	t.Parallel()
	got := findingsOf(Check(loadFixture(t, filepath.Join("testdata", RuleObserveInvalid, "bad.yaml"))), RuleObserveInvalid)
	require.Len(t, got, 2, "%v", got)
	byNode := map[string]Finding{}
	for _, f := range got {
		byNode[f.Node] = f
	}
	assert.True(t, strings.HasPrefix(byNode["plan"].Message, "events.[0].attributes.reason: "), byNode["plan"].Message)
	assert.Contains(t, byNode["plan"].Message, "reason")
	assert.True(t, strings.HasPrefix(byNode[""].Message, "attributes.first: "), "the workflow's defaults are reported on the workflow: %s", byNode[""].Message)
	assert.Contains(t, byNode[""].Message, "`nodes`")
}

func TestObserveFindingLocation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		path        []string
		node, where string
	}{
		{[]string{"wf", "nodes", "[1](plan)", "observe", "events", "[0]", "when"}, "plan", "events.[0].when"},
		{[]string{"nodes", "plan", "observe"}, "plan", ""},
		{[]string{"wf", "observe", "attributes", "x"}, "", "attributes.x"},
		{[]string{"nodes", "loop", "inline", "nodes", "body", "observe", "metrics", "[0]", "kind"}, "loop.body", "metrics.[0].kind"},
		{[]string{"wf", "on_failure", "nodes", "[0](notify)", "observe", "attributes", "a"}, "on_failure.notify", "attributes.a"},
		{[]string{"on_failure", "observe", "attributes", "a"}, "", "on_failure.observe.attributes.a"},
	} {
		node, where := observeFindingLocation(tc.path)
		assert.Equal(t, tc.node, node, "%v", tc.path)
		assert.Equal(t, tc.where, where, "%v", tc.path)
	}
}
