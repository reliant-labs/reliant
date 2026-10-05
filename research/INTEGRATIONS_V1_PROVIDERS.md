# Integrations v1: provider reference

Companion to `research/INTEGRATIONS_V1_BRIEF.md`. This is the single source of
provider facts for the wave-1 manifest and trigger agents: auth, the listed
actions, the listed triggers, operator app registration, and gotchas. Build
against `httptest` fakes that reproduce what is written here.

Researched 2026-10. Items marked **UNVERIFIED** come from prior knowledge or
SDK convention and were not confirmed against a fetched official page. Check
them before relying on them in a security path.

Reliant URLs used below:

- OAuth callback: `<PUBLIC_URL>/integrations/oauth/<provider>/callback`
- App-level events: `<PUBLIC_URL>/integrations/<provider>/events`

---

## 0. Cross-provider summary

| Provider | Auth | Trigger mechanism | Signature | Dedupe key | Route to account by | Ack budget |
|---|---|---|---|---|---|---|
| GitHub | GitHub App user token, brokered by CP (`delegated`) | app-level webhook | `X-Hub-Signature-256`: HMAC-SHA256(body), hex, `sha256=` prefix | `X-GitHub-Delivery` | `installation.id` | 10 s |
| Slack | OAuth v2 bot token | app-level Events API | `X-Slack-Signature`: `v0=` HMAC-SHA256(`v0:{ts}:{body}`); 5 min window | `event_id` | `team_id` (+ `api_app_id`) | 3 s |
| Gmail | Google OAuth2 (offline) | poll `users.history.list` | n/a | `gmail:<connection>:<message id>` | connection (poll is per connection) | n/a |
| Twilio | Basic (Account SID + Auth Token, or API Key SID + secret) | per-number webhook | `X-Twilio-Signature`: base64 HMAC-SHA1(full URL + sorted params) | `MessageSid` | `AccountSid`, then `To` | 15 s (UNVERIFIED) |
| iMessage | none (no API) | daemon-local only | n/a | n/a | n/a | n/a |
| Zapier | none / bearer header | generic webhook `/hooks/{id}/{token}` | bearer or custom header | caller-supplied, else body hash | trigger id in URL | Zapier waits ~30 s (UNVERIFIED) |

**Constraints that affect the brief's plan:**

1. **Gmail read needs a RESTRICTED scope.** `messages.list`/`get` and
   `history.list` require `gmail.readonly` (or `gmail.metadata` /
   `gmail.modify`, all restricted). Production use beyond test users needs
   Google verification plus a CASA security assessment. Until then the app is
   stuck in "Testing": max 100 test users, and **refresh tokens expire after
   7 days**, so every Gmail connection silently dies weekly. The UI needs a
   "reconnect" state that is normal, not exceptional.
2. **GitHub does not retry failed webhook deliveries.** A non-2xx or a >10 s
   response is lost unless we redeliver via the REST API. Receivers must
   ack fast, and stream B should consider a reconcile job (list failed
   deliveries for the App and redeliver).
3. **Twilio signatures cover the exact public URL.** Behind the ingress, the
   receiver must rebuild the URL from `PUBLIC_URL` and the request's path and
   query, not from `r.Host`/`r.URL`. Twilio also signs inconsistently with and
   without the default port, so the validator must try both.
4. **A Twilio webhook URL is per phone number, not per app.** There is no
   provider-side "app": the operator (or the connection flow) must set each
   number's inbound URL. Routing is `AccountSid` then `To`.
5. **Slack bot events only cover channels the bot is in.** `message.channels`
   fires only for public channels the bot has joined; users must
   `/invite @app`.

---

## 1. GitHub

The App already exists (slug `reliant-labs`, control-plane owns tokens and the
webhook secret). **Do not register another App.** Reliant gets a user's
current token from CP via the new internal RPC (stream D) and declares the
connection as `type: delegated`.

### 1.1 Auth

| Item | Value |
|---|---|
| Type | GitHub App user-to-server token (owned by CP) |
| Authorize | `https://github.com/login/oauth/authorize` (CP's flow, not reliant's) |
| Token / refresh | `POST https://github.com/login/oauth/access_token` (`grant_type=refresh_token`) |
| PKCE | Supported, `S256` only (`plain` rejected) |
| Lifetimes | Expiring tokens: access token 8 h, refresh token 6 months. Refresh rotates both. If the refresh token expires, the user must re-authorize. |
| Scopes | None. A GitHub App user token has the App's permissions intersected with what the user can access. Needed: Issues (write), Pull requests (write), Contents (read) for push metadata, Metadata (read). The App already has these. |
| Identity | `GET https://api.github.com/user` → label `login`, external id `id` (numeric; store as string) |

Access is the intersection of the repos the App is installed on and the repos
the user can see. If the App is not installed on a repo, actions fail with 404
even though the user can see the repo.

**Mapping installations to user connections:** `GET /user/installations`
(user token) lists installations the user can access, as `installations[].id`
and `installations[].account.login`. To check a repo, call
`GET /user/installations/{installation_id}/repositories`. Store the
installation ids on the connection, and refresh them on connect and
periodically, or on an `installation`/`installation_repositories` webhook. A
webhook with `installation.id = X` then fans out to triggers whose owner's
GitHub connection includes X. **One installation (an org) maps to many
users.** Trigger filters must also scope by repository (`repository.full_name`)
so user A's trigger does not fire on a repo only user B can see. Re-check
access with the user's token (repo GET) before launching if strictness
matters.

### 1.2 Actions

Base `https://api.github.com`. Headers: `Accept: application/vnd.github+json`,
`X-GitHub-Api-Version: 2022-11-28`, `Authorization: Bearer <token>`,
`User-Agent` (required).

| Action | Method + path | Required | Body | Output fields |
|---|---|---|---|---|
| issue create | `POST /repos/{owner}/{repo}/issues` | owner, repo, title | `{title, body?, labels?[], assignees?[], milestone?}` | `number`, `html_url`, `id`, `state` |
| issue comment | `POST /repos/{owner}/{repo}/issues/{issue_number}/comments` | owner, repo, issue_number, body | `{body}` | `id`, `html_url`, `created_at` |
| PR comment | same endpoint as issue comment (PRs are issues), with `issue_number` = PR number | same | `{body}` | same |
| PR get | `GET /repos/{owner}/{repo}/pulls/{pull_number}` | owner, repo, pull_number | n/a | `number`, `title`, `state`, `merged`, `draft`, `html_url`, `head.ref`, `head.sha`, `base.ref`, `user.login`, `body` |

A review comment on a diff line is a separate API
(`/pulls/{n}/comments`, needs `commit_id`, `path`, `line`). Out of scope;
"PR comment" means the conversation comment above.

**Errors and rate limits:** 401 bad token, 403/404 no access (GitHub returns
404 for private repos you cannot see), 410 issues disabled, 422 validation
(`errors[]`). Primary limit: `x-ratelimit-remaining` /
`x-ratelimit-reset` (epoch seconds); exhausted gives 403 or 429. Secondary
limits give 403 or 429, sometimes with `retry-after` (seconds). Back off on
`retry-after` if present, else until `x-ratelimit-reset`, else at least 60 s.

### 1.3 Triggers (app-level webhook)

Received by CP's App webhook config today. Either CP forwards to reliant, or
the App's webhook URL points at `<PUBLIC_URL>/integrations/github/events`.
**Decide in stream B/D. There is only one webhook URL per App.**

| Item | Value |
|---|---|
| Event header | `X-GitHub-Event` (`issues`, `issue_comment`, `pull_request`, `push`, `ping`) |
| Signature | `X-Hub-Signature-256: sha256=<hex HMAC-SHA256(secret, raw body)>`. Constant-time compare over the raw bytes. No timestamp, so there is no replay window: dedupe is the replay defence. |
| Dedupe | `X-GitHub-Delivery` (GUID). **Redeliveries reuse the same GUID.** |
| Routing | `installation.id` in the body; also `repository.full_name`, `sender.login` |
| Retries | **None automatic.** Redeliver manually or via `POST /app/hook/deliveries/{id}/attempts` (App JWT). |
| Timeout | Respond 2xx within 10 s. |
| Handshake | A `ping` event on webhook creation; reply 2xx. |

Event/action filters:

| Trigger | `X-GitHub-Event` | `action` |
|---|---|---|
| issue opened | `issues` | `opened` |
| issue labeled | `issues` | `labeled` (the added label is in `label.name`) |
| comment created | `issue_comment` | `created` (`issue.pull_request` present means a PR) |
| PR opened/updated | `pull_request` | `opened`, `synchronize` (new commits pushed) |
| push | `push` | (no action; `ref`, `before`, `after`, `commits[]`) |

Trimmed example (`issues`/`opened`):

```json
{
  "action": "opened",
  "issue": {"number": 42, "title": "Crash on save", "body": "...",
            "html_url": "https://github.com/acme/app/issues/42",
            "user": {"login": "octocat"}, "labels": []},
  "repository": {"id": 123, "full_name": "acme/app", "private": true},
  "sender": {"login": "octocat", "id": 1},
  "installation": {"id": 98765}
}
```

`push` trimmed: `{"ref":"refs/heads/main","before":"…","after":"…","commits":[{"id":"…","message":"…","author":{"name":"…"}}],"pusher":{"name":"…"},"repository":{…},"installation":{"id":98765}}`.
`commits[]` is capped at 20 per payload; use the compare API for more.

### 1.4 Operator registration

Nothing new. If reliant receives events directly, change the existing App's
Webhook URL to `<PUBLIC_URL>/integrations/github/events` and share its webhook
secret with reliant as a deployment secret. Otherwise CP forwards. Confirm the
event subscriptions include issues, issue_comment, pull_request and push (the
brief says they do).

### 1.5 Gotchas

- Events arrive for every installation, including users with no trigger:
  drop them cheaply before writing a row.
- Bot loops: our own comments produce `issue_comment` events. Filter
  `sender.type == "Bot"` or our App's bot login (`reliant-labs[bot]`).
- `issues` events also fire for PRs in some cases (UNVERIFIED); check for
  `issue.pull_request`.
- Payloads up to 25 MB; size-cap before storing.

Sources: docs.github.com
`/webhooks/using-webhooks/validating-webhook-deliveries`,
`/webhooks/using-webhooks/best-practices-for-using-webhooks`,
`/webhooks/using-webhooks/handling-failed-webhook-deliveries`,
`/apps/creating-github-apps/authenticating-with-a-github-app/refreshing-user-access-tokens`,
`/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app`,
`/rest/issues/issues`, `/rest/issues/comments`, `/rest/pulls/pulls`,
`/rest/apps/installations`, `/rest/using-the-rest-api/rate-limits-for-the-rest-api`.

---

## 2. Slack

### 2.1 Auth

| Item | Value |
|---|---|
| Type | OAuth v2, bot token (`xoxb-`) |
| Authorize | `https://slack.com/oauth/v2/authorize?client_id&scope=<bot scopes, comma-sep>&redirect_uri&state` (`user_scope` for user-token scopes; not needed) |
| Token | `POST https://slack.com/api/oauth.v2.access` (form: `code`, `redirect_uri`, client id/secret via basic auth or form) |
| PKCE | Slack documents PKCE ("Using PKCE" page). Mainly for public clients; we are confidential, so it is optional. Exact params UNVERIFIED. |
| Refresh | Bot tokens **do not expire** unless the app opts into token rotation. With rotation: 12 h access token plus refresh token (UNVERIFIED lifetime). Recommend no rotation for v1. |
| Identity | `oauth.v2.access` response: external id `team.id`, label `team.name`; also `bot_user_id`, `app_id`, `authed_user.id`. Or `POST https://slack.com/api/auth.test` → `team_id`, `team`, `user_id`, `url`. |

Minimal bot scopes:

| Need | Scope |
|---|---|
| chat.postMessage / thread reply | `chat:write` (`chat:write.public` to post in public channels without joining) |
| users.lookupByEmail | `users:read.email` (and `users:read`) |
| `message.channels` event | `channels:history` |
| `app_mention` event | `app_mentions:read` |
| `reaction_added` event | `reactions:read` |

Slack has no Google-style sensitive/restricted tiers. Marketplace review
applies only if listed publicly.

**Connection model:** a Slack connection is per workspace (`team_id`), but
the brief says connections are per user. Two reliant users in one workspace
installing the app each receive a bot token for the same `team_id` (the bot
token is reused). The `team_id` → trigger fan-out must therefore match on
owner connections' `team_id` plus each trigger's channel filter.

### 2.2 Actions

All are `POST https://slack.com/api/<method>`,
`Authorization: Bearer xoxb-…`, with `Content-Type: application/json; charset=utf-8`
(lookupByEmail is form/GET).

| Action | Method | Required | Body | Output |
|---|---|---|---|---|
| post message | `chat.postMessage` | `channel` (id), `text` or `blocks` | `{channel, text, blocks?, unfurl_links?}` | `ts`, `channel`, `message.text` |
| reply in thread | `chat.postMessage` | `channel`, `thread_ts`, `text` | `{channel, thread_ts, text, reply_broadcast?}` | `ts`, `channel` |
| lookup user | `users.lookupByEmail` | `email` | form or GET query `email=` | `user.id`, `user.name`, `user.real_name`, `user.profile.display_name` |

**Errors:** Slack returns **HTTP 200 with `{"ok":false,"error":"<code>"}`**
for most failures. The runner must treat `ok:false` as failure. Common codes:
`channel_not_found`, `not_in_channel`, `invalid_auth`, `token_revoked`,
`account_inactive`, `missing_scope` (with `needed`), `users_not_found`,
`ratelimited`. A rate limit is **HTTP 429 plus `Retry-After` (seconds)**.
`chat.postMessage` is the special tier: about 1 msg/s per channel plus a
workspace limit. `users.lookupByEmail` is Tier 3 (50+/min, UNVERIFIED tier).
Successful responses may include `warning` and `response_metadata.warnings`.

### 2.3 Triggers (Events API, app-level)

Request URL `<PUBLIC_URL>/integrations/slack/events`, JSON POST.

| Item | Value |
|---|---|
| Signature | `X-Slack-Signature: v0=<hex HMAC-SHA256(signing_secret, "v0:" + X-Slack-Request-Timestamp + ":" + raw_body)>` |
| Replay window | Reject if `abs(now - X-Slack-Request-Timestamp) > 300 s` |
| URL handshake | Body `{"type":"url_verification","challenge":"…","token":"…"}` → respond 200 with the `challenge` value (plain text, or JSON `{"challenge":…}`). It is signed too; verify first. |
| Envelope | `type: "event_callback"`, `team_id`, `api_app_id`, `event_id`, `event_time`, `event{…}`, `authorizations[]` |
| Dedupe | `event_id` (stable across retries) |
| Routing | `team_id` (with `api_app_id` = our app); `event.channel` for the channel filter |
| Ack | 2xx within **3 s** |
| Retries | Up to 3: near-immediate, then after 1 min, then after 5 min. Headers `X-Slack-Retry-Num` (1–3) and `X-Slack-Retry-Reason` (e.g. `http_timeout`). Reply with header `X-Slack-No-Retry: 1` to suppress. |
| Disablement | If more than 95% of deliveries fail within 60 min, subscriptions are disabled (apps under 1,000 events/h are exempt from auto-disable). |
| Volume | 30,000 events per workspace per app per 60 min |

Inner events:

| Trigger | `event.type` | Notes |
|---|---|---|
| channel message | `message` (subscription `message.channels`) | Ignore `subtype` (`bot_message`, `message_changed`, `message_deleted`, `channel_join`…) and any `bot_id` to avoid loops. `thread_ts` is present for replies. |
| mention | `app_mention` | `text` includes `<@BOTID>`. Also delivered as `message` if subscribed: dedupe by `channel`+`ts` across types. |
| reaction | `reaction_added` | `reaction`, `user`, `item{type,channel,ts}`, `item_user` |

Trimmed example:

```json
{
  "token": "deprecated",
  "team_id": "T123ABC456",
  "api_app_id": "A123ABC456",
  "type": "event_callback",
  "event_id": "Ev123ABC456",
  "event_time": 1700000000,
  "event": {
    "type": "app_mention",
    "user": "U123ABC456",
    "text": "<@U0BOT> summarize this",
    "ts": "1700000000.000100",
    "channel": "C123ABC456",
    "event_ts": "1700000000.000100"
  },
  "authorizations": [{"team_id": "T123ABC456", "user_id": "U0BOT", "is_bot": true}]
}
```

### 2.4 Operator registration

Create one app at https://api.slack.com/apps (from a manifest):

```yaml
display_information: {name: Reliant}
features:
  bot_user: {display_name: Reliant, always_online: false}
oauth_config:
  redirect_urls: ["<PUBLIC_URL>/integrations/oauth/slack/callback"]
  scopes:
    bot: [chat:write, chat:write.public, users:read, users:read.email,
          channels:history, app_mentions:read, reactions:read]
settings:
  event_subscriptions:
    request_url: "<PUBLIC_URL>/integrations/slack/events"
    bot_events: [message.channels, app_mention, reaction_added]
  org_deploy_enabled: false
  socket_mode_enabled: false
  token_rotation_enabled: false
```

Then copy the Client ID and Client Secret to
`RELIANT_OAUTH_SLACK_CLIENT_ID/_SECRET` and the **Signing Secret** to a new
deployment secret for the receiver. Saving `request_url` triggers the
`url_verification` handshake, so the receiver must be live first. Enable
"Manage Distribution" to install into workspaces other than the dev one.

### 2.5 Gotchas

- Do all work asynchronously: verify, write the row, return 200. Never call
  Slack inline.
- Verify signatures over the raw body bytes before any JSON parsing.
- `channel` in actions must be an id (`C…`), not `#name`.
- Our own posts come back as `message` events with `bot_id`: filter them.
- Revocation: `tokens_revoked` / `app_uninstalled` events (subscribe later)
  mark the connection dead.

Sources: https://docs.slack.dev/authentication/verifying-requests-from-slack,
https://docs.slack.dev/apis/events-api/,
https://docs.slack.dev/authentication/installing-with-oauth,
https://docs.slack.dev/apis/web-api/rate-limits,
https://api.slack.com/methods/chat.postMessage,
https://api.slack.com/methods/users.lookupByEmail.

---

## 3. Gmail

### 3.1 Auth

| Item | Value |
|---|---|
| Type | Google OAuth 2.0, authorization code, offline |
| Authorize | `https://accounts.google.com/o/oauth2/v2/auth` with `access_type=offline`, `prompt=consent` (needed to get a refresh token again on reconnect), `include_granted_scopes=true` |
| Token | `POST https://oauth2.googleapis.com/token` |
| PKCE | Supported (`S256`) |
| Lifetimes | Access token about 1 h (`expires_in`). Refresh token: long-lived in production, **7 days when the consent screen is External + "Testing"** (unless scopes are only openid/email/profile). Limit of 100 refresh tokens per Google account per client: older ones are silently invalidated. `invalid_grant` on refresh means reconnect. |
| Identity | `GET https://gmail.googleapis.com/gmail/v1/users/me/profile` → label `emailAddress`, also `historyId` (useful for the baseline). For a stable id, add `openid email` and use `GET https://openidconnect.googleapis.com/v1/userinfo` → `sub` (id), `email` (label). |

Scopes (Google classification):

| Scope | Class | Gives |
|---|---|---|
| `https://www.googleapis.com/auth/gmail.send` | **Sensitive** | messages.send only |
| `https://www.googleapis.com/auth/gmail.readonly` | **Restricted** | list/get/history |
| `https://www.googleapis.com/auth/gmail.metadata` | Restricted | headers only, no body; `q` search not allowed |
| `https://www.googleapis.com/auth/gmail.modify` | Restricted | read + send + labels |
| `https://mail.google.com/` | Restricted | everything incl. permanent delete |
| `openid`, `email` | Non-sensitive | identity |

**Minimal set: `gmail.send` + `gmail.readonly` + `openid email`.** Reading is
unavoidably restricted, so:

- **Testing** (External user type): up to 100 test users listed on the
  consent screen, an "unverified app" warning, and 7-day refresh tokens.
- **Production** with restricted scopes: brand plus scope verification, and a
  **CASA security assessment** (third-party, annual), before non-test users
  can authorize. Until verified, a lifetime user cap applies for unapproved
  sensitive/restricted scopes (it cannot be reset).
- **Internal** user type (Workspace org only): no verification and no 7-day
  expiry, but only that org's users. Good for dogfooding on the reliant
  Workspace.

Option for v1: ship send-only (sensitive, verification without CASA) and gate
the read trigger behind testing/internal. Flag this to the user.

### 3.2 Actions

Base `https://gmail.googleapis.com/gmail/v1/users/me`.

| Action | Method + path | Required | Body / query | Output |
|---|---|---|---|---|
| send | `POST /messages/send` | `raw` | `{"raw": base64url(RFC 2822 message), "threadId"?: "…"}` | `id`, `threadId`, `labelIds` |
| list | `GET /messages` | none | `q` (Gmail search syntax), `maxResults` (default 100, max 500), `pageToken`, `labelIds`, `includeSpamTrash` | `messages[]{id,threadId}`, `nextPageToken`, `resultSizeEstimate` |
| get | `GET /messages/{id}` | `id` | `format=full\|metadata\|minimal\|raw`, `metadataHeaders=From&metadataHeaders=Subject` | `id`, `threadId`, `labelIds`, `snippet`, `internalDate`, `payload.headers[]`, `payload.parts[]` (body base64url in `body.data`) |

**Send is a Go executor (`go:gmail_send`):** build the MIME (From is optional;
Gmail uses the account address), `To`, `Subject` (RFC 2047-encode non-ASCII),
`Content-Type: text/plain; charset=UTF-8`, and for replies `In-Reply-To` /
`References` plus `threadId`. Encode with **base64url, no padding is
accepted** (`RawURLEncoding` works). Get should also be Go, or post-processed,
to decode the body part and flatten headers into `from`/`to`/`subject`/`date`/`text`.

**Errors:** Google JSON `{"error":{"code","message","status","errors":[{"reason"}]}}`.
401 means refresh. 403 `insufficientPermissions` (scope missing), 403
`rateLimitExceeded` / `userRateLimitExceeded`, 429 `RESOURCE_EXHAUSTED`, 400
`invalidArgument` (bad raw), 404 not found / stale historyId, 500/503 retry.
Back off exponentially; `Retry-After` is not reliably sent (UNVERIFIED).
Quota: 6,000 units/min per user per project; 1,200,000/min per project.
Per-method costs (UNVERIFIED, from the quota table): send 100, list 5, get 5,
history.list 2.

### 3.3 Trigger: new email (poll)

| Step | Call |
|---|---|
| Baseline (first poll, fire nothing) | `GET /users/me/profile` → store `historyId` as the cursor |
| Poll | `GET /users/me/history?startHistoryId=<cursor>&historyTypes=messageAdded&labelId=INBOX&maxResults=500` (default page 100, max 500); follow `nextPageToken` |
| Advance | Set the cursor to the response's top-level `historyId` after all pages succeed |
| Event | For each `history[].messagesAdded[].message` (`id`, `threadId`, `labelIds`), skip ones with `SENT`/`DRAFT` labels, then `messages.get?format=metadata` to build the payload |
| Dedupe | `gmail:<connection_id>:<message.id>` (history can repeat ids across pages/polls) |
| Expired cursor | **HTTP 404** when `startHistoryId` is too old (records are kept "at least one week", sometimes much less). Re-baseline from `profile.historyId` and record a gap. Do not replay. |
| Empty | A response with no `history` field means no changes (still update `historyId`) |

Payload to emit (our shape, trimmed):

```json
{"message_id":"18c1…","thread_id":"18c1…","from":"Ann <ann@x.com>",
 "to":"me@y.com","subject":"Invoice","date":"Tue, 3 Oct 2026 10:00:00 +0000",
 "snippet":"Please find…","label_ids":["INBOX","UNREAD"]}
```

Raw `history.list` response:

```json
{"history":[{"id":"9876","messagesAdded":[{"message":{"id":"18c1","threadId":"18c1","labelIds":["UNREAD","INBOX"]}}]}],
 "historyId":"9880"}
```

**Push (later):** `POST /users/me/watch {topicName:"projects/<p>/topics/<t>", labelIds:["INBOX"]}`.
It needs a Cloud Pub/Sub topic with publish granted to
`gmail-api-push@system.gserviceaccount.com`, a push subscription to our
endpoint (verify the Pub/Sub OIDC JWT), and a `watch` renewal at least every
7 days. The notification only carries `{emailAddress, historyId}`, so you
still call `history.list`. Polling is the right v1.

### 3.4 Operator registration (Google Cloud console)

1. Create or choose a project. Enable the **Gmail API**.
2. OAuth consent screen ("Google Auth Platform → Branding/Audience/Data
   access"): user type External (or Internal for the reliant Workspace only),
   app name, support email, developer contact, and authorized domain = the
   `PUBLIC_URL` domain. Add the scopes above. Publishing status Testing: add
   test users (max 100).
3. Credentials → Create OAuth client ID → **Web application**. Authorized
   redirect URI `<PUBLIC_URL>/integrations/oauth/gmail/callback` (exact match;
   use the provider id the manifest declares). Copy to
   `RELIANT_OAUTH_GMAIL_CLIENT_ID/_SECRET`.
4. For production: submit for verification (homepage, privacy policy, a demo
   video, restricted-scope justification) and complete CASA.

No webhook URL is needed for polling.

### 3.5 Gotchas

- Without `prompt=consent`, a reconnect may return no `refresh_token`.
- Testing mode gives 7-day refresh tokens. Expect `invalid_grant` weekly.
- `internalDate` is ms epoch as a string; `historyId` is a uint64 string.
- Don't poll faster than about 1/min per connection; it is quota-cheap but
  pointless.

Sources: https://developers.google.com/gmail/api/guides/sync,
https://developers.google.com/workspace/gmail/api/auth/scopes,
https://developers.google.com/workspace/gmail/api/reference/quota,
https://developers.google.com/identity/protocols/oauth2 (7-day testing
expiry, 100-token limit), https://support.google.com/cloud/answer/15549945
(100 test users, user cap), https://developers.google.com/gmail/api/reference/rest/v1/users.messages/send,
https://developers.google.com/gmail/api/reference/rest/v1/users.history/list,
https://developers.google.com/gmail/api/guides/push.

---

## 4. Twilio (SMS + WhatsApp)

### 4.1 Auth

| Item | Value |
|---|---|
| Type | HTTP Basic. Username/password is `AccountSid:AuthToken`, or (preferred) `ApiKeySid (SK…):ApiKeySecret` with the Account SID still in the URL path. |
| OAuth | None for this use. |
| Connection params | `account_sid` (non-secret, templated into `base_url`), and optionally a default `from` number |
| Identity | `GET https://api.twilio.com/2010-04-01/Accounts/{AccountSid}.json` → id `sid`, label `friendly_name` |

Manifest: `type: basic` plus `connection_params: [account_sid]`. **Signature
verification needs the Auth Token** (an API key secret does not validate
webhooks, UNVERIFIED but standard). If users connect with an API key, the
inbound trigger needs the account's Auth Token separately. Simplest v1:
collect SID + Auth Token.

### 4.2 Action: send message

`POST https://api.twilio.com/2010-04-01/Accounts/{AccountSid}/Messages.json`,
**form-encoded** (`application/x-www-form-urlencoded`).

| Param | Notes |
|---|---|
| `To` | E.164 (`+15551234567`) or `whatsapp:+15551234567` |
| `From` or `MessagingServiceSid` | One is required. WhatsApp: `whatsapp:+14155238886` (sandbox) or an approved sender |
| `Body` | Up to 1600 chars; or `MediaUrl` |
| `ContentSid` + `ContentVariables` | WhatsApp templates (required outside the 24 h window) |
| `StatusCallback` | Optional delivery-status webhook |

Output: `sid` (SM…/MM…), `status` (`queued`/`accepted`), `to`, `from`,
`date_created`, `error_code`, `error_message`. Response 201.

Errors: JSON `{code, message, more_info, status}`. 400 invalid params (e.g.
21211 invalid To, 21608 unverified number on a trial account, 63016 WhatsApp
outside the 24 h window, UNVERIFIED codes), 401 bad creds, 429 too many
requests (error 20429). Queueing is per sender, so a 429 at the API is rare.

### 4.3 Trigger: inbound message (webhook)

The webhook is configured **per phone number / messaging service / WhatsApp
sender**, as a "A message comes in" URL. Use
`<PUBLIC_URL>/integrations/twilio/events`, HTTP POST, form-encoded.

| Item | Value |
|---|---|
| Signature | `X-Twilio-Signature = base64(HMAC-SHA1(AuthToken, URL + concat(sorted(key)+value for each POST param)))`. The URL is the **full URL exactly as configured, including query string**. Params are sorted by key; for repeated keys, all values in order. |
| Port quirk | Twilio's own SDK validates both with and without the explicit default port (`:443`/`:80`) because signing is inconsistent; do the same. |
| JSON bodies | If the request has a `bodySHA256` query param, the signature is over the URL only and the body must hash to it. Not used for SMS inbound. |
| Replay | No timestamp: dedupe on `MessageSid`. |
| Routing | `AccountSid` → connection(s); `To` → trigger (which number) |
| Response | 200 with empty TwiML `<Response/>` (`Content-Type: text/xml`), or 204. Returning TwiML `<Message>` replies inline; we do not. |
| Timeout / retries | 15 s timeout; no retry by default (connection-override params `#rc=`/`#rt=` can enable). UNVERIFIED. |

Fields: `MessageSid`, `AccountSid`, `MessagingServiceSid`, `From`, `To`,
`Body`, `NumMedia`, `MediaUrl0…`, `MediaContentType0…`, `FromCity`/`FromCountry`
(SMS), and for WhatsApp `ProfileName`, `WaId`, with `From`/`To` prefixed
`whatsapp:`.

Trimmed example (form, shown decoded):

```
MessageSid=SM1234…&AccountSid=AC1234…&From=whatsapp:+15551230000
&To=whatsapp:+14155238886&Body=hello&NumMedia=0&ProfileName=Ann&WaId=15551230000
```

### 4.4 Operator / user registration

- **SMS:** buy a number in the Twilio Console (Phone Numbers → Buy). Under
  Messaging configuration, set "A message comes in" = Webhook,
  `<PUBLIC_URL>/integrations/twilio/events`, POST. US 10DLC numbers need A2P
  10DLC brand + campaign registration before sending to US recipients at
  scale. Trial accounts can send only to verified numbers, prefixed "Sent from
  your Twilio trial account".
- **WhatsApp sandbox:** Console → Messaging → Try it out → WhatsApp. Shared
  number `+1 415 523 8886` (UNVERIFIED, shown in console). Each tester sends
  `join <keyword>`. The session expires 3 days after joining. Business-initiated
  messages are limited to pre-approved templates, and there is a send rate
  limit. Set "When a message comes in" to the events URL.
- **WhatsApp production:** register a WhatsApp Sender via Self Sign-up, which
  needs a Meta Business Manager account (business verification for higher
  limits), a display name approved by Meta, and templates approved by Meta for
  business-initiated messages. Free-form messages are allowed only within 24 h
  of the user's last message.
- Because the URL is per number, this is per user, not an operator-wide app.
  The connection flow could set it via the API
  (`POST /IncomingPhoneNumbers/{sid}.json SmsUrl=…`), which needs write creds.
  Decide in the trigger stream.

### 4.5 Gotchas

- Reconstruct the signed URL from `PUBLIC_URL` + path + raw query. A TLS
  proxy changes the scheme/host that `r.URL` sees, and that breaks
  verification.
- Parse the form only after reading the raw body. Sign over the decoded
  values (`r.PostForm`), not raw bytes.
- A per-trigger query token in the URL (`?t=…`) is fine: it is signed.
- `whatsapp:` prefixes must match on both `To` and `From` when sending.

Sources: https://www.twilio.com/docs/usage/webhooks/webhooks-security,
https://github.com/twilio/twilio-go/blob/main/client/request_validator.go
(sort, port both ways, `bodySHA256`),
https://www.twilio.com/docs/messaging/api/message-resource,
https://www.twilio.com/docs/messaging/guides/webhook-request,
https://www.twilio.com/docs/whatsapp/sandbox (3-day expiry, templates only),
https://www.twilio.com/docs/whatsapp/self-sign-up.

---

## 5. iMessage

**There is no official API.** Apple offers no public iMessage send/receive
API for third parties. "Messages for Business" (formerly Business Chat) is a
customer-initiated channel through approved Messaging Service Providers, not a
general API (UNVERIFIED on current terms).

Realistic options:

| Option | How | Risks |
|---|---|---|
| Daemon on the user's Mac: send | `osascript` driving Messages.app (`tell application "Messages" to send "…" to buddy "+1…" of (service 1 whose service type is iMessage)`) | Needs Automation permission (TCC prompt); AppleScript dictionary breaks between macOS versions; Mac must be awake and logged in; no delivery confirmation |
| Daemon on the user's Mac: receive | Poll `~/Library/Messages/chat.db` (SQLite, `message` table, `ROWID` as cursor) | Needs **Full Disk Access** for the daemon binary; undocumented schema that changes (`attributedBody` blobs on newer macOS); read-only access only |
| Third-party bridges (BlueBubbles, Beeper/mautrix-imessage, hosted relay vendors) | Server on a Mac, or a vendor's Mac fleet | ToS gray area; vendors have been shut down by Apple (Beeper Mini, Dec 2023); trusts a third party with message content |

The fit for reliant is a **daemon-placed action and poll** on the user's own
Mac. Per the brief, daemon-placed actions cannot use connections, so this is
a capability flag, not a connection. Recommend out of scope for v1.

---

## 6. Zapier

**Zap → reliant (inbound):** the user adds a "Webhooks by Zapier" action
(Premium on paid plans; UNVERIFIED tier) with event **POST** (or Custom
Request). Settings:

- URL: the reliant generic webhook `<PUBLIC_URL>/hooks/{trigger_id}/{token}`
  (the token in the path is the secret).
- Payload Type: `json`; Data: key/value pairs (or raw JSON via Custom Request).
- Headers: optional, e.g. `Authorization: Bearer <secret>` if the receiver
  supports a bearer instead of a path token. Zapier cannot compute an HMAC
  over the body natively (it would need a Code step), so **support
  path-token or bearer, not HMAC only**.
- Zapier may retry or replay on errors, and users can "replay" runs. Accept
  an optional `Idempotency-Key`/`X-Request-Id` header as the dedupe key, else
  hash the body.

**reliant → Zap (outbound):** the user creates a Zap with trigger "Webhooks by
Zapier → Catch Hook", which yields a URL like
`https://hooks.zapier.com/hooks/catch/<account>/<hook>/`. A reliant workflow
calls it with `http/request@1`: POST JSON, no auth (the URL is the secret).
The response is `{"status":"success","attempt":"…","id":"…","request_id":"…"}`
(UNVERIFIED shape). Catch Hook returns immediately; the Zap runs
asynchronously, so no Zap output is returned. Store the URL as a secret-ish
param, since anyone with it can trigger the Zap.

Sources: https://help.zapier.com/hc/en-us/articles/8496288690317 (Webhooks by
Zapier triggers), https://help.zapier.com/hc/en-us/articles/8496326446989
(Webhooks actions). Help-center pages are JS-rendered and were not fetched:
UNVERIFIED details as marked.

---

## 7. Fake-server checklist for wave-1 agents

Each provider's `httptest` fake should reproduce:

- **GitHub:** `ping`; a signed event; a bad signature → 401; a repeated
  `X-GitHub-Delivery` → dedupe; 403 with `x-ratelimit-remaining: 0`.
- **Slack:** `url_verification` echo; a timestamp more than 300 s old →
  reject; `X-Slack-Retry-Num` retry with the same `event_id`; a 200
  `{"ok":false,"error":"not_in_channel"}`; 429 + `Retry-After`; a bot's own
  message filtered.
- **Gmail:** baseline (no fire); a history page with `nextPageToken`; an empty
  response (no `history` key); 404 on a stale `startHistoryId` → re-baseline;
  `invalid_grant` on refresh → connection needs reconnect.
- **Twilio:** a signature over a URL with a query string; the `:443` variant;
  a `whatsapp:` prefix; a duplicate `MessageSid`.
