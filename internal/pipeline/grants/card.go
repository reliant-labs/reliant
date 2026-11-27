// Copyright (c) 2025 Reliant Labs
package grants

import (
	"strings"
	"unicode/utf8"
)

// The capability-grant card is one string, the approval's title, laid out as
// every approval card is so the app renders it with structure and a person
// can still read it as plain text (frontends/web/src/lib/approvalCard.ts):
//
//	<one plain sentence: what approving it lets the activation do>
//	<Label>: <value>
//	...
//
//	Technical details
//	<one technical line per row>
//
// Every value is flattened to one line, so no value can forge a row or the
// details heading. A card with technical details reads as one that grants
// standing power, so it is never approved in an "Approve all" unread.
const (
	// cardDetailsHeading introduces the technical lines. The web client
	// splits on "\n\n" + cardDetailsHeading + "\n"; keep the two in step.
	cardDetailsHeading = "Technical details"
	// cardValueLimit bounds one displayed value.
	cardValueLimit = 240
	// cardOperation is the card's technical operation line.
	cardOperation = "pipeline_capability_grant"
)

// grantCard is everything the card shows.
type grantCard struct {
	Activation, TriggerID string
	Pool, PoolID          string
	// Repository is the enrolled repository's owner/name, "" for every one.
	Repository, RepoID string
	Capabilities       []Capability
	Reason             string
	RunID              string
}

// capabilityWords is what each capability lets the activation do, for the
// card's lead and rows.
var capabilityWords = map[Capability]string{
	Approve: "approve pull requests (its review verdicts count)",
	Land:    "land approved pull requests through the merge queue",
}

func (c grantCard) title() string {
	var what []string
	for _, k := range c.Capabilities {
		what = append(what, capabilityWords[k])
	}
	scope := "every enrolled repository"
	if c.Repository != "" {
		scope = c.Repository
	}
	lines := []string{
		flatten("Let the automation " + quote(c.Activation) + " " + strings.Join(what, " and ") +
			", while it runs on machine pool " + quote(c.Pool) + "."),
		"Automation: " + flatten(c.Activation),
		"Machine pool: " + flatten(c.Pool),
		"Repository: " + flatten(scope),
		"Grants: " + flatten(strings.Join(what, "; ")),
		"Revoke: any time, from the automation's grants; revoking needs no approval",
	}
	if r := flatten(c.Reason); r != "" {
		lines = append(lines, "Reason: "+r)
	}
	details := []string{
		"Operation: " + cardOperation,
		"Trigger: " + flatten(c.TriggerID),
		"Machine pool id: " + flatten(c.PoolID),
		"Capabilities: " + flatten(joinCapsComma(c.Capabilities)),
	}
	if c.RepoID != "" {
		details = append(details, "Repository id: "+flatten(c.RepoID))
	}
	if c.RunID != "" {
		details = append(details, "Requested by run: "+flatten(c.RunID))
	}
	return strings.Join(lines, "\n") + "\n\n" + cardDetailsHeading + "\n" + strings.Join(details, "\n")
}

func quote(s string) string { return "\u201c" + flatten(s) + "\u201d" }

// flatten makes s one bounded line.
func flatten(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > cardValueLimit {
		r := []rune(s)
		s = string(r[:cardValueLimit-1]) + "\u2026"
	}
	return s
}

func joinCapsComma(caps []Capability) string {
	names := make([]string, 0, len(caps))
	for _, c := range caps {
		names = append(names, string(c))
	}
	return strings.Join(names, ", ")
}
