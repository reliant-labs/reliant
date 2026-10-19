package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsOurBot_DefaultsToTheProductionApp(t *testing.T) {
	assert.True(t, IsOurBot("Bot", 186876276, "reliant-labs[bot]"))
}

// The id is the identity: a login can be renamed and re-registered, so a Bot
// that merely CALLS itself reliant-labs[bot] is not ours.
func TestIsOurBot_IdentifiesByNumericIDNotLogin(t *testing.T) {
	assert.False(t, IsOurBot("Bot", 1, "reliant-labs[bot]"), "right login, wrong id")
	assert.True(t, IsOurBot("Bot", 186876276, "renamed[bot]"), "right id, other login")
}

func TestIsOurBot_OnlyBotAccounts(t *testing.T) {
	assert.False(t, IsOurBot("User", 186876276, "reliant-labs[bot]"), "a human with the bot's id is not the bot")
	assert.False(t, IsOurBot("Bot", 49699333, "dependabot[bot]"))
	assert.False(t, IsOurBot("", 186876276, ""))
}

func TestBotFrom(t *testing.T) {
	assert.Equal(t, OurBot{ID: 186876276, Login: "reliant-labs[bot]"}, BotFrom(0, ""))
	assert.Equal(t, OurBot{ID: 42}, BotFrom(42, ""), "a configured id wins and drops the default login")
	assert.Equal(t, OurBot{Login: "acme-app[bot]"}, BotFrom(0, "acme-app[bot]"), "a login alone replaces the default id")
	assert.Equal(t, OurBot{ID: 42}, BotFrom(42, "x[bot]"), "id beats login")
}

func TestIsOurBot_ConfiguredLoginOnly(t *testing.T) {
	defer SetBot(OurBot{Login: "acme-app[bot]"})()
	assert.True(t, IsOurBot("Bot", 7, "Acme-App[bot]"), "logins compare case-insensitively")
	assert.False(t, IsOurBot("Bot", 186876276, "reliant-labs[bot]"))
}
