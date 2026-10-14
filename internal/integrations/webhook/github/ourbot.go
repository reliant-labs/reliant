package github

import (
	"os"
	"strconv"
	"strings"
)

const (
	// BotUserIDEnv names the numeric GitHub user id of the App's bot account
	// (`<slug>[bot]`). The default is the production App's; a deployment with
	// its own App sets this (and BotLoginEnv) to that App's bot.
	BotUserIDEnv = "RELIANT_GITHUB_BOT_USER_ID"
	// BotLoginEnv names the bot's login, used only when no id is configured.
	BotLoginEnv = "RELIANT_GITHUB_BOT_LOGIN"

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

// BotFromEnv reads the configured bot, defaulting to the production App's.
func BotFromEnv(getenv func(string) string) OurBot {
	if getenv == nil {
		getenv = os.Getenv
	}
	bot := OurBot{ID: defaultBotUserID, Login: defaultBotLogin}
	if v := strings.TrimSpace(getenv(BotUserIDEnv)); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			bot = OurBot{ID: id}
		}
	}
	if v := strings.TrimSpace(getenv(BotLoginEnv)); v != "" && getenv(BotUserIDEnv) == "" {
		bot = OurBot{Login: v}
	}
	return bot
}

// ourBot is the process's bot, read once. Tests that need another use SetBot.
var ourBot = BotFromEnv(os.Getenv)

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
