package wfyaml

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/require"
)

func TestParseHostedUseActionReference(t *testing.T) {
	wf, err := ParseWorkflow([]byte(`
name: hosted-call
nodes:
  - id: fixed
    type: action
    uses: http/request@1
    hosted_use: grant-test
    with:
      url: https://example.com/rpc
      method: POST
      body: { proposal: true }
`))
	require.NoError(t, err)
	args := findNode(t, wf.GetNodes(), "fixed").GetAction()
	require.NotNil(t, args)
	require.Equal(t, "grant-test", model.CelStringRaw(args.GetHostedUse()))
	require.False(t, model.CelStringIsSet(args.GetConnection()))
}
