// Copyright (c) 2025 Reliant Labs

// Package askuser is the app's name for the ask_user question metadata parser.
// The implementation lives in the SDK (sdk/go/reliant/askuser), which the follow
// engine needs and which cannot import this module; every name here is an alias
// of it, so there is one parser and the web client's askUserUtils.ts has one Go
// counterpart to agree with.
//
//forge:exclude-contract: aliases of sdk/go/reliant/askuser; no behaviour of its own
package askuser

import sdk "github.com/reliant-labs/reliant/sdk/go/reliant/askuser"

type (
	Option   = sdk.Option
	Question = sdk.Question
	Metadata = sdk.Metadata
	Answer   = sdk.Answer
)

var (
	ParseMetadata     = sdk.ParseMetadata
	IsAskUser         = sdk.IsAskUser
	BuildResponseData = sdk.BuildResponseData
)
