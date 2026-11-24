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
	require.Len(t, got, 3, "%v", got)
	defaults := map[string]Finding{}
	var own Finding
	for _, f := range got {
		if strings.HasPrefix(f.Message, "observe_defaults.") {
			defaults[f.Node] = f
		} else {
			own = f
		}
	}
	assert.Equal(t, "plan", own.Node)
	assert.True(t, strings.HasPrefix(own.Message, "events.[0].attributes.reason: "), own.Message)
	assert.Contains(t, own.Message, "reason")
	for _, node := range []string{"plan", "report"} {
		assert.True(t, strings.HasPrefix(defaults[node].Message, "observe_defaults.attributes.first: "), defaults[node].Message)
		assert.Contains(t, defaults[node].Message, "`nodes`")
	}
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
