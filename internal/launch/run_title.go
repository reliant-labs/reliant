// Copyright (c) 2025 Reliant Labs
package launch

import (
	"strings"
	"time"
)

// runTitleSubjectMax bounds the subject half of a RunTitle, in runes: long
// enough to say what the run is about, short enough for a sidebar or Runs row.
const runTitleSubjectMax = 60

// RunTitle is the title of a run something other than a person in a chat
// started — an agent's start_run, `reliant workflow run` — when the starter
// gave none: "<workflow> · <subject>", the same shape a trigger's runs carry
// ("<trigger> · <time>").
//
// Such a run's first message is usually an instruction to the workflow ("Run
// the cicd-probe workflow with the provided inputs."), so a title generated
// from it reads "Run CICD Probe Workflow" nine times over. The workflow's name
// says what kind of run it is; the subject is the first line of the message,
// cut to a row's width, or the start time when there is no message to take
// one from.
func RunTitle(workflow, subject string, at time.Time) string {
	name := workflow
	if i := strings.Index(name, "://"); i >= 0 {
		name = name[i+len("://"):]
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "agent"
	}
	if s := titleSubject(subject); s != "" {
		return name + " · " + s
	}
	return name + " · " + at.UTC().Format("2006-01-02 15:04 MST")
}

// titleSubject is the first non-blank line of text with its whitespace
// collapsed, cut to runTitleSubjectMax runes with an ellipsis.
func titleSubject(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		if runes := []rune(line); len(runes) > runTitleSubjectMax {
			return strings.TrimSpace(string(runes[:runTitleSubjectMax-1])) + "…"
		}
		return line
	}
	return ""
}
