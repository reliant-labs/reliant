// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/antigravity"
	"github.com/reliant-labs/reliant/internal/llm/drivers/claude"
	"github.com/reliant-labs/reliant/internal/llm/drivers/codex"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

// BuildAvailableDrivers creates an AvailableDrivers struct from configured API keys
// and local provider configuration.
// userID is required to fetch API keys from the database.
func BuildAvailableDrivers(ctx context.Context, repo db.Repository, userID string) (models.AvailableDrivers, error) {
	return buildAvailableDrivers(ctx, repo, userID, AccountAvailabilityClient)
}

// AccountAvailabilityClient builds the credentialed client whose account
// catalog decides a dynamic provider's availability (BuildAvailableDrivers,
// ListAvailableModels). A variable, like GetDriver, so a test that resolves
// through GetAvailableDrivers never depends on a real provider's catalog
// answering — or refusing — its fake credential.
var AccountAvailabilityClient func(ctx context.Context, driverID models.DriverID, cfg models.DriverConfig) (registry.Client, error) = clientForDriver

// buildAvailableDrivers is BuildAvailableDrivers with the availability client
// builder injected, so tests never reach a provider's real catalog endpoint.
func buildAvailableDrivers(ctx context.Context, repo db.Repository, userID string, build clientBuilder) (models.AvailableDrivers, error) {
	accounts, unavailable, err := configuredAccounts(ctx, repo, userID)
	if err != nil {
		return models.AvailableDrivers{}, err
	}
	available := withAccountAvailability(ctx, accounts, build)
	for driverID, reason := range unavailable {
		if _, ok := available.Accounts[driverID]; ok {
			continue
		}
		if available.Unavailable == nil {
			available.Unavailable = make(map[models.DriverID]string)
		}
		if _, ok := available.Unavailable[driverID]; !ok {
			available.Unavailable[driverID] = reason
		}
	}
	return available, nil
}

// usableAPIKey reports whether a stored api_keys value is a credential: not
// empty, not the "dummy" placeholder, and not an OAuth provider's connection
// marker (db.ProviderOAuthMarker).
func usableAPIKey(key string) bool {
	key = strings.TrimSpace(key)
	return key != "" && key != "dummy" && key != db.ProviderOAuthMarker
}

// legacySyncer is the expand-period bridge to the legacy credential tables (db.Repo).
type legacySyncer interface {
	SyncLegacyCredentials(ctx context.Context, userID string) error
}

// syncLegacyAccounts levels the user's provider accounts with the legacy credential tables.
//
// During the expand release the connect and disconnect RPCs still write only those tables and
// the previous release may still rotate tokens in them, so reading provider_accounts alone
// would miss a connect, resurrect a disconnect, or run on a spent refresh token until the next
// boot copy. A failure is logged and never fails the read: the accounts are served as they are.
func syncLegacyAccounts(ctx context.Context, repo db.Repository, userID string) {
	syncer, ok := repo.(legacySyncer)
	if !ok {
		return
	}
	if err := syncer.SyncLegacyCredentials(ctx, userID); err != nil {
		logging.Warn("Could not level provider accounts with the legacy credential tables; using them as they are",
			"user_id", userID, "error", err)
	}
}

// configuredAccounts loads every usable account of every provider the user has connected, in
// priority order. Paused accounts and accounts needing sign-in are left out; limited ones are
// in (being limited is the account router's business, not resolution's). unavailable names the
// providers whose every account needs the user to sign in again, with the reason to show.
func configuredAccounts(ctx context.Context, repo db.Repository, userID string) (map[models.DriverID][]models.AccountConfig, map[models.DriverID]string, error) {
	if userID == "" {
		// Per reliant.md: no fallback paths, error out when things aren't set properly
		return nil, nil, fmt.Errorf("userID is required to fetch provider accounts from database")
	}
	syncLegacyAccounts(ctx, repo, userID)

	stored, err := repo.ListProviderAccountsWithSecrets(ctx, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get provider API keys and sign-ins: %w", err)
	}
	limits, err := repo.ListAccountLimits(ctx, userID)
	if err != nil {
		logging.Warn("Could not read account limit state; treating every account as unlimited", "user_id", userID, "error", err)
	}
	now := time.Now()

	accounts := make(map[models.DriverID][]models.AccountConfig)
	needsSignIn := make(map[models.DriverID]bool)
	for i := range stored {
		acct := &stored[i]
		driverID := models.DriverID(acct.Provider)
		// A provider is available only if its driver is registered. Credentials for
		// catalog-only providers (xai, groq, azure, bedrock) must not make their models
		// selectable: every call would fail at resolution.
		if !hasRegisteredDriver(acct.Provider) {
			continue
		}
		var lim *core.LimitState
		if l, ok := limits[acct.ID]; ok {
			lim = &l
		}
		status := acct.Status(lim, now)
		switch status {
		case core.AccountNeedsSignIn:
			needsSignIn[driverID] = true
			continue
		case core.AccountPaused:
			continue
		}
		cfg, ok := accountDriverConfig(ctx, userID, acct)
		if !ok {
			continue
		}
		ac := models.AccountConfig{
			DriverConfig: cfg,
			Priority:     acct.Priority,
			Status:       models.AccountStatus(status),
		}
		if lim != nil && lim.LimitedUntil != nil && lim.LimitedUntil.After(now) {
			ac.LimitedUntil = *lim.LimitedUntil
		}
		accounts[driverID] = append(accounts[driverID], ac)
	}

	unavailable := make(map[models.DriverID]string)
	for driverID := range needsSignIn {
		if len(accounts[driverID]) == 0 {
			unavailable[driverID] = fmt.Sprintf("%s needs you to sign in again", models.ProviderDisplayName(string(driverID)))
		}
	}
	return accounts, unavailable, nil
}

// accountDriverConfig shapes one stored account into the credential material its driver needs.
// ok is false for an account that cannot serve anything (no usable secret, or an expired OAuth
// session with no refresh token to recover it).
func accountDriverConfig(ctx context.Context, userID string, acct *core.AccountWithSecrets) (models.DriverConfig, bool) {
	driverID := models.DriverID(acct.Provider)
	secret := strings.TrimSpace(acct.Secrets.Secret)
	refresh := strings.TrimSpace(acct.Secrets.Refresh)
	cfg := models.DriverConfig{
		DriverID:         driverID,
		APIKey:           secret,
		Enabled:          true,
		UserID:           userID,
		AccountID:        acct.ID,
		AccountLabel:     acct.DisplayLabel(),
		AccountEmail:     acct.Email,
		AccountUUID:      acct.ExternalAccountID,
		OrganizationUUID: acct.OrganizationID,
	}

	switch acct.Provider {
	case "claude":
		// Don't skip expired tokens here: the transport interceptor will refresh them. Skip
		// only a session that is expired AND has no refresh token (the user must reconnect).
		if secret == "" || (claude.IsTokenExpired(acct.ExpiresAt) && refresh == "") {
			return cfg, false
		}
		// The subscription is its own provider, "claude", distinct from an Anthropic API key
		// ("anthropic"): both can be connected at once.
		cfg.RefreshToken, cfg.TokenExpiresAt = refresh, acct.ExpiresAt
	case "codex":
		if secret == "" || (codex.IsTokenExpired(secret) && refresh == "") {
			return cfg, false
		}
		// The ChatGPT account id is the per-credential discriminator: it makes each upstream
		// account derive a distinct installation_id, so one Reliant user's several accounts
		// don't share a device fingerprint (Codex enforces one account per device). Expiry is
		// the JWT's exp claim; an unparseable token yields the zero time, which the refresh
		// transport treats as "refresh before using".
		cfg.RefreshToken, cfg.TokenExpiresAt = refresh, codexTokenExpiry(secret)
	case antigravity.DriverID:
		if secret == "" || (antigravity.IsTokenExpired(acct.ExpiresAt) && refresh == "") {
			return cfg, false
		}
		cfg.RefreshToken, cfg.TokenExpiresAt = refresh, acct.ExpiresAt
		cfg.AccountUUID, cfg.OrganizationUUID = "", ""
	case "copilot":
		// The credential is the GitHub OAuth token from the device flow, used as a raw gho_
		// Bearer against a single host.
		if !usableAPIKey(secret) {
			return cfg, false
		}
		cfg.AccountUUID, cfg.OrganizationUUID = "", ""
	default:
		if !usableAPIKey(secret) {
			return cfg, false
		}
		cfg.AccountUUID, cfg.OrganizationUUID, cfg.AccountEmail = "", "", ""
		// The gateway authenticates only rlat_ access tokens (401 otherwise), so a
		// legacy-shaped key is not a usable credential. Never log the key.
		if acct.Provider == "reliant" && !IsReliantLLMKey(secret) {
			healed, configured, healErr := HealReliantKey(ctx, userID)
			if healErr == nil && IsReliantLLMKey(healed) {
				secret = healed
				cfg.APIKey = healed
			} else {
				logging.Warn("Skipping reliant provider key: not an rlat_ access token and it could not be healed",
					"user_id", userID, "reason", "legacy_reliant_key_format",
					"heal_configured", configured, "error", healErr)
				return cfg, false
			}
		}
		// Add special base URLs for certain drivers.
		switch acct.Provider {
		case "openrouter":
			cfg.BaseURL = "https://openrouter.ai/api/v1"
		case "reliant":
			// Reliant routes to the admin-server proxy via RELIANT_API_BASE_URL.
			cfg.BaseURL = ResolveReliantBaseURL(secret)
		}
	}
	return cfg, true
}

// withAccountAvailability asks each configured dynamic provider account (Copilot,
// Codex: the clients implementing registry.AvailabilityReporter) what it may use, and applies
// the answers. Each report is TTL-cached by its driver, so a warm resolution makes no network
// call. The reports are per ACCOUNT: two Codex accounts may be on different plans.
//
// A failed report is either a VERDICT or an OUTAGE, and the two must not be confused:
//
//   - Verdict (registry.ErrCredentialRejected: the provider answered 400, 401 or 403 to the
//     credential every request would carry). That ACCOUNT leaves Accounts for this build, so no
//     resolution can route to it. When it was the provider's last account the provider leaves
//     Drivers and Unavailable records why, for the error that names the fix. Treating it as
//     servable is what sent chat dfd85515's sub-agents to a Copilot that answered every
//     request "Authorization header is badly formatted". The verdict is not persisted: the
//     first report the provider accepts puts the account back, with no reconnect. Persisting
//     needs-sign-in is the refresh coordinator's job, on the one signal that is authoritative
//     (invalid_grant).
//   - Outage (transport error, timeout, 429, 5xx, an unparseable body). The account stays,
//     unfiltered: the credential is not in question, and a catalog blip must never strand a
//     chat on an otherwise healthy provider.
func withAccountAvailability(ctx context.Context, accounts map[models.DriverID][]models.AccountConfig, build clientBuilder) models.AvailableDrivers {
	reg, regErr := models.GetRegistry()
	reports := make(map[string][]registry.ProviderAvailability)
	var unavailable map[models.DriverID]string

	kept := make(map[models.DriverID][]models.AccountConfig, len(accounts))
	for driverID, list := range accounts {
		var firstReason string
		for _, acct := range list {
			if !acct.IsConfigured() {
				kept[driverID] = append(kept[driverID], acct)
				continue
			}
			client, err := build(ctx, driverID, acct.DriverConfig)
			if err != nil {
				kept[driverID] = append(kept[driverID], acct)
				continue
			}
			reporter, ok := client.(registry.AvailabilityReporter)
			if !ok {
				kept[driverID] = append(kept[driverID], acct)
				continue
			}
			report, err := reporter.ReportAvailability(ctx)
			var rejected *registry.CredentialRejectedError
			if errors.As(err, &rejected) {
				logging.Warn("provider rejected the stored credential; account unavailable until reconnected",
					"driver", string(driverID), "account_id", acct.AccountID, "error", err)
				if firstReason == "" {
					firstReason = fmt.Sprintf("%s rejected the saved credential (HTTP %d: %s)",
						models.ProviderDisplayName(string(driverID)), rejected.Status, rejected.Detail)
				}
				continue
			}
			if err != nil {
				logging.Warn("provider availability report failed (outage, not a credential verdict); treating its models as servable",
					"driver", string(driverID), "account_id", acct.AccountID, "error", err)
				kept[driverID] = append(kept[driverID], acct)
				continue
			}
			reports[string(driverID)] = append(reports[string(driverID)], report)
			if regErr == nil {
				acct.Serves = servesFromReport(reg, string(driverID), report)
			}
			kept[driverID] = append(kept[driverID], acct)
		}
		if len(kept[driverID]) == 0 && firstReason != "" {
			if unavailable == nil {
				unavailable = make(map[models.DriverID]string)
			}
			unavailable[driverID] = firstReason
		}
	}

	available := models.AvailableDrivers{
		Drivers:     make(map[models.DriverID]models.DriverConfig),
		Accounts:    make(map[models.DriverID][]models.AccountConfig),
		Unavailable: unavailable,
	}
	for driverID, list := range kept {
		if len(list) == 0 {
			continue
		}
		available.Accounts[driverID] = list
		available.Drivers[driverID] = list[0].DriverConfig
	}
	if regErr == nil {
		available.Availability = mergedAvailability(reg, reports)
	}
	return available
}

// servesFromReport is whether one account's own catalog serves a model. A model the driver has
// no mapping for is left to the registry (servable here).
func servesFromReport(reg *models.ModelRegistry, driver string, report registry.ProviderAvailability) func(string) bool {
	return func(modelID string) bool {
		def, ok := reg.GetDefinition(modelID)
		if !ok {
			return true
		}
		for _, p := range def.Providers {
			if p.Driver == driver {
				return !report.For(p.APIModel).Disabled
			}
		}
		return true
	}
}

// mergedAvailability is registry.BuildAvailabilityFunc over several accounts per driver: a model
// is servable when ANY account's report serves it, and then carries that account's limits. A
// driver without a report, and a model without a mapping on that driver, is servable.
func mergedAvailability(reg *models.ModelRegistry, reports map[string][]registry.ProviderAvailability) models.AvailabilityFunc {
	if len(reports) == 0 {
		return nil
	}
	return func(driver, modelID string) models.ModelAvailability {
		list, ok := reports[driver]
		if !ok {
			return models.ModelAvailability{}
		}
		def, ok := reg.GetDefinition(modelID)
		if !ok {
			return models.ModelAvailability{}
		}
		for _, p := range def.Providers {
			if p.Driver != driver {
				continue
			}
			first := list[0].For(p.APIModel)
			for _, report := range list {
				if a := report.For(p.APIModel); !a.Disabled {
					return a
				}
			}
			return first
		}
		return models.ModelAvailability{}
	}
}

// hasRegisteredDriver reports whether a stored provider id has a registered
// driver factory.
func hasRegisteredDriver(providerID string) bool {
	_, ok := registry.GetDriverFactory(models.Family(providerID))
	return ok
}

// HasStaleReliantKey reports whether the user has a stored `reliant` provider
// key that the gateway would reject (not an rlat_ access token). It is the
// condition under which BuildAvailableDrivers silently drops the provider.
func HasStaleReliantKey(ctx context.Context, repo db.Repository, userID string) (bool, error) {
	key, err := managedReliantKey(ctx, repo, userID)
	if err != nil {
		return false, err
	}
	return key != "" && key != "dummy" && !IsReliantLLMKey(key), nil
}

// managedReliantKey is the user's stored reliant provider key ("" when they have none).
func managedReliantKey(ctx context.Context, repo interface {
	ListProviderAccountsWithSecrets(ctx context.Context, userID string) ([]core.AccountWithSecrets, error)
}, userID string) (string, error) {
	if s, ok := repo.(legacySyncer); ok {
		if err := s.SyncLegacyCredentials(ctx, userID); err != nil {
			logging.Warn("Could not level provider accounts with the legacy credential tables; using them as they are",
				"user_id", userID, "error", err)
		}
	}
	accounts, err := repo.ListProviderAccountsWithSecrets(ctx, userID)
	if err != nil {
		return "", err
	}
	for _, a := range accounts {
		if a.Provider == "reliant" {
			return strings.TrimSpace(a.Secrets.Secret), nil
		}
	}
	return "", nil
}

// NewLocalDriver builds a driver for a local model synthesized for this
// request. Local models are never in the registry or DriverMapping (they
// belong to a user's daemons), so GetDriverForModel's CanDriverUseModel gate
// does not apply; the caller supplies the relay via llm.WithTransport.
func NewLocalDriver(model models.Model, opts ...llm.DriverOption) (llm.Driver, error) {
	options := llm.DriverOptions{}
	for _, o := range opts {
		o(&options)
	}
	options.Model = model
	return &baseDriver{client: local.NewClient(options), model: model}, nil
}
