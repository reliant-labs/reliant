// Copyright (c) 2026 Reliant Labs

package gtm

import "github.com/reliant-labs/reliant/internal/store"

// The gtm_channels.channel_types column arrives with the next PR-2 head.
// Until it does, a channel's types are not persisted; these two are where
// the row mapping goes once it lands.

func setChannelTypes(_ *store.GtmChannel, _ []string) {}

func channelTypesOf(_ *store.GtmChannel) []string { return nil }
