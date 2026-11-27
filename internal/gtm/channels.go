// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/reliant-labs/reliant/internal/store"
)

var (
	channelModes    = []ChannelMode{ChannelIntegrated, ChannelManual}
	channelStatuses = []ChannelStatus{ChannelActive, ChannelPaused, ChannelArchived}
	channelKey      = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	channelType     = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	integrationKind = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
)

const (
	maxChannelNameLen  = 100
	maxConnectionIDLen = 200
	maxChannelTypes    = 32
	// GmailIntegration is the integration an email-type item is sent through.
	GmailIntegration = "gmail"
)

// DefaultChannel is one channel every org starts with.
type DefaultChannel struct {
	Key                  string
	Name                 string
	Mode                 ChannelMode
	IntegrationKind      string
	ManualInstructionsMD string
	// ChannelTypes are generic types a library entry's channel_types match.
	ChannelTypes []string
}

// manualPost is the default channels' instructions for posting by hand.
const manualPost = "Post by hand, then paste the URL on the item."

// defaultChannels are what ListChannels and GetChannel seed for an org with
// no channel yet (source SEED).
var defaultChannels = []DefaultChannel{
	{Key: "blog", Name: "Blog", Mode: ChannelManual, ManualInstructionsMD: "Publish via the site repo, then record the URL.", ChannelTypes: []string{"blog", "content"}},
	{Key: "email", Name: "Email", Mode: ChannelIntegrated, IntegrationKind: GmailIntegration, ChannelTypes: []string{"email", "newsletter"}},
	{Key: "hacker-news", Name: "Hacker News", Mode: ChannelManual, ManualInstructionsMD: manualPost, ChannelTypes: []string{"forum", "community"}},
	{Key: "reddit", Name: "Reddit", Mode: ChannelManual, ManualInstructionsMD: manualPost, ChannelTypes: []string{"forum", "community"}},
	{Key: "linkedin", Name: "LinkedIn", Mode: ChannelManual, ManualInstructionsMD: manualPost, ChannelTypes: []string{"social"}},
	{Key: "x", Name: "X", Mode: ChannelManual, ManualInstructionsMD: manualPost, ChannelTypes: []string{"social"}},
	{Key: "youtube", Name: "YouTube", Mode: ChannelManual, ManualInstructionsMD: manualPost, ChannelTypes: []string{"video"}},
}

// DefaultChannels returns the channels ListChannels and GetChannel seed for
// an org with no channel yet (source SEED). The result is a copy.
func DefaultChannels() []DefaultChannel {
	out := make([]DefaultChannel, len(defaultChannels))
	for i, d := range defaultChannels {
		d.ChannelTypes = slices.Clone(d.ChannelTypes)
		out[i] = d
	}
	return out
}

// ensureChannels seeds the defaults for an org with no channel yet. It is
// idempotent: a second call, or a race, inserts nothing (ON CONFLICT).
func (s *service) ensureChannels(ctx context.Context, orgID string) error {
	n, err := s.store.CountChannels(ctx, orgID)
	if err != nil || n > 0 {
		return err
	}
	now := s.now()
	rows := make([]*store.GtmChannel, 0, len(defaultChannels))
	for _, d := range defaultChannels {
		rows = append(rows, seededChannelRow(orgID, d, now))
	}
	return s.store.InTx(ctx, func(ctx context.Context) error {
		return s.store.SeedChannels(ctx, rows)
	})
}

// connectionState computes ch's connection_state for actor: display only.
func (s *service) connectionState(ctx context.Context, actor Actor, ch *Channel) {
	if ch.Mode != ChannelIntegrated {
		ch.ConnectionState = ""
		return
	}
	if s.connections == nil {
		ch.ConnectionState = ConnectionUnknown
		return
	}
	st, err := s.connections.State(ctx, actor, ch.IntegrationKind, ch.ConnectionID)
	if err != nil || (st != ConnectionConnected && st != ConnectionNotConnected && st != ConnectionUnknown) {
		if err != nil {
			s.log.DebugContext(ctx, "gtm: connection state unknown", "channel_id", ch.ID, "error", err)
		}
		st = ConnectionUnknown
	}
	ch.ConnectionState = st
}

// ListChannels lists the org's channels by key, seeding the defaults first.
func (s *service) ListChannels(ctx context.Context, actor Actor, in ListChannelsInput) (ListChannelsResult, error) {
	if err := s.admit(ctx, actor); err != nil {
		return ListChannelsResult{}, err
	}
	if in.Status != "" && !slices.Contains(channelStatuses, in.Status) {
		return ListChannelsResult{}, invalid("status", "unknown status")
	}
	if in.Mode != "" && !slices.Contains(channelModes, in.Mode) {
		return ListChannelsResult{}, invalid("mode", "unknown mode")
	}
	if err := s.ensureChannels(ctx, actor.OrgID); err != nil {
		return ListChannelsResult{}, err
	}
	rows, err := s.store.ListChannels(ctx, ChannelFilter{OrgID: actor.OrgID, Status: in.Status, Mode: in.Mode, Limit: MaxChannels})
	if err != nil {
		return ListChannelsResult{}, err
	}
	out := make([]Channel, 0, len(rows))
	for _, r := range rows {
		ch := channelFromRow(r)
		s.connectionState(ctx, actor, &ch)
		out = append(out, ch)
	}
	return ListChannelsResult{Channels: out}, nil
}

// GetChannel returns one channel, seeding the defaults first.
func (s *service) GetChannel(ctx context.Context, actor Actor, in GetChannelInput) (Channel, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Channel{}, err
	}
	if err := checkID("id", in.ID); err != nil {
		return Channel{}, err
	}
	if err := s.ensureChannels(ctx, actor.OrgID); err != nil {
		return Channel{}, err
	}
	row, err := s.store.GetChannel(ctx, actor.OrgID, in.ID)
	if err != nil {
		return Channel{}, s.notFound(ctx, err, "channel", in.ID)
	}
	ch := channelFromRow(row)
	s.connectionState(ctx, actor, &ch)
	return ch, nil
}

// CreateChannel creates a channel. An agent's is MANUAL, sourced AGENT.
func (s *service) CreateChannel(ctx context.Context, actor Actor, in CreateChannelInput) (Channel, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Channel{}, err
	}
	status := in.Status
	if status == "" {
		status = ChannelActive
	}
	if err := firstErr(
		checkChannelKey(in.Key),
		checkLen("name", in.Name, 1, maxChannelNameLen),
		checkLen("manual_instructions_md", in.ManualInstructionsMD, 0, MaxShortTextLen),
		checkChannelTypes(in.ChannelTypes),
	); err != nil {
		return Channel{}, err
	}
	if !slices.Contains(channelModes, in.Mode) {
		return Channel{}, invalid("mode", "required")
	}
	if !slices.Contains(channelStatuses, status) {
		return Channel{}, invalid("status", "unknown status")
	}
	if !actor.Person() && (in.Mode != ChannelManual || in.IntegrationKind != "" || in.ConnectionID != "") {
		return Channel{}, personOnly("set up an integrated channel")
	}
	if err := checkChannelIntegration(in.Mode, in.IntegrationKind, in.ConnectionID); err != nil {
		return Channel{}, err
	}
	source := ChannelSourcePerson
	if !actor.Person() {
		source = ChannelSourceAgent
	}
	now := s.now()
	row := &store.GtmChannel{
		OrgID:                actor.OrgID,
		Key:                  in.Key,
		Name:                 in.Name,
		Mode:                 string(in.Mode),
		IntegrationKind:      strPtrOrNil(in.IntegrationKind),
		ConnectionID:         strPtrOrNil(in.ConnectionID),
		ManualInstructionsMd: in.ManualInstructionsMD,
		Status:               string(status),
		Revision:             1,
		Source:               string(source),
		CreatedBy:            actor.UserID,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	setChannelTypes(row, in.ChannelTypes)
	var out Channel
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		// The defaults first, so a key they take is AlreadyExists now rather
		// than a seed that silently skips it later.
		if err := s.ensureChannels(ctx, actor.OrgID); err != nil {
			return err
		}
		n, err := s.store.CountChannels(ctx, actor.OrgID)
		if err != nil {
			return err
		}
		if n >= MaxChannels {
			return ErrTooManyChannels
		}
		r := *row
		if err := s.store.CreateChannel(ctx, &r); err != nil {
			return err
		}
		out = channelFromRow(&r)
		return nil
	})
	if err != nil {
		return Channel{}, err
	}
	s.connectionState(ctx, actor, &out)
	return out, nil
}

// UpdateChannel edits a channel. An agent changes a MANUAL channel's key,
// name, instructions, types and status, and never an integration.
func (s *service) UpdateChannel(ctx context.Context, actor Actor, in UpdateChannelInput) (Channel, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Channel{}, err
	}
	if err := firstErr(checkID("id", in.ID), checkRevision(in.ExpectedRevision)); err != nil {
		return Channel{}, err
	}
	var checks []error
	if in.Key != nil {
		checks = append(checks, checkChannelKey(*in.Key))
	}
	if in.Name != nil {
		checks = append(checks, checkLen("name", *in.Name, 1, maxChannelNameLen))
	}
	if in.ManualInstructionsMD != nil {
		checks = append(checks, checkLen("manual_instructions_md", *in.ManualInstructionsMD, 0, MaxShortTextLen))
	}
	if in.Mode != nil && !slices.Contains(channelModes, *in.Mode) {
		checks = append(checks, invalid("mode", "unknown mode"))
	}
	if in.Status != nil && !slices.Contains(channelStatuses, *in.Status) {
		checks = append(checks, invalid("status", "unknown status"))
	}
	if in.ChannelTypes != nil {
		checks = append(checks, checkChannelTypes(*in.ChannelTypes))
	}
	if err := firstErr(checks...); err != nil {
		return Channel{}, err
	}
	integration := in.Mode != nil || in.IntegrationKind != nil || in.ConnectionID != nil
	var out Channel
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		row, err := s.store.GetChannel(ctx, actor.OrgID, in.ID)
		if err != nil {
			return s.notFound(ctx, err, "channel", in.ID)
		}
		if row.Revision != in.ExpectedRevision {
			return conflict(in.ID, in.ExpectedRevision, row.Revision)
		}
		if !actor.Person() && (integration || ChannelMode(row.Mode) != ChannelManual) {
			if integration {
				return personOnly("change a channel's integration")
			}
			return personOnly("change an integrated channel")
		}
		if in.Key != nil {
			row.Key = *in.Key
		}
		if in.Name != nil {
			row.Name = *in.Name
		}
		if in.Mode != nil {
			row.Mode = string(*in.Mode)
		}
		if in.IntegrationKind != nil {
			row.IntegrationKind = strPtrOrNil(*in.IntegrationKind)
		}
		if in.ConnectionID != nil {
			row.ConnectionID = strPtrOrNil(*in.ConnectionID)
		}
		if err := checkChannelIntegration(ChannelMode(row.Mode), deref(row.IntegrationKind), deref(row.ConnectionID)); err != nil {
			return err
		}
		if in.ManualInstructionsMD != nil {
			row.ManualInstructionsMd = *in.ManualInstructionsMD
		}
		if in.Status != nil {
			row.Status = string(*in.Status)
		}
		if in.ChannelTypes != nil {
			setChannelTypes(row, *in.ChannelTypes)
		}
		row.UpdatedAt = s.now()
		if err := s.store.UpdateChannel(ctx, row, in.ExpectedRevision); err != nil {
			return s.writeErr(err, in.ID, in.ExpectedRevision, func() (int32, error) {
				cur, err := s.store.GetChannel(ctx, actor.OrgID, in.ID)
				if err != nil {
					return 0, err
				}
				return cur.Revision, nil
			})
		}
		stored, err := s.store.GetChannel(ctx, actor.OrgID, in.ID)
		if err != nil {
			return s.notFound(ctx, err, "channel", in.ID)
		}
		out = channelFromRow(stored)
		return nil
	})
	if err != nil {
		return Channel{}, err
	}
	s.connectionState(ctx, actor, &out)
	return out, nil
}

func checkChannelKey(key string) error {
	if !channelKey.MatchString(key) {
		return invalid("key", "a slug: lowercase letters, digits and '-', 1 to 32")
	}
	return nil
}

func checkChannelTypes(types []string) error {
	if len(types) > maxChannelTypes {
		return invalid("channel_types", "at most 32")
	}
	seen := map[string]bool{}
	for _, t := range types {
		if !channelType.MatchString(t) {
			return invalid("channel_types", "each a slug: lowercase letters, digits and '-', 1 to 32")
		}
		if seen[t] {
			return invalid("channel_types", "each type once")
		}
		seen[t] = true
	}
	return nil
}

// checkChannelIntegration is the table's mode CHECKs, as named errors.
func checkChannelIntegration(mode ChannelMode, kind, connectionID string) error {
	switch mode {
	case ChannelIntegrated:
		if !integrationKind.MatchString(kind) {
			return invalid("integration_kind", "an integrated channel needs its integration (a catalog id)")
		}
		if len(connectionID) > maxConnectionIDLen {
			return invalid("connection_id", "too long")
		}
	case ChannelManual:
		if kind != "" || connectionID != "" {
			return invalid("integration_kind", "a manual channel has no integration or connection")
		}
	}
	return nil
}

// checkChannelLink is rule 2 for an item's channel: "" or a channel of the
// org.
func (s *service) checkChannelLink(ctx context.Context, orgID, id string) error {
	if id == "" {
		return nil
	}
	if _, err := s.store.GetChannel(ctx, orgID, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			return &LinkError{Field: "channel_id", ID: id}
		}
		return err
	}
	return nil
}

func seededChannelRow(orgID string, d DefaultChannel, now time.Time) *store.GtmChannel {
	r := &store.GtmChannel{
		ID:                   ulid.Make().String(),
		OrgID:                orgID,
		Key:                  d.Key,
		Name:                 d.Name,
		Mode:                 string(d.Mode),
		IntegrationKind:      strPtrOrNil(d.IntegrationKind),
		ManualInstructionsMd: d.ManualInstructionsMD,
		Status:               string(ChannelActive),
		Revision:             1,
		Source:               string(ChannelSourceSeed),
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	setChannelTypes(r, d.ChannelTypes)
	return r
}
