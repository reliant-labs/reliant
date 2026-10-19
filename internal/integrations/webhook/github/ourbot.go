package github

import (
	"strings"
)

const (
	// defaultBotUserID is `reliant-labs[bot]`, the production App's bot
	// (GET /users/reliant-labs%5Bbot%5D). Assigned once by GitHub and never
	// reused, unlike a login.
	defaultBotUserID int64 = 186876276
	// defaultBotLogin is that bot's login.
	defaultBotLogin = "reliant-labs[bot]"
)

// OurBot is who the App's bot is, as GitHub names it in a webhook's sender.
type OurBot struct {
	// ID is the bot account's numeric user id; non-zero means identify by it.
	ID int64
	// Login is the bot's login, compared only when ID is zero.
	Login string
}

// BotFrom is the configured bot (RELIANT_GITHUB_BOT_USER_ID,
// RELIANT_GITHUB_BOT_LOGIN): the user id when one is set, else the login, else
// the production App's bot. A deployment with its own App names that App's.
func BotFrom(userID int64, login string) OurBot {
	if userID > 0 {
		return OurBot{ID: userID}
	}
	if login = strings.TrimSpace(login); login != "" {
		return OurBot{Login: login}
	}
	return OurBot{ID: defaultBotUserID, Login: defaultBotLogin}
}

// ourBot is the process's bot: the production App's until the process
// configures its own (ConfigureBot).
var ourBot = BotFrom(0, "")

// ConfigureBot sets the process's bot. A server role calls it once at boot,
// from its config, before any webhook is parsed.
func ConfigureBot(b OurBot) { ourBot = b }

// SetBot replaces the process's bot and returns a restore func, for tests.
func SetBot(b OurBot) (restore func()) {
	prev := ourBot
	ourBot = b
	return func() { ourBot = prev }
}

// IsOurBot reports whether a webhook sender is our App's bot: a Bot account
// whose numeric id is ours (or, with no id configured, whose login is).
func IsOurBot(senderType string, id int64, login string) bool {
	if senderType != "Bot" {
		return false
	}
	if ourBot.ID > 0 {
		return id == ourBot.ID
	}
	return ourBot.Login != "" && strings.EqualFold(login, ourBot.Login)
}
