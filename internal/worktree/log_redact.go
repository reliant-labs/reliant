package worktree

import "regexp"

// urlUserinfo matches the userinfo of a URL (scheme://user:token@), which git
// echoes back in push/fetch output when the remote embeds a credential.
var urlUserinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/@\s]+@`)

// maxGitOutputForLog bounds git's diagnostic output in a log record.
const maxGitOutputForLog = 2048

// gitOutputForLog returns git's combined output for a failure log: credentials
// in URLs are removed and the text is bounded.
func gitOutputForLog(output []byte) string {
	if len(output) > maxGitOutputForLog {
		output = output[:maxGitOutputForLog]
	}
	return urlUserinfo.ReplaceAllString(string(output), "$1")
}
