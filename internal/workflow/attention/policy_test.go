// Copyright (c) 2025 Reliant Labs
package attention

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

func TestValidateAttentionPolicy(t *testing.T) {
	for _, policy := range []*reliantv1.WorkflowAttention{
		nil,
		{Key: "incident"},
		{Key: strings.Repeat("x", 512)},
		{Key: "{{ inputs.key }}"},
		{Key: "repo:{{ inputs.repo }}:pr:{{ inputs.pr }}", OnStart: "recovering", RecoveryTimeout: "30m", OnSuccess: "resolved"},
		{Key: "incident", OnStart: "recovering", RecoveryTimeout: "1s"},
		{Key: "incident", OnStart: "recovering", RecoveryTimeout: "24h"},
		{Key: "incident", OnStart: "action_required", OnSuccess: "action_required", OnFailure: "action_required"},
	} {
		require.NoError(t, Validate(policy), "%v", policy)
	}
	for name, policy := range map[string]*reliantv1.WorkflowAttention{
		"empty":              {},
		"blank":              {Key: " \n"},
		"oversized":          {Key: strings.Repeat("é", 257)},
		"start resolved":     {Key: "key", OnStart: "resolved"},
		"unknown start":      {Key: "key", OnStart: "RECOVERING"},
		"success recovering": {Key: "key", OnSuccess: "recovering"},
		"failure resolved":   {Key: "key", OnFailure: "resolved"},
		"missing timeout":    {Key: "key", OnStart: "recovering"},
		"invalid timeout":    {Key: "key", OnStart: "recovering", RecoveryTimeout: "tomorrow"},
		"zero timeout":       {Key: "key", OnStart: "recovering", RecoveryTimeout: "0s"},
		"negative timeout":   {Key: "key", OnStart: "recovering", RecoveryTimeout: "-1s"},
		"small timeout":      {Key: "key", OnStart: "recovering", RecoveryTimeout: "999ms"},
		"large timeout":      {Key: "key", OnStart: "recovering", RecoveryTimeout: "24h1ns"},
		"overflow timeout":   {Key: "key", OnStart: "recovering", RecoveryTimeout: "999999999999999999h"},
		"orphan timeout":     {Key: "key", RecoveryTimeout: "1m"},
		"action timeout":     {Key: "key", OnStart: "action_required", RecoveryTimeout: "1m"},
		"unclosed template":  {Key: "{{ inputs.key"},
		"empty expression":   {Key: "{{ }}"},
		"invalid expression": {Key: "{{ inputs. }}"},
		"nodes scope":        {Key: "{{ nodes.a }}"},
		"trigger scope":      {Key: "{{ trigger.id }}"},
		"workflow scope":     {Key: "{{ workflow.id }}"},
		"number":             {Key: "{{ 123 }}"},
		"bool":               {Key: " {{ true }} "},
		"null":               {Key: "{{ null }}"},
	} {
		t.Run(name, func(t *testing.T) { require.Error(t, Validate(policy)) })
	}
}
