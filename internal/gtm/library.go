// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/reliant-labs/reliant/internal/store"
)

var (
	libraryDepths = []LibraryDepth{DepthPlay, DepthTactic}
	libraryKey    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)
)

// Library entry field caps.
const (
	maxLibraryNameLen    = 200
	maxLibraryVocabLen   = 200
	maxLibraryListLen    = 100
	maxLibraryListItem   = 500
	maxLibrarySteps      = 100
	maxLibraryExtraBytes = 64 << 10
	maxSourceRevisionLen = 200
	maxLibraryQueryLen   = 200
)

// ListStrategyLibrary lists the org's library, by category then name.
func (s *service) ListStrategyLibrary(ctx context.Context, actor Actor, in ListStrategyLibraryInput) (ListStrategyLibraryResult, error) {
	if err := s.admit(ctx, actor); err != nil {
		return ListStrategyLibraryResult{}, err
	}
	limit, offset, err := page(in.PageSize, in.PageToken, DefaultLibraryPageSize, MaxLibraryPageSize)
	if err != nil {
		return ListStrategyLibraryResult{}, err
	}
	if in.Category != "" && !slices.Contains(strategyCategories, in.Category) {
		return ListStrategyLibraryResult{}, invalid("category", "unknown category")
	}
	if in.Depth != "" && !slices.Contains(libraryDepths, in.Depth) {
		return ListStrategyLibraryResult{}, invalid("depth", "unknown depth")
	}
	if err := firstErr(
		checkLen("stage", in.Stage, 0, maxLibraryVocabLen),
		checkLen("cost", in.Cost, 0, maxLibraryVocabLen),
		checkLen("difficulty", in.Difficulty, 0, maxLibraryVocabLen),
		checkLen("fit", in.Fit, 0, maxLibraryVocabLen),
		checkLen("query", in.Query, 0, maxLibraryQueryLen),
	); err != nil {
		return ListStrategyLibraryResult{}, err
	}
	rows, err := s.store.ListLibrary(ctx, LibraryFilter{
		OrgID: actor.OrgID, Category: in.Category, Depth: in.Depth, Stage: in.Stage, Cost: in.Cost,
		Difficulty: in.Difficulty, Fit: in.Fit, Query: in.Query, Limit: limit, Offset: offset,
	})
	if err != nil {
		return ListStrategyLibraryResult{}, err
	}
	rows, next := nextToken(rows, limit, offset)
	out := make([]LibraryEntry, 0, len(rows))
	for _, r := range rows {
		e, err := libraryFromRow(r)
		if err != nil {
			return ListStrategyLibraryResult{}, err
		}
		out = append(out, e)
	}
	return ListStrategyLibraryResult{Entries: out, NextPageToken: next}, nil
}

// GetStrategyLibraryEntry returns one library entry by key.
func (s *service) GetStrategyLibraryEntry(ctx context.Context, actor Actor, in GetStrategyLibraryEntryInput) (LibraryEntry, error) {
	if err := s.admit(ctx, actor); err != nil {
		return LibraryEntry{}, err
	}
	if !libraryKey.MatchString(in.Key) {
		return LibraryEntry{}, invalid("key", "a library key")
	}
	row, err := s.store.GetLibraryEntry(ctx, actor.OrgID, in.Key)
	if err != nil {
		return LibraryEntry{}, s.notFound(ctx, err, "library entry", in.Key)
	}
	return libraryFromRow(row)
}

// ImportStrategyLibrary upserts a batch of entries by key: a person's act.
// An entry equal to the stored one (every field but the import's own
// stamps) is left alone. With Prune, the org's entries whose key is not in
// AllKeys go; strategies made from them keep their library_key.
func (s *service) ImportStrategyLibrary(ctx context.Context, actor Actor, in ImportStrategyLibraryInput) (ImportStrategyLibraryResult, error) {
	if err := s.admit(ctx, actor); err != nil {
		return ImportStrategyLibraryResult{}, err
	}
	if !actor.Person() {
		return ImportStrategyLibraryResult{}, personOnly("import the strategy library")
	}
	if err := checkImport(in); err != nil {
		return ImportStrategyLibraryResult{}, err
	}
	keys := make([]string, 0, len(in.Entries))
	for _, e := range in.Entries {
		keys = append(keys, e.Key)
	}
	var out ImportStrategyLibraryResult
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		out = ImportStrategyLibraryResult{}
		existing, err := s.store.ListLibrary(ctx, LibraryFilter{OrgID: actor.OrgID, Keys: keys, Limit: len(keys) + 1})
		if err != nil {
			return err
		}
		byKey := make(map[string]*store.GtmStrategyLibrary, len(existing))
		for _, r := range existing {
			byKey[r.Key] = r
		}
		now := s.now()
		for _, e := range in.Entries {
			row, err := libraryRow(actor.OrgID, e)
			if err != nil {
				return err
			}
			row.SourceRevision, row.ImportedAt, row.ImportedBy = in.SourceRevision, now, actor.UserID
			cur, ok := byKey[e.Key]
			if !ok {
				if err := s.store.CreateLibraryEntry(ctx, row); err != nil {
					return err
				}
				out.Created++
				continue
			}
			same, err := sameLibraryEntry(cur, row)
			if err != nil {
				return err
			}
			if same {
				out.Unchanged++
				continue
			}
			row.ID = cur.ID
			if err := s.store.UpdateLibraryEntry(ctx, row); err != nil {
				return err
			}
			out.Updated++
		}
		if in.Prune {
			n, err := s.store.PruneLibrary(ctx, actor.OrgID, in.AllKeys)
			if err != nil {
				return err
			}
			out.Pruned = int(n)
		}
		return nil
	})
	return out, err
}

// sameLibraryEntry reports whether cur already holds what row would write,
// the import's stamps aside.
func sameLibraryEntry(cur, row *store.GtmStrategyLibrary) (bool, error) {
	a, err := libraryFromRow(cur)
	if err != nil {
		return false, err
	}
	b, err := libraryFromRow(row)
	if err != nil {
		return false, err
	}
	for _, e := range []*LibraryEntry{&a, &b} {
		e.SourceRevision, e.ImportedBy, e.ImportedAt = "", "", time.Time{}
		for _, l := range []*[]string{&e.Lifecycle, &e.ChannelTypes, &e.Integrations, &e.Fits, &e.Prerequisites,
			&e.KPIs, &e.Tactics, &e.Automates, &e.Experiments, &e.StopWhen} {
			if len(*l) == 0 {
				*l = nil
			}
		}
		if len(e.Steps) == 0 {
			e.Steps = nil
		}
		if len(e.Extra) == 0 {
			e.Extra = nil
		}
	}
	return reflect.DeepEqual(a, b), nil
}

func checkImport(in ImportStrategyLibraryInput) error {
	if n := len(in.Entries); n < 1 || n > MaxLibraryImport {
		return invalid("entries", fmt.Sprintf("1 to %d per call", MaxLibraryImport))
	}
	if err := checkLen("source_revision", in.SourceRevision, 1, maxSourceRevisionLen); err != nil {
		return err
	}
	if len(in.AllKeys) > MaxLibraryKeys {
		return invalid("all_keys", fmt.Sprintf("at most %d", MaxLibraryKeys))
	}
	all := make(map[string]bool, len(in.AllKeys))
	for _, k := range in.AllKeys {
		if !libraryKey.MatchString(k) {
			return invalid("all_keys", fmt.Sprintf("%q is not a library key", k))
		}
		if all[k] {
			return invalid("all_keys", fmt.Sprintf("%q twice", k))
		}
		all[k] = true
	}
	if in.Prune && len(in.AllKeys) == 0 {
		return invalid("all_keys", "required with prune: every key of the library")
	}
	seen := make(map[string]bool, len(in.Entries))
	for i, e := range in.Entries {
		if err := checkLibraryEntry(e); err != nil {
			var ve *ValidationError
			if errors.As(err, &ve) {
				return invalid(fmt.Sprintf("entries[%d].%s", i, ve.Field), ve.Reason)
			}
			return err
		}
		if seen[e.Key] {
			return invalid(fmt.Sprintf("entries[%d].key", i), fmt.Sprintf("%q twice in the batch", e.Key))
		}
		seen[e.Key] = true
		if in.Prune && !all[e.Key] {
			return invalid(fmt.Sprintf("entries[%d].key", i), fmt.Sprintf("%q is not in all_keys", e.Key))
		}
	}
	return nil
}

func checkLibraryEntry(e LibraryEntry) error {
	if !libraryKey.MatchString(e.Key) {
		return invalid("key", "a slug: [a-z0-9][a-z0-9_-]{0,127}")
	}
	if !slices.Contains(strategyCategories, e.Category) {
		return invalid("category", "required, one of the 13")
	}
	if !slices.Contains(libraryDepths, e.Depth) {
		return invalid("depth", "required: PLAY or TACTIC")
	}
	checks := []error{
		checkLen("name", e.Name, 1, maxLibraryNameLen),
		checkLen("summary_md", e.SummaryMD, 0, MaxShortTextLen),
		checkLen("hypothesis_md", e.HypothesisMD, 0, MaxShortTextLen),
	}
	for field, v := range map[string]string{
		"icon": e.Icon, "stage": e.Stage, "cost": e.Cost, "cost_note": e.CostNote, "difficulty": e.Difficulty,
		"level": e.Level, "objective": e.Objective, "north_star": e.NorthStar,
	} {
		checks = append(checks, checkLen(field, v, 0, maxLibraryVocabLen))
	}
	for field, v := range map[string]int32{"hours": e.Hours, "signal_days": e.SignalDays, "window_days": e.WindowDays, "launch_days": e.LaunchDays} {
		if v < 0 {
			checks = append(checks, invalid(field, "0 or more"))
		}
	}
	if math.IsNaN(e.ExpectedDelta) || math.IsInf(e.ExpectedDelta, 0) {
		checks = append(checks, invalid("expected_delta", "a finite number"))
	}
	for field, l := range map[string][]string{
		"lifecycle": e.Lifecycle, "channel_types": e.ChannelTypes, "integrations": e.Integrations, "fits": e.Fits,
		"prerequisites": e.Prerequisites, "kpis": e.KPIs, "tactics": e.Tactics, "automates": e.Automates,
		"experiments": e.Experiments, "stop_when": e.StopWhen,
	} {
		checks = append(checks, checkList(field, l))
	}
	if len(e.Steps) > maxLibrarySteps {
		checks = append(checks, invalid("steps", fmt.Sprintf("at most %d", maxLibrarySteps)))
	}
	for _, st := range e.Steps {
		checks = append(checks, checkLen("steps.who", st.Who, 0, maxLibraryVocabLen), checkLen("steps.title", st.Title, 1, maxLibraryListItem))
	}
	if e.Extra != nil {
		b, err := json.Marshal(e.Extra)
		if err != nil {
			checks = append(checks, invalid("extra", "not JSON"))
		} else if len(b) > maxLibraryExtraBytes {
			checks = append(checks, invalid("extra", "at most 64 KiB"))
		}
	}
	return firstErr(checks...)
}

func checkList(field string, l []string) error {
	if len(l) > maxLibraryListLen {
		return invalid(field, fmt.Sprintf("at most %d entries", maxLibraryListLen))
	}
	for _, v := range l {
		if strings.TrimSpace(v) == "" || utf8.RuneCountInString(v) > maxLibraryListItem {
			return invalid(field, fmt.Sprintf("each 1 to %d characters", maxLibraryListItem))
		}
	}
	return nil
}

// UseLibraryStrategy makes a DRAFT strategy from a library entry, and with
// CreateFirstCampaign a first DRAFT campaign under it that plans the org's
// ACTIVE channels an entry's channel_types name: by key, or by a channel
// type in common. A person or an agent may.
func (s *service) UseLibraryStrategy(ctx context.Context, actor Actor, in UseLibraryStrategyInput) (UseLibraryStrategyResult, error) {
	if err := s.admit(ctx, actor); err != nil {
		return UseLibraryStrategyResult{}, err
	}
	if !libraryKey.MatchString(in.LibraryKey) {
		return UseLibraryStrategyResult{}, invalid("library_key", "a library key")
	}
	if in.CampaignName != nil {
		if !in.CreateFirstCampaign {
			return UseLibraryStrategyResult{}, invalid("campaign_name", "only with create_first_campaign")
		}
		if err := checkLen("campaign_name", *in.CampaignName, 1, MaxCampaignNameLen); err != nil {
			return UseLibraryStrategyResult{}, err
		}
	}
	if in.CreateFirstCampaign {
		if err := s.ensureChannels(ctx, actor.OrgID); err != nil {
			return UseLibraryStrategyResult{}, err
		}
	}
	var out UseLibraryStrategyResult
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		out = UseLibraryStrategyResult{}
		er, err := s.store.GetLibraryEntry(ctx, actor.OrgID, in.LibraryKey)
		if err != nil {
			return s.notFound(ctx, err, "library entry", in.LibraryKey)
		}
		entry, err := libraryFromRow(er)
		if err != nil {
			return err
		}
		now := s.now()
		srow := &store.GtmStrategy{
			OrgID:        actor.OrgID,
			Name:         clip(entry.Name, maxStrategyNameLen),
			GoalMd:       joinNonEmpty("\n\n", entry.NorthStar, entry.Objective),
			TargetMetric: clip(strings.Join(entry.KPIs, ", "), maxTargetMetricLen),
			Status:       string(StrategyDraft),
			Category:     strPtrOrNil(string(entry.Category)),
			LibraryKey:   strPtrOrNil(entry.Key),
			Revision:     1,
			CreatedBy:    actor.UserID,
			UpdatedBy:    actor.UserID,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := s.store.CreateStrategy(ctx, srow); err != nil {
			return err
		}
		out.Strategy = strategyFromRow(srow)
		if !in.CreateFirstCampaign {
			return nil
		}
		channels, err := s.store.ListChannels(ctx, ChannelFilter{OrgID: actor.OrgID, Status: ChannelActive, Limit: MaxChannels})
		if err != nil {
			return err
		}
		name := entry.Name
		if in.CampaignName != nil {
			name = *in.CampaignName
		}
		crow := &store.GtmCampaign{
			OrgID:             actor.OrgID,
			Name:              clip(name, MaxCampaignNameLen),
			Status:            string(CampaignDraft),
			StrategyID:        srow.ID,
			PlannedChannelIds: plannedChannels(entry.ChannelTypes, channels),
			PlannedFormats:    []string{},
			Revision:          1,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		if err := s.store.CreateCampaign(ctx, crow); err != nil {
			return err
		}
		c := campaignFromRow(crow)
		out.Campaign = &c
		return nil
	})
	return out, err
}

// plannedChannels maps an entry's channel_types to the given channels: in
// channel_types order, each channel whose key is the type or whose types
// include it, once, capped at MaxPlannedChannels.
func plannedChannels(types []string, channels []*store.GtmChannel) []string {
	ids := []string{}
	seen := map[string]bool{}
	for _, t := range types {
		for _, ch := range channels {
			if seen[ch.ID] || len(ids) >= MaxPlannedChannels {
				continue
			}
			if ch.Key == t || slices.Contains(channelTypesOf(ch), t) {
				seen[ch.ID] = true
				ids = append(ids, ch.ID)
			}
		}
	}
	return ids
}

// clip cuts s to at most n runes.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func joinNonEmpty(sep string, parts ...string) string {
	var keep []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, sep)
}
