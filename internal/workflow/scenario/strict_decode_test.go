package scenario

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A scenario file with a key the schema does not have must fail to load. The
// decode used to drop such keys, so an expectation written with the wrong key
// (`contains:` for `content_contains:`, `message:` for `messages:`) loaded as
// an empty assertion and passed whatever the run did.
func TestScenarioParsingRejectsUnknownFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"unknown key inside an expect.messages entry",
			"name: a\nevents: []\nexpect:\n  messages:\n    save_result:\n      contains: hello\n", "contains"},
		{"singular message under expect",
			"name: a\nevents: []\nexpect:\n  message:\n    save_result:\n      saved: true\n", "message"},
		{"unknown key under expect",
			"name: a\nevents: []\nexpect:\n  reachd: [x]\n", "reachd"},
		{"unknown key on an event",
			"name: a\nevents:\n  - node: x\n    txt: hi\n", "txt"},
		{"unknown top-level key",
			"name: a\nevents: []\nexpects:\n  outcome: completed\n", "expects"},
		{"unknown key inside a wrapper-format scenario",
			"apiVersion: '1.0'\nscenarios:\n  - name: a\n    events: []\n    expect:\n      messages:\n        x:\n          cnt: 1\n", "cnt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseScenarioYAML([]byte(tc.yaml))
			require.Error(t, err, "ParseScenarioYAML")
			assert.Contains(t, err.Error(), tc.wantErr)

			if strings.HasPrefix(tc.yaml, "apiVersion") {
				return // a list of scenarios is not a project scenario file
			}
			_, err = ParseScenarioFile([]byte(tc.yaml), "a")
			require.Error(t, err, "ParseScenarioFile")
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestScenarioParsingAcceptsKnownFields(t *testing.T) {
	t.Parallel()
	const doc = "name: a\nevents: []\nexpect:\n  outcome: completed\n  messages:\n    save_result:\n      saved: true\n      count: 1\n      role: assistant\n      content_contains: [Done]\n      content_not_contains: [oops]\n"
	scs, err := ParseScenarioYAML([]byte(doc))
	require.NoError(t, err)
	require.Len(t, scs, 1)
	assert.Equal(t, []string{"Done"}, scs[0].Expect.Messages["save_result"].ContentContains)

	sc, err := ParseScenarioFile([]byte(doc), "a")
	require.NoError(t, err)
	assert.Equal(t, "a", sc.Name)
}
