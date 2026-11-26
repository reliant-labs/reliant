-- forge:backward-compatible — new tables only; nothing existing is read or changed

-- Migration: create_gtm
-- Created: 2026-11-26T17:31:00Z
--
-- GTM inside Reliant (proto/reliant/v1/gtm.proto, GtmService): ideas,
-- campaigns, items (posts and emails: the calendar), the org's positioning,
-- and its claims registry. Inert: nothing reads or writes these tables until
-- GtmService is served, and then only for allowlisted orgs (GtmConfig).
--
-- Tenancy: every row carries org_id. Orgs live in control-plane, so org_id
-- has no foreign key (as on every other org-scoped table). A link between
-- rows (an idea's or item's campaign, an item's idea) is a composite foreign
-- key on (org_id, <link>), so a row can only link a row of its own org; the
-- service checks the same inside its write transaction.
--
-- Status and kind columns store the GtmService enum value NAMES
-- ('GTM_IDEA_STATUS_PROPOSED'). The proto zero (…_UNSPECIFIED) means "unset"
-- on the wire and is never a state a row is in, so it is not admitted, and
-- each DEFAULT is the first real member.
--
-- revision is the optimistic-concurrency counter: 1 on insert, and every
-- update is UPDATE … WHERE revision = $expected, setting revision + 1.
--
-- The tables are new and empty, so their indexes are built here, in the
-- transaction that creates them, rather than CONCURRENTLY in files of their
-- own.
--
-- Post-deploy verification (prod): the five tables exist and are empty:
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify gtm tables" \
--     "SELECT (SELECT count(*) FROM gtm_ideas), (SELECT count(*) FROM gtm_campaigns), (SELECT count(*) FROM gtm_items), (SELECT count(*) FROM gtm_positioning), (SELECT count(*) FROM gtm_claims)"
--
-- Rollback: none needed. The previous release never names these tables; they
-- stay in place, unread.

-- A campaign groups ideas and items toward one goal over a period.
CREATE TABLE gtm_campaigns (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    name TEXT NOT NULL CHECK (btrim(name) <> ''),
    -- The campaign's north star.
    goal TEXT NOT NULL DEFAULT '',
    period_start TIMESTAMPTZ,
    period_end TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'GTM_CAMPAIGN_STATUS_DRAFT'
        CHECK (status IN ('GTM_CAMPAIGN_STATUS_DRAFT', 'GTM_CAMPAIGN_STATUS_ACTIVE', 'GTM_CAMPAIGN_STATUS_DONE', 'GTM_CAMPAIGN_STATUS_ARCHIVED')),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT gtm_campaigns_period_order CHECK (period_start IS NULL OR period_end IS NULL OR period_end >= period_start),
    -- The target of the same-org composite foreign keys below.
    CONSTRAINT gtm_campaigns_org_id_id_key UNIQUE (org_id, id)
);
CREATE INDEX gtm_campaigns_org_id_created_at_idx ON gtm_campaigns (org_id, created_at DESC);

-- An idea: something to build, a go-to-market move, or content to write,
-- with the evidence behind it. An agent proposes; a person accepts or rejects.
CREATE TABLE gtm_ideas (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    kind TEXT NOT NULL DEFAULT 'GTM_IDEA_KIND_PRODUCT'
        CHECK (kind IN ('GTM_IDEA_KIND_PRODUCT', 'GTM_IDEA_KIND_GTM', 'GTM_IDEA_KIND_CONTENT')),
    title TEXT NOT NULL CHECK (btrim(title) <> ''),
    body_md TEXT NOT NULL DEFAULT '',
    -- A JSON array of GtmEvidence (analytics readouts, links, notes).
    evidence JSONB NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(evidence) = 'array'),
    status TEXT NOT NULL DEFAULT 'GTM_IDEA_STATUS_PROPOSED'
        CHECK (status IN ('GTM_IDEA_STATUS_PROPOSED', 'GTM_IDEA_STATUS_ACCEPTED', 'GTM_IDEA_STATUS_DONE', 'GTM_IDEA_STATUS_REJECTED')),
    source TEXT NOT NULL DEFAULT 'GTM_IDEA_SOURCE_HUMAN'
        CHECK (source IN ('GTM_IDEA_SOURCE_AGENT', 'GTM_IDEA_SOURCE_HUMAN')),
    -- The chat that proposed it, when an agent did. Chats can be deleted, so
    -- this is a reference, not a foreign key.
    source_chat_id TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL CHECK (created_by <> ''),
    campaign_id TEXT,
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT gtm_ideas_campaign_same_org_fkey FOREIGN KEY (org_id, campaign_id)
        REFERENCES gtm_campaigns (org_id, id) ON DELETE SET NULL (campaign_id),
    -- The target of gtm_items' same-org idea link.
    CONSTRAINT gtm_ideas_org_id_id_key UNIQUE (org_id, id)
);
CREATE INDEX gtm_ideas_org_id_created_at_idx ON gtm_ideas (org_id, created_at DESC);
CREATE INDEX gtm_ideas_org_id_status_idx ON gtm_ideas (org_id, status);
CREATE INDEX gtm_ideas_org_id_campaign_id_idx ON gtm_ideas (org_id, campaign_id) WHERE campaign_id IS NOT NULL;

-- An item: a blog or social post, an email, a launch. With scheduled_at it is
-- on the calendar. Only the approved email path writes SENT and the email_*
-- send record; nothing in GTM publishes.
CREATE TABLE gtm_items (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    campaign_id TEXT,
    idea_id TEXT,
    kind TEXT NOT NULL DEFAULT 'GTM_ITEM_KIND_BLOG_POST'
        CHECK (kind IN ('GTM_ITEM_KIND_BLOG_POST', 'GTM_ITEM_KIND_SOCIAL_POST', 'GTM_ITEM_KIND_EMAIL', 'GTM_ITEM_KIND_LAUNCH', 'GTM_ITEM_KIND_OTHER')),
    -- Where it goes, free-form ("blog", "linkedin", "newsletter").
    channel TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL CHECK (btrim(title) <> ''),
    body_md TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'GTM_ITEM_STATUS_DRAFT'
        CHECK (status IN ('GTM_ITEM_STATUS_DRAFT', 'GTM_ITEM_STATUS_REVIEW', 'GTM_ITEM_STATUS_APPROVED', 'GTM_ITEM_STATUS_SCHEDULED', 'GTM_ITEM_STATUS_SENT', 'GTM_ITEM_STATUS_PUBLISHED', 'GTM_ITEM_STATUS_PAUSED')),
    scheduled_at TIMESTAMPTZ,
    -- EMAIL only.
    email_to TEXT[] NOT NULL DEFAULT '{}',
    email_subject TEXT NOT NULL DEFAULT '',
    -- The send record, written with SENT.
    gmail_message_id TEXT NOT NULL DEFAULT '',
    sent_at TIMESTAMPTZ,
    sent_by TEXT NOT NULL DEFAULT '',
    -- The hash of the approval card the email was sent under.
    approval_hash TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT gtm_items_campaign_same_org_fkey FOREIGN KEY (org_id, campaign_id)
        REFERENCES gtm_campaigns (org_id, id) ON DELETE SET NULL (campaign_id),
    CONSTRAINT gtm_items_idea_same_org_fkey FOREIGN KEY (org_id, idea_id)
        REFERENCES gtm_ideas (org_id, id) ON DELETE SET NULL (idea_id),
    -- Only an email is sent, and a sent one carries its whole send record.
    CONSTRAINT gtm_items_sent_is_email CHECK (status <> 'GTM_ITEM_STATUS_SENT' OR kind = 'GTM_ITEM_KIND_EMAIL'),
    CONSTRAINT gtm_items_sent_has_record CHECK (
        status <> 'GTM_ITEM_STATUS_SENT'
        OR (gmail_message_id <> '' AND sent_at IS NOT NULL AND sent_by <> '' AND approval_hash <> '')),
    CONSTRAINT gtm_items_scheduled_has_date CHECK (status <> 'GTM_ITEM_STATUS_SCHEDULED' OR scheduled_at IS NOT NULL)
);
COMMENT ON COLUMN gtm_items.approval_hash IS 'forge:readable SHA-256 of the approved item id, revision, recipients, subject and body; a content hash, not a credential';
CREATE INDEX gtm_items_org_id_scheduled_at_idx ON gtm_items (org_id, scheduled_at) WHERE scheduled_at IS NOT NULL;
CREATE INDEX gtm_items_org_id_created_at_idx ON gtm_items (org_id, created_at DESC);
CREATE INDEX gtm_items_org_id_campaign_id_idx ON gtm_items (org_id, campaign_id) WHERE campaign_id IS NOT NULL;
CREATE INDEX gtm_items_org_id_idea_id_idx ON gtm_items (org_id, idea_id) WHERE idea_id IS NOT NULL;

-- The org's positioning: one row per org, replaced whole.
CREATE TABLE gtm_positioning (
    org_id TEXT PRIMARY KEY CHECK (org_id <> ''),
    positioning_md TEXT NOT NULL DEFAULT '',
    audiences TEXT[] NOT NULL DEFAULT '{}',
    updated_by TEXT NOT NULL CHECK (updated_by <> ''),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The claims registry (growth's claims, tenant_id -> org_id): every number,
-- price, duration, license name, superlative and competitor mention in an
-- item must match a row here (internal/gtm/claimcheck). text is what a person
-- reads; text_norm is the lower-cased, whitespace-collapsed,
-- trailing-punctuation-free form postgres derives (claimcheck.Normalize), and
-- an org holds each one once. A VERIFIED row carries its evidence (source,
-- kind, who, when), and a competitor comparison needs a primary third-party
-- source. Only a person verifies.
CREATE TABLE gtm_claims (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    text TEXT NOT NULL CHECK (length(btrim(text)) BETWEEN 1 AND 500),
    text_norm TEXT GENERATED ALWAYS AS (lower(btrim(regexp_replace(btrim(regexp_replace(text, '\s+', ' ', 'g')), '[.!]+$', '')))) STORED,
    category TEXT NOT NULL
        CHECK (category IN ('GTM_CLAIM_CATEGORY_PRICING', 'GTM_CLAIM_CATEGORY_CAPABILITY', 'GTM_CLAIM_CATEGORY_LIMIT', 'GTM_CLAIM_CATEGORY_LICENSE', 'GTM_CLAIM_CATEGORY_TIMING', 'GTM_CLAIM_CATEGORY_COMPARISON')),
    status TEXT NOT NULL DEFAULT 'GTM_CLAIM_STATUS_UNVERIFIED'
        CHECK (status IN ('GTM_CLAIM_STATUS_VERIFIED', 'GTM_CLAIM_STATUS_UNVERIFIED', 'GTM_CLAIM_STATUS_RETIRED')),
    source_url TEXT NOT NULL DEFAULT '',
    -- GTM_CLAIM_SOURCE_KIND_UNSPECIFIED means "none yet": an unverified claim.
    source_kind TEXT NOT NULL DEFAULT 'GTM_CLAIM_SOURCE_KIND_UNSPECIFIED'
        CHECK (source_kind IN ('GTM_CLAIM_SOURCE_KIND_UNSPECIFIED', 'GTM_CLAIM_SOURCE_KIND_DOCS', 'GTM_CLAIM_SOURCE_KIND_PRICING_CATALOG', 'GTM_CLAIM_SOURCE_KIND_CODE', 'GTM_CLAIM_SOURCE_KIND_PRIMARY_THIRD_PARTY')),
    verified_at TIMESTAMPTZ,
    verified_by TEXT NOT NULL DEFAULT '',
    expires_or_recheck_at TIMESTAMPTZ,
    notes TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT gtm_claims_text_norm_not_empty CHECK (text_norm <> ''),
    CONSTRAINT gtm_claims_verified_has_evidence CHECK (
        status <> 'GTM_CLAIM_STATUS_VERIFIED'
        OR (source_url <> '' AND source_kind <> 'GTM_CLAIM_SOURCE_KIND_UNSPECIFIED' AND verified_at IS NOT NULL AND verified_by <> '')),
    CONSTRAINT gtm_claims_comparison_needs_primary_source CHECK (
        category <> 'GTM_CLAIM_CATEGORY_COMPARISON'
        OR status <> 'GTM_CLAIM_STATUS_VERIFIED'
        OR source_kind = 'GTM_CLAIM_SOURCE_KIND_PRIMARY_THIRD_PARTY')
);
-- Fixed at creation: changing either would change what a verification vouched for.
COMMENT ON COLUMN gtm_claims.text IS 'forge:immutable';
COMMENT ON COLUMN gtm_claims.category IS 'forge:immutable';
CREATE UNIQUE INDEX gtm_claims_org_id_text_norm_key ON gtm_claims (org_id, text_norm);
CREATE INDEX gtm_claims_org_id_status_idx ON gtm_claims (org_id, status);
