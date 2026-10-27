package webhook

import (
	"regexp"
	"strings"
)

var observedSHA = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)

// observedPRHead is called only on GitHub's verified, projected envelope.
// Delivery order says nothing about commit order. Private PR workflows must
// not enable latest supersession until an authoritative head guard is wired.
func observedPRHead(event string, data map[string]any) string {
	if !strings.HasPrefix(event, "pull_request.") {
		return ""
	}
	pr, ok := data["pull_request"].(map[string]any)
	if !ok {
		return ""
	}
	head, ok := pr["head"].(map[string]any)
	if !ok {
		return ""
	}
	sha, _ := head["sha"].(string)
	if !observedSHA.MatchString(sha) {
		return ""
	}
	return strings.ToLower(sha)
}
