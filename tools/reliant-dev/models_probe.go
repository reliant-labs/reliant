// Copyright (c) 2025 Reliant Labs
package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ============================================================================
// models probe — matrix construction and failure classification (pure; the
// network-facing runner lives in models_probe_run.go).
// ============================================================================

const (
	cellBasic = "basic" // PONG at a thinking level
	cellTemp  = "temp"  // PONG at an explicit temperature
	cellTool  = "tool"  // tool round-trip with history replay
	cellTag   = "tag"   // tag resolution through the production resolver
	cellImage = "image" // image generation

	// cellReasoning is a puzzle that needs real reasoning, run at default and at the
	// model's lowest and highest declared thinking level.
	cellReasoning = "reasoning"
)

// Failure classes, in the vocabulary of research/MODEL_PROBE_RESULTS.md.
const (
	classCredential          = "credential"
	classCatalogMapping      = "catalog-mapping"
	classDriverBug           = "driver-bug"
	classProviderSide        = "provider-side"
	classExpectedUnsupported = "expected-unsupported"
	classUnclassified        = "unclassified"
)

// probeCoreTags are the tags every probe run resolves.
var probeCoreTags = []string{"powerful", "flagship", "moderate", "fast", "cheap", "reasoning", "meta"}

// probeTemperatures are the explicit temperatures exercised per model.
var probeTemperatures = []float64{0, 0.7}

// probeModel is the slice of a catalog definition the matrix needs.
type probeModel struct {
	ID       string
	Drivers  []string // distinct provider drivers mapped in the catalog
	Levels   []string // thinking levels the model supports (empty: cannot reason)
	TempOmit bool     // models.yaml temperature_mode: omit — temperature is never sent
}

// probeCell is one request (or request sequence) in the matrix.
type probeCell struct {
	Kind     string   `json:"kind"`
	Model    string   `json:"model,omitempty"`
	Provider string   `json:"provider,omitempty"`
	Tag      string   `json:"tag,omitempty"`
	Level    string   `json:"level"` // "" = no explicit level (default)
	Temp     *float64 `json:"temperature,omitempty"`
}

// Label is the human-readable cell identity used in the table and filters.
func (c probeCell) Label() string {
	var id string
	switch c.Kind {
	case cellTag:
		id = "tag:" + c.Tag
	default:
		id = c.Model + "@" + c.Provider
	}
	lvl := c.Level
	if lvl == "" {
		lvl = "default"
	}
	switch c.Kind {
	case cellTemp:
		return fmt.Sprintf("%s temp=%g", id, *c.Temp)
	case cellTool:
		return id + " tool-roundtrip"
	case cellImage:
		return id + " image"
	case cellLocalBasic, cellLocalTool, cellLocalThinking, cellLocalLong, cellLocalOver:
		return id + " " + c.Kind
	case cellLocalTemp:
		return fmt.Sprintf("%s %s temp=%g", id, c.Kind, *c.Temp)
	case cellReasoning:
		return id + " reasoning level=" + lvl
	case cellTag:
		return id + " (tag level)"
	}
	return id + " level=" + lvl
}

// buildProbeMatrix expands models x credentialed providers into cells.
// providers is the set of provider drivers a credential exists for. A filter
// matches against "model@provider".
func buildProbeMatrix(models []probeModel, providers map[string]bool, filter *regexp.Regexp) []probeCell {
	var cells []probeCell
	sorted := append([]probeModel(nil), models...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	for _, m := range sorted {
		drivers := append([]string(nil), m.Drivers...)
		sort.Strings(drivers)
		for _, driver := range drivers {
			if !providers[driver] {
				continue
			}
			if filter != nil && !filter.MatchString(m.ID+"@"+driver) {
				continue
			}
			base := probeCell{Model: m.ID, Provider: driver}

			c := base
			c.Kind = cellBasic
			cells = append(cells, c)
			for _, lvl := range m.Levels {
				c := base
				c.Kind, c.Level = cellBasic, lvl
				cells = append(cells, c)
			}
			if !m.TempOmit {
				for i := range probeTemperatures {
					t := probeTemperatures[i]
					c := base
					c.Kind, c.Temp = cellTemp, &t
					cells = append(cells, c)
				}
			}
			c = base
			c.Kind = cellTool
			cells = append(cells, c)
		}
	}
	return cells
}

// buildTagCells returns one cell per tag. A tag cell resolves through the
// production resolver (registry + the user's providers) rather than being
// pre-resolved here, so the probe exercises exactly what a workflow would.
func buildTagCells(tags []string, filter *regexp.Regexp) []probeCell {
	var cells []probeCell
	for _, tag := range tags {
		if filter != nil && !filter.MatchString("tag:"+tag) {
			continue
		}
		cells = append(cells, probeCell{Kind: cellTag, Tag: tag})
	}
	return cells
}

var (
	// reModelInvalid names a model the provider does not serve; it outranks every
	// other rule because a bad model id also reads as a 400/404.
	reModelInvalid = regexp.MustCompile(`(?i)not a valid model|no endpoints found|model not found|model_not_found|unknown model|no such model|does not exist`)
	reCredential   = regexp.MustCompile(`(?i)\b(401|403)\b|unauthori[sz]ed|invalid[ _-]?(api[ _-]?)?key|api key|authentication|permission denied|no api keys|credential|forbidden|invalid x-api-key|token (has )?expired|invalid_grant|not configured`)
	reCatalog      = regexp.MustCompile(`(?i)\b404\b|model not found|not_found|does not exist|unknown model|invalid model|model .* not (found|available|supported)|no such model|not a valid model|no endpoints found|failed to resolve model|no available provider|not served|is not supported on|unsupported model|decommissioned|model_not_found|no models found`)
	reProvider     = regexp.MustCompile(`(?i)\b(429|500|502|503|504|529)\b|overloaded|rate.?limit|quota|capacity|temporarily|deadline exceeded|timed? ?out|timeout|context canceled|connection reset|unexpected eof|service unavailable|resource_exhausted|insufficient[_ ](funds|credit)|billing`)
	reUnsupported  = regexp.MustCompile(`(?i)driver .* not (registered|supported)|no driver|unsupported driver|no .* driver registered|not implemented|xai`)
	reBadRequest   = regexp.MustCompile(`(?i)\b400\b|invalid_request|bad request|invalid argument|unsupported parameter|unsupported value|temperature|thinking|reasoning|thought.?signature|signature|budget_tokens|effort|schema|tool_use|tool_result|function_call|must be|is not valid`)
)

// classifyProbeFailure assigns a failure class from an error string and the
// provider the cell targeted. Order matters: a more specific cause wins.
func classifyProbeFailure(errText, provider string) string {
	if strings.TrimSpace(errText) == "" {
		return classDriverBug
	}
	switch {
	case reModelInvalid.MatchString(errText):
		return classCatalogMapping
	case provider == "xai" && (reUnsupported.MatchString(errText) || reCatalog.MatchString(errText) || strings.Contains(strings.ToLower(errText), "driver")):
		return classExpectedUnsupported
	case reCatalog.MatchString(errText) && !reBadRequest.MatchString(strings.ReplaceAll(strings.ToLower(errText), "not found", "")):
		return classCatalogMapping
	case reBadRequest.MatchString(errText) && !reProvider.MatchString(errText) && !reCredential.MatchString(errText):
		return classDriverBug
	case reCredential.MatchString(errText):
		return classCredential
	case reProvider.MatchString(errText):
		return classProviderSide
	case reCatalog.MatchString(errText):
		return classCatalogMapping
	case reUnsupported.MatchString(errText):
		return classExpectedUnsupported
	}
	return classUnclassified
}

// assertPong reports whether the text satisfies the PONG assertion.
func assertPong(text string) bool {
	return strings.Contains(strings.ToUpper(text), "PONG")
}

// assertSecretWord reports whether a tool round-trip's final answer carries
// the tool's result.
func assertSecretWord(text string) bool {
	return strings.Contains(strings.ToLower(text), "pineapple")
}

// probeTally counts outcomes for the summary line.
type probeTally struct {
	Total, Passed, Failed int
	ByClass               map[string]int
}

func tallyOutcomes(outcomes []probeOutcome) probeTally {
	t := probeTally{ByClass: map[string]int{}}
	for _, o := range outcomes {
		t.Total++
		if o.Status == "pass" {
			t.Passed++
			continue
		}
		t.Failed++
		t.ByClass[o.Class]++
	}
	return t
}

// reasoningPrompt needs real multi-step arithmetic but has a short checkable
// answer. The reply is constrained to the bare number so visible output stays
// tiny and any large output-token count is hidden reasoning.
const reasoningPrompt = "Compute the sum of all integers from 1 to 1000 inclusive that are divisible by exactly one of 7 or 11 (divisible by 7 or by 11, but not by both). Reply with only the final number, nothing else."

const reasoningAnswer = "104104"

// assertReasoningAnswer reports whether the reply carries the known answer.
func assertReasoningAnswer(text string) bool {
	cleaned := strings.NewReplacer(",", "", " ", "", "_", "", "\u202f", "").Replace(text)
	return strings.Contains(cleaned, reasoningAnswer)
}

// buildReasoningMatrix expands reasoning-capable models x credentialed
// providers into default / lowest / highest level cells. Models that cannot
// reason (no declared levels) are skipped: a level has nothing to change there.
func buildReasoningMatrix(models []probeModel, providers map[string]bool, filter *regexp.Regexp) []probeCell {
	var cells []probeCell
	sorted := append([]probeModel(nil), models...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, m := range sorted {
		if len(m.Levels) == 0 {
			continue
		}
		low, high := m.Levels[0], m.Levels[len(m.Levels)-1]
		drivers := append([]string(nil), m.Drivers...)
		sort.Strings(drivers)
		for _, driver := range drivers {
			if !providers[driver] {
				continue
			}
			if filter != nil && !filter.MatchString(m.ID+"@"+driver) {
				continue
			}
			levels := []string{"", low}
			if high != low {
				levels = append(levels, high)
			}
			for _, lvl := range levels {
				cells = append(cells, probeCell{Kind: cellReasoning, Model: m.ID, Provider: driver, Level: lvl})
			}
		}
	}
	return cells
}
