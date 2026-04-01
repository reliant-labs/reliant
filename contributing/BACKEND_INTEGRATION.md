# Backend Integration Plan: Product Events → GTM Pipeline

## Problem

The Reliant Go backend sends analytics events to Statsig only. The GTM pipeline (Attio CRM, Customer.io emails, Slack notifications) has no visibility into what users do in the product. All the CRM fields for usage metrics, activation status, and PQL scores are empty.

## Solution

Add a second event sink in the Go backend that writes authenticated lifecycle events to Supabase `REST /rest/v1/events` (anon key + user JWT).

Only authenticated data is sent to GTM. Pre-sign-in events (like app opens) stay in Statsig and are not replicated to this backend pipeline.

```
Today:
  Go backend → Statsig API (only)

After:
  Go backend → Statsig API (unchanged)
             → Supabase REST API (authenticated path, opt-in)
                  ↓
             aggregate-usage cron (daily)
                  ↓
             Attio + Customer.io
```

## Why Supabase REST API with anon key

- **Desktop app** — can't ship service role keys to user machines
- The app already has `SUPABASE_URL` and `SUPABASE_ANON_KEY` (shipped in the Electron build)
- Users already have JWTs from Supabase Auth login
- RLS policies scope writes to `user_id = auth.uid()` — users can only insert their own events
- A `BEFORE INSERT` trigger auto-resolves `identity_id` from the user's identity row
- **Opt-in** — if the Supabase URL/key aren't configured, the analytics client skips GTM writes silently

## Architecture

```
Desktop App (Electron + Go backend)
  │
  │  POST /rest/v1/events
  │  Headers:
  │    apikey: <SUPABASE_ANON_KEY>
  │    Authorization: Bearer <user-jwt>
  │
  ▼
Supabase PostgREST + RLS
  │  user_id = auth.uid() enforced
  ▼
events table
  │
  │  Daily cron (aggregate-usage edge function)
  ▼
Attio (CRM) + Customer.io (lifecycle emails)
```

## Database changes (this repo — already done)

Migration `20250219000001_events_user_id_and_rls.sql`:

1. **`user_id` column** on `events` table — FK to `auth.users(id)`, indexed
2. **`resolve_event_identity()` trigger** — `BEFORE INSERT`, auto-sets `identity_id` by looking up the user's identity row (created by the auth signup trigger in migration 002)
3. **RLS policies**:
   - `events` INSERT: `user_id = auth.uid()` (users can only write their own events)
   - `events` SELECT: `user_id = auth.uid()` (users can read their own, for client-side dedup)
   - `identities` SELECT: `supabase_user_id = auth.uid()` (users can read their own identity)
   - Service role (edge functions, cron) bypasses RLS automatically
4. **`payload` nullable** — desktop app events don't always need a payload

These deploy automatically via the `deploy-migrations.yml` GitHub Actions workflow on push to main.

## What changes in the Reliant repo

All changes are in `<your-project-path>`. The design is **opt-in** — nothing breaks if GTM isn't configured.

### 1. Add GTM event sink to analytics client

**File:** `internal/analytics/client.go`

Add fields to the `Client` struct:

```go
// GTM event sink (opt-in, configured via env vars)
gtmURL    string // SUPABASE_URL e.g. https://xxx.supabase.co
gtmAPIKey string // SUPABASE_ANON_KEY (public, safe to ship)
```

Initialize from env vars. If not set, GTM is disabled:

```go
// In NewClientFromSettings or InitializeWithUserID:
client.gtmURL = os.Getenv("SUPABASE_URL")
client.gtmAPIKey = os.Getenv("SUPABASE_ANON_KEY")
// That's it. If empty, writeToGTM is a no-op.
```

Note: these are the same env vars the frontend already uses (`VITE_SUPABASE_URL` and `VITE_SUPABASE_ANON_KEY`). The Go backend can read them without the `VITE_` prefix, or reuse the same values from the Electron main process.

### 2. Store the user JWT alongside user ID

**File:** `internal/analytics/client.go`

The analytics client currently stores `userID` (set via `SetUserID`). Also store the raw JWT so we can use it for authenticated REST API calls:

```go
// Add to Client struct:
userJWT string

// New method (called from auth middleware alongside SetUserID):
func (c *Client) SetUserJWT(token string) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.userJWT = token
}
```

**File:** `internal/auth/middleware.go` (line ~138)

```go
// Existing:
analytics.SetUserID(claims.Sub)
// Add:
analytics.SetUserJWT(rawToken)
```

**File:** `internal/grpc/interceptors/auth.go` (line ~201)

Same — pass the raw token to analytics alongside the user ID.

### 3. Write qualifying events to Supabase

**File:** `internal/analytics/client.go`

In `sendEvents()` (line ~728), after the Statsig HTTP call:

```go
if c.gtmURL != "" && c.gtmAPIKey != "" {
    c.writeToGTM(events)
}
```

The `writeToGTM` function:

```go
var gtmEventTypes = map[string]bool{
    "session_start":             true,
    "message_sent":              true,
    "workflow_started":           true,
    "workflow_completed":         true,
    "workflow_draft_created":     true,
    "project_opened":            true,
    "api_key_configured":        true,
    "first_message_sent":        true,
    "onboarding_completed":      true,
    "onboarding_skipped":        true,
    "llm_call_completed":        true,
    "provider_settings_updated": true,
}

func (c *Client) writeToGTM(events []Event) {
    c.mu.RLock()
    jwt := c.userJWT
    userID := c.userID
    c.mu.RUnlock()

    // Need both user ID and JWT for authenticated writes
    if jwt == "" || userID == "" {
        return
    }

    for _, e := range events {
        if !gtmEventTypes[e.eventName] {
            continue
        }

        idempotencyKey := fmt.Sprintf("app:%s:%s:%d", e.eventName, userID, e.time)

        body, err := json.Marshal(map[string]interface{}{
            "idempotency_key": idempotencyKey,
            "source":          "app",
            "event_type":      e.eventName,
            "user_id":         userID,
            "payload":         e.metadata,
        })
        if err != nil {
            continue
        }

        req, err := http.NewRequest("POST",
            c.gtmURL+"/rest/v1/events",
            bytes.NewReader(body))
        if err != nil {
            continue
        }

        req.Header.Set("apikey", c.gtmAPIKey)
        req.Header.Set("Authorization", "Bearer "+jwt)
        req.Header.Set("Content-Type", "application/json")
        req.Header.Set("Prefer", "return=minimal")

        resp, err := c.httpClient.Do(req)
        if err != nil {
            // Non-fatal: Statsig delivery is unaffected
            continue
        }
        resp.Body.Close()

        // 409 = conflict on idempotency_key = already exists = fine
        if resp.StatusCode != 201 && resp.StatusCode != 409 {
            log.Printf("GTM event write failed (non-fatal): %s %d",
                e.eventName, resp.StatusCode)
        }
    }
}
```

Key design decisions:
- **Non-blocking** — failures are logged and skipped, never affect Statsig
- **Idempotent** — `idempotency_key` has a UNIQUE constraint, duplicates return 409 (ignored)
- **No new dependencies** — uses the existing `net/http` client
- **No batching needed** — PostgREST accepts individual inserts, and the analytics client already batches flushes every 30s
- **RLS enforced** — the JWT ensures `user_id = auth.uid()`, PostgREST rejects mismatches

### 4. Environment variables

The Go backend needs these:

```bash
# Already shipped in the Electron build for the frontend:
SUPABASE_URL=https://dash.reliantlabs.io
SUPABASE_ANON_KEY=<your-supabase-anon-key>

```

Notes:
- No service-role key is needed in the app.
- If Supabase URL/key are missing, GTM writes are skipped.
- If no user JWT is present (pre-login), do not send the event to GTM; keep it in Statsig only.

### 5. Graceful handling

```go
func (c *Client) gtmEnabled() bool {
    return c.gtmURL != "" && c.gtmAPIKey != ""
}
```

All GTM code paths check this first. If not configured:
- No HTTP connections opened
- No goroutines spawned
- No log output
- Zero performance impact

## Events that flow to GTM

| Event | Why it matters for GTM |
|---|---|
| `session_start` | Session count, last_seen_at, WAU/MAU |
| `message_sent` | Usage volume, engagement scoring |
| `workflow_started` | Workflow adoption |
| `workflow_completed` | Success rate, duration |
| `workflow_draft_created` | Custom workflow builder adoption |
| `project_opened` | Active project count |
| `api_key_configured` | Activation milestone |
| `first_message_sent` | Activation milestone |
| `onboarding_completed` | Activation milestone |
| `onboarding_skipped` | Activation milestone |
| `llm_call_completed` | Provider/model preference, token usage |
| `provider_settings_updated` | Provider adoption |

Events NOT sent (noise for CRM):
- `page_visited`, `preferences_updated`, `onboarding_started`, `onboarding_step_*`, `workflow_draft_saved`

## Data flow end-to-end

```
1. User signs up via Supabase Auth
   → Auth trigger (migration 002) creates identity row
   → identity.supabase_user_id = auth.users.id

2. User uses the product
   → Go backend logs events to Statsig (existing, unchanged)
   → Go backend POSTs to Supabase REST API (new, opt-in)
   → RLS: user_id = auth.uid() ✓
   → Trigger: identity_id auto-resolved from supabase_user_id
   → Event lands in events table

3. Daily cron (aggregate-usage edge function)
   → Queries events table per identity
   → Computes: total_sessions, messages_last_7d, activation_status, pql_score
   → Pushes traits to Attio (upsert person) and Customer.io (identify)

4. Customer.io triggers lifecycle emails
   → signed_up_no_api_key → setup-api-key email
   → api_key_no_first_message → first-message-nudge email
   → at_risk_churn → we-miss-you email

5. Attio shows enriched contact
   → Sales sees: activation_status=active_user, pql_score=78, messages_last_7d=15
```

## Identity linking

```
auth.users.id (UUID)
    ↓ auth trigger (migration 002) — ON CONFLICT merges with rb2b identity
identities.supabase_user_id
    ↓ event insert trigger (migration 003) — auto-resolves
events.identity_id
    ↓ aggregate-usage cron
Attio person + Customer.io person
```

If rb2b identified someone by email before they signed up, the auth trigger merges via `ON CONFLICT (email)`. Their rb2b-created CRM contact gets enriched with product data automatically.

## Rollout

### Phase 1: Deploy GTM infrastructure (this repo)
```bash
# Migrations auto-deploy on push to main via deploy-migrations.yml
# Or manually:
supabase link --project-ref <ref>
supabase db push

# Deploy edge functions
supabase functions deploy

# Apply CRM config
npm run apply:attio
npm run apply:customerio

# Aggregation schedule is managed by GitHub Actions (single source of truth)
# See: .github/workflows/run-aggregation.yml
# - Daily at 06:00 UTC
# - Manually triggerable via workflow_dispatch
```

### Phase 2: Backend changes (reliant repo)
1. Add `gtmURL`, `gtmAPIKey`, `userJWT` fields to analytics client
2. Add `writeToGTM()` function
3. Pass JWT from auth middleware to analytics client
4. Deploy — events start flowing automatically for authenticated users
5. No new env vars needed (reuses existing Supabase URL/key)

### Phase 3: Continuous reconciliation (no one-time backfill required)

Instead of relying on a one-time manual SQL backfill, this repo now includes
an intent-driven reconciliation function deployed via migration:

- `public.reconcile_identities_from_auth()`

The `aggregate-usage` function invokes this on every run before processing identities,
so state continuously converges toward:

- every `auth.users` row has a linked `identities.supabase_user_id`
- pre-trigger legacy users are healed automatically
- future drift is corrected without manual intervention

This means existing users and new signups are both handled in the same ongoing loop.