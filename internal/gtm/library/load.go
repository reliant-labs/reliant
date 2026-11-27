// Copyright (c) 2026 Reliant Labs

package library

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Limits on an entry. They keep the bank readable as seed text for an agent
// and keep every field inside what the GTM API carries.
const (
	maxNameLen     = 120
	maxTextLen     = 4000
	maxQuestionLen = 300
	minTailor      = 2
	maxTailor      = 5
	maxKPIs        = 8
	maxKPILen      = 120
	maxSignalDays  = 365
)

var (
	keyPattern         = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)
	channelTypePattern = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
)

// ideaFile is the shape of one ideas/<category>.yaml file.
type ideaFile struct {
	Entries []Entry `yaml:"entries"`
}

// load reads every *.yaml file in dir of fsys, in name order, and validates
// the result as a whole bank. Each file is named for its category and holds
// only that category's entries. The result is ordered by category
// (Categories order), then by position in the file.
func load(fsys fs.FS, dir string) ([]Entry, error) {
	names, err := fs.Glob(fsys, path.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, errors.New("no idea files")
	}
	byCategory := make(map[string][]Entry)
	for _, name := range names {
		category := strings.TrimSuffix(path.Base(name), ".yaml")
		if err := checkOne("file category", category, categories); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		raw, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		var f ideaFile
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		// One document per file: a second one (after "---") would otherwise
		// be silently ignored.
		var extra yaml.Node
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			return nil, fmt.Errorf("%s: more than one YAML document", name)
		}
		if len(f.Entries) == 0 {
			return nil, fmt.Errorf("%s: no entries", name)
		}
		for i, e := range f.Entries {
			if e.Category != category {
				return nil, fmt.Errorf("%s: entry %d (%q): category %q is not the file's category %q", name, i, e.Key, e.Category, category)
			}
		}
		byCategory[category] = append(byCategory[category], f.Entries...)
	}
	var out []Entry
	for _, c := range categories {
		out = append(out, byCategory[c]...)
	}
	if err := validateBank(out); err != nil {
		return nil, err
	}
	return out, nil
}

// validateBank checks every entry and that keys are unique.
func validateBank(es []Entry) error {
	seen := make(map[string]bool, len(es))
	for i, e := range es {
		if err := validateEntry(e); err != nil {
			return fmt.Errorf("entry %d (%q): %w", i, e.Key, err)
		}
		if seen[e.Key] {
			return fmt.Errorf("entry %d: duplicate key %q", i, e.Key)
		}
		seen[e.Key] = true
	}
	return nil
}

// validateEntry checks one entry's fields against the schema and the
// vocabularies.
func validateEntry(e Entry) error {
	if !keyPattern.MatchString(e.Key) {
		return fmt.Errorf("key %q does not match %s", e.Key, keyPattern)
	}
	if err := checkOne("category", e.Category, categories); err != nil {
		return err
	}
	if err := checkText("name", e.Name, maxNameLen); err != nil {
		return err
	}
	if strings.ContainsAny(e.Name, "\n") {
		return errors.New("name: must be one line")
	}
	if err := checkText("summary", e.Summary, maxTextLen); err != nil {
		return err
	}
	if err := checkText("why_it_works", e.WhyItWorks, maxTextLen); err != nil {
		return err
	}
	if err := checkList("fits.business_types", e.Fits.BusinessTypes, businessTypes); err != nil {
		return err
	}
	if err := checkList("fits.business_models", e.Fits.BusinessModels, businessModels); err != nil {
		return err
	}
	if err := checkList("stages", e.Stages, stages); err != nil {
		return err
	}
	if err := checkOne("objective", e.Objective, objectives); err != nil {
		return err
	}
	if err := checkOne("effort", e.Effort, efforts); err != nil {
		return err
	}
	if err := checkOne("cost", e.Cost, costs); err != nil {
		return err
	}
	if e.SignalDays < 1 || e.SignalDays > maxSignalDays {
		return fmt.Errorf("signal_days %d: want 1..%d", e.SignalDays, maxSignalDays)
	}
	if n := len(e.TailorQuestions); n < minTailor || n > maxTailor {
		return fmt.Errorf("tailor_questions: %d, want %d..%d", n, minTailor, maxTailor)
	}
	for i, q := range e.TailorQuestions {
		if err := checkText(fmt.Sprintf("tailor_questions[%d]", i), q, maxQuestionLen); err != nil {
			return err
		}
		if !strings.HasSuffix(q, "?") {
			return fmt.Errorf("tailor_questions[%d]: %q is not a question", i, q)
		}
	}
	if dup := firstDuplicate(e.TailorQuestions); dup != "" {
		return fmt.Errorf("tailor_questions: duplicate %q", dup)
	}
	if e.SuggestedWorkflow != "" {
		if err := checkText("suggested_workflow", e.SuggestedWorkflow, maxTextLen); err != nil {
			return err
		}
	}
	for _, c := range e.ChannelTypes {
		if !channelTypePattern.MatchString(c) {
			return fmt.Errorf("channel_types: %q does not match %s", c, channelTypePattern)
		}
	}
	if err := checkList("channel_types", e.ChannelTypes, channelTypes); err != nil {
		return err
	}
	if len(e.KPIs) > maxKPIs {
		return fmt.Errorf("kpis: %d, want at most %d", len(e.KPIs), maxKPIs)
	}
	for i, k := range e.KPIs {
		if err := checkText(fmt.Sprintf("kpis[%d]", i), k, maxKPILen); err != nil {
			return err
		}
	}
	if dup := firstDuplicate(e.KPIs); dup != "" {
		return fmt.Errorf("kpis: duplicate %q", dup)
	}
	return nil
}

// checkText: non-empty, no surrounding whitespace, valid UTF-8, at most max
// runes.
func checkText(field, v string, maxRunes int) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("%s: empty", field)
	}
	if strings.TrimSpace(v) != v {
		return fmt.Errorf("%s: leading or trailing whitespace", field)
	}
	if !utf8.ValidString(v) {
		return fmt.Errorf("%s: invalid UTF-8", field)
	}
	if n := utf8.RuneCountInString(v); n > maxRunes {
		return fmt.Errorf("%s: %d characters, want at most %d", field, n, maxRunes)
	}
	return nil
}

// checkOne: v is in vocab.
func checkOne(field, v string, vocab []string) error {
	if !slices.Contains(vocab, v) {
		return fmt.Errorf("%s: %q is not one of %s", field, v, strings.Join(vocab, ", "))
	}
	return nil
}

// checkList: every value is in vocab, once (so a list is never longer than
// its vocabulary).
func checkList(field string, vs, vocab []string) error {
	for _, v := range vs {
		if err := checkOne(field, v, vocab); err != nil {
			return err
		}
	}
	if dup := firstDuplicate(vs); dup != "" {
		return fmt.Errorf("%s: duplicate %q", field, dup)
	}
	return nil
}

// dedupe returns vs without repeated values, in first-seen order.
func dedupe(vs []string) []string {
	var out []string
	for _, v := range vs {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func firstDuplicate(vs []string) string {
	seen := make(map[string]bool, len(vs))
	for _, v := range vs {
		if seen[v] {
			return v
		}
		seen[v] = true
	}
	return ""
}
