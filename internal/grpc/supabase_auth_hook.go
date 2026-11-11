// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"net/http"

	"github.com/reliant-labs/forge/pkg/authhooks"

	"github.com/reliant-labs/reliant/internal/analytics"
)

// NewSupabaseAuthHook is the endpoint the Supabase project's auth-event
// triggers post to (deploy/supabase/forge_auth_events.sql): forge's
// authhooks handler, which records sign-ups and credential sign-ins as the
// standard auth events with provider "supabase". secret is the bearer the
// triggers send (SUPABASE_AUTH_HOOK_SECRET, stored in the project's Vault as
// forge_auth_hook_secret); empty is nil, and no route is served.
//
// A user who opted out of analytics records nothing here either: Allow reads
// the same consent every backend product event honours (analytics.ForUser).
// An account the API has never seen reads the default, as at its first
// request.
func NewSupabaseAuthHook(secret string) http.Handler {
	if secret == "" {
		return nil
	}
	return authhooks.Supabase(authhooks.Options{
		Secret: secret,
		Allow:  supabaseAuthHookAllows,
	})
}

func supabaseAuthHookAllows(ctx context.Context, ev authhooks.Event) bool {
	return analytics.ForUser(ctx, ev.UserID).Recording()
}
