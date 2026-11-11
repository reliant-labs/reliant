package wfyaml

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseExcludeSkillCatalog(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		want        bool
		invalid     bool
	}{
		{name: "omitted defaults false"},
		{name: "explicit false", field: "exclude_skill_catalog: false"},
		{name: "explicit true", field: "exclude_skill_catalog: true", want: true},
		{name: "wrong type", field: "exclude_skill_catalog: [true]", invalid: true},
		{name: "not a boolean", field: "exclude_skill_catalog: nope", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := fmt.Sprintf(`name: isolated-coach
entry: [coach]
nodes:
  - id: coach
    type: call_llm
    args:
      model: {tags: [flagship]}
      %s
`, tc.field)
			wf, err := ParseWorkflow([]byte(source))
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, wf.GetNodes()[0].GetCallLlm().GetExcludeSkillCatalog())
			encoded, err := MarshalWorkflow(wf)
			require.NoError(t, err)
			roundtrip, err := ParseWorkflow(encoded)
			require.NoError(t, err)
			require.Equal(t, tc.want, roundtrip.GetNodes()[0].GetCallLlm().GetExcludeSkillCatalog())
		})
	}
}
