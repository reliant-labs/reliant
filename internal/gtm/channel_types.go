// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"slices"

	"github.com/reliant-labs/reliant/internal/store"
)

// setChannelTypes writes a channel's types (gtm_channels.channel_types).
func setChannelTypes(r *store.GtmChannel, types []string) { r.ChannelTypes = nonNil(types) }

// channelTypesOf reads a channel's types.
func channelTypesOf(r *store.GtmChannel) []string { return slices.Clone(r.ChannelTypes) }
