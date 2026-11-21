package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// Bug report a21fcc49: `token create --name ci` stores reliant-cli@ci, and
// `token revoke ci` said there was no such token. Revoke takes the name create
// took, and an id or an exact stored name still wins.
func TestTokensToRevoke(t *testing.T) {
	tok := func(id, name string) *reliantv1.TokenInfo { return &reliantv1.TokenInfo{Id: id, Name: name} }
	ci := tok("t1", "reliant-cli@ci")
	exact := tok("t2", "deploy")
	cliDeploy := tok("t3", "reliant-cli@deploy")
	other := tok("t4", "reliant-cli@other")
	tokens := []*reliantv1.TokenInfo{ci, exact, cliDeploy, other}

	ids := func(ts []*reliantv1.TokenInfo) []string {
		var out []string
		for _, t := range ts {
			out = append(out, t.GetId())
		}
		return out
	}
	assert.Equal(t, []string{"t1"}, ids(tokensToRevoke(tokens, "ci")), "the --name given to create")
	assert.Equal(t, []string{"t1"}, ids(tokensToRevoke(tokens, "reliant-cli@ci")), "the stored name")
	assert.Equal(t, []string{"t1"}, ids(tokensToRevoke(tokens, "t1")), "the id")
	assert.Equal(t, []string{"t2"}, ids(tokensToRevoke(tokens, "deploy")), "an exact stored name wins over the create name")
	assert.Empty(t, tokensToRevoke(tokens, "missing"))
	assert.Empty(t, tokensToRevoke(tokens, "reliant-cli@missing"), "a full name is never prefixed again")

	twice := []*reliantv1.TokenInfo{tok("a", "reliant-cli@ci"), tok("b", "reliant-cli@ci")}
	assert.Len(t, tokensToRevoke(twice, "ci"), 2, "two matches are reported, not guessed between")
}
