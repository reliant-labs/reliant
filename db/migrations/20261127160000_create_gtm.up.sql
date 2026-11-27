-- forge:backward-compatible — new tables only; nothing existing is read or changed

-- Migration: create_gtm
-- Created: 2026-11-27T16:00:00Z
--
-- GTM inside Reliant (proto/reliant/v1/gtm.proto, GtmService): strategies
-- and their campaigns, channels, ideas, items (posts, emails, videos: the
-- calendar), assets (file metadata; the bytes are in the org's bucket), the
-- org's positioning, and its claims registry. The strategy library is not a
-- table: it is the generic idea bank embedded in the binary
-- (internal/gtm/library); rows only name its keys (library_key). Inert:
-- nothing reads or writes these tables until GtmService is served, and then
-- only for allowlisted orgs (GtmConfig).
--
-- Tenancy: every row carries org_id. Orgs live in control-plane, so org_id
-- has no foreign key (as on every other org-scoped table). A link between
-- rows (a campaign's strategy, an idea's or item's campaign, an item's idea
-- or channel, an asset's item or campaign) is a composite foreign key on
-- (org_id, <link>), so a row can only link a row of its own org; the service
-- checks the same inside its write transaction. A campaign's planned channels
-- are an array, which no foreign key can cover: the service checks them.
--
-- Status, kind and format columns store the GtmService enum value NAMES
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
-- Post-deploy verification (prod): the eight tables exist and are empty:
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify gtm tables" \
--     "SELECT (SELECT count(*) FROM gtm_ideas), (SELECT count(*) FROM gtm_campaigns), (SELECT count(*) FROM gtm_items), (SELECT count(*) FROM gtm_positioning), (SELECT count(*) FROM gtm_claims), (SELECT count(*) FROM gtm_strategies), (SELECT count(*) FROM gtm_channels), (SELECT count(*) FROM gtm_assets)"
--
-- Rollback: none needed. The previous release never names these tables; they
-- stay in place, unread.

-- A strategy: a goal over a period, pursued by its campaigns. An agent
-- drafts; a person makes it ACTIVE, DONE or ARCHIVED. Several may be ACTIVE.
CREATE TABLE gtm_strategies (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    name TEXT NOT NULL CHECK (btrim(name) <> ''),
    -- The goal, its north star.
    goal_md TEXT NOT NULL DEFAULT '',
    -- What success is measured by, free text ("200 weekly active orgs").
    target_metric TEXT NOT NULL DEFAULT '',
    period_start DATE,
    period_end DATE,
    status TEXT NOT NULL DEFAULT 'GTM_STRATEGY_STATUS_DRAFT'
        CHECK (status IN ('GTM_STRATEGY_STATUS_DRAFT', 'GTM_STRATEGY_STATUS_ACTIVE', 'GTM_STRATEGY_STATUS_DONE', 'GTM_STRATEGY_STATUS_ARCHIVED')),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_by TEXT NOT NULL CHECK (created_by <> ''),
    updated_by TEXT NOT NULL CHECK (updated_by <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- One of the 13 strategy catalog categories; NULL for none.
    category TEXT CONSTRAINT gtm_strategies_category_known CHECK (category IN ('GTM_STRATEGY_CATEGORY_PRICING_OFFERS_INCENTIVES', 'GTM_STRATEGY_CATEGORY_REFERRAL_VIRAL_GROWTH', 'GTM_STRATEGY_CATEGORY_PAID_ACQUISITION', 'GTM_STRATEGY_CATEGORY_ORGANIC_CONTENT_SEO', 'GTM_STRATEGY_CATEGORY_OUTBOUND_SALES_PROSPECTING', 'GTM_STRATEGY_CATEGORY_PARTNERSHIPS_DISTRIBUTION', 'GTM_STRATEGY_CATEGORY_LAUNCHES_PR', 'GTM_STRATEGY_CATEGORY_COMMUNITY_EVENTS', 'GTM_STRATEGY_CATEGORY_PRODUCT_LED_GROWTH', 'GTM_STRATEGY_CATEGORY_CONVERSION_OPTIMIZATION', 'GTM_STRATEGY_CATEGORY_RETENTION_EXPANSION', 'GTM_STRATEGY_CATEGORY_TRUST_REPUTATION', 'GTM_STRATEGY_CATEGORY_MARKET_RESEARCH_POSITIONING')),
    -- The idea bank key it was made from (UseLibraryStrategy): a reference,
    -- not a foreign key (the bank is embedded, and a later release may drop
    -- an entry).
    library_key TEXT CONSTRAINT gtm_strategies_library_key_slug CHECK (library_key ~ '^[a-z0-9][a-z0-9_-]{0,127}$'),
    CONSTRAINT gtm_strategies_period_order CHECK (period_start IS NULL OR period_end IS NULL OR period_end >= period_start),
    -- The target of gtm_campaigns' same-org strategy link.
    CONSTRAINT gtm_strategies_org_id_id_key UNIQUE (org_id, id)
);
CREATE INDEX gtm_strategies_org_id_status_idx ON gtm_strategies (org_id, status);
CREATE INDEX gtm_strategies_org_id_created_at_idx ON gtm_strategies (org_id, created_at DESC);

-- A channel: where items go out. INTEGRATED through a Reliant integration
-- (integration_kind, e.g. gmail; connection_id empty means the acting user's
-- default connection), or MANUAL (a person posts by hand and records the URL).
-- An agent may add and edit MANUAL channels; only a person sets the
-- integration fields. The service seeds an allowlisted org's
-- defaults on its first read (orgs live in control-plane, so no migration
-- can).
CREATE TABLE gtm_channels (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    key TEXT NOT NULL CONSTRAINT gtm_channels_key_slug CHECK (key ~ '^[a-z0-9-]{1,32}$'),
    name TEXT NOT NULL CHECK (btrim(name) <> ''),
    mode TEXT NOT NULL CHECK (mode IN ('GTM_CHANNEL_MODE_INTEGRATED', 'GTM_CHANNEL_MODE_MANUAL')),
    -- NULL, never '' (an empty value on the wire is stored as NULL).
    integration_kind TEXT CONSTRAINT gtm_channels_integration_kind_not_empty CHECK (integration_kind IS NULL OR integration_kind <> ''),
    connection_id TEXT CONSTRAINT gtm_channels_connection_id_not_empty CHECK (connection_id IS NULL OR connection_id <> ''),
    manual_instructions_md TEXT NOT NULL DEFAULT '',
    -- The kinds of channel it is (slugs, ^[a-z0-9-]{1,32}$ each), matched
    -- against a library entry's channel_types. A CHECK cannot loop over the
    -- elements, so the slug CHECK runs over the space-joined array, and the
    -- split-back count catches an element that holds a space itself ("a b"
    -- would join as two slugs). An empty or NULL element joins to nothing
    -- visible, so those are refused separately.
    channel_types TEXT[] NOT NULL DEFAULT '{}'
        CONSTRAINT gtm_channels_channel_types_slugs CHECK (
            array_to_string(channel_types, ' ') ~ '^([a-z0-9-]{1,32}( |$))*$'
            AND cardinality(string_to_array(array_to_string(channel_types, ' '), ' ')) = cardinality(channel_types))
        CONSTRAINT gtm_channels_channel_types_not_empty CHECK (NOT ('' = ANY (channel_types)) AND array_position(channel_types, NULL) IS NULL),
    status TEXT NOT NULL DEFAULT 'GTM_CHANNEL_STATUS_ACTIVE'
        CHECK (status IN ('GTM_CHANNEL_STATUS_ACTIVE', 'GTM_CHANNEL_STATUS_PAUSED', 'GTM_CHANNEL_STATUS_ARCHIVED')),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Who added it; created_by is empty for a seeded one.
    source TEXT NOT NULL DEFAULT 'GTM_CHANNEL_SOURCE_PERSON'
        CHECK (source IN ('GTM_CHANNEL_SOURCE_SEED', 'GTM_CHANNEL_SOURCE_AGENT', 'GTM_CHANNEL_SOURCE_PERSON')),
    created_by TEXT NOT NULL DEFAULT '',
    CONSTRAINT gtm_channels_creator CHECK ((source = 'GTM_CHANNEL_SOURCE_SEED') = (created_by = '')),
    CONSTRAINT gtm_channels_integrated_has_kind CHECK (mode <> 'GTM_CHANNEL_MODE_INTEGRATED' OR (integration_kind IS NOT NULL AND integration_kind <> '')),
    CONSTRAINT gtm_channels_manual_has_no_integration CHECK (mode <> 'GTM_CHANNEL_MODE_MANUAL' OR (integration_kind IS NULL AND connection_id IS NULL)),
    -- The seed's ON CONFLICT target: an org holds each key once.
    CONSTRAINT gtm_channels_org_id_key_key UNIQUE (org_id, key),
    -- The target of gtm_items' same-org channel link.
    CONSTRAINT gtm_channels_org_id_id_key UNIQUE (org_id, id)
);
CREATE INDEX gtm_channels_org_id_status_idx ON gtm_channels (org_id, status);

-- A campaign groups ideas and items toward one goal over a period, under one
-- strategy, and plans which channels and formats it will use.
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
    strategy_id TEXT NOT NULL,
    -- gtm_channels ids of the org (the service checks them: no foreign key
    -- covers an array).
    planned_channel_ids TEXT[] NOT NULL DEFAULT '{}',
    planned_formats TEXT[] NOT NULL DEFAULT '{}'
        CONSTRAINT gtm_campaigns_planned_formats_known CHECK (planned_formats <@ ARRAY['GTM_ITEM_FORMAT_SHORT_POST', 'GTM_ITEM_FORMAT_LONG_POST', 'GTM_ITEM_FORMAT_EMAIL', 'GTM_ITEM_FORMAT_COLD_OUTREACH', 'GTM_ITEM_FORMAT_VIDEO', 'GTM_ITEM_FORMAT_MEDIA_PITCH', 'GTM_ITEM_FORMAT_AD', 'GTM_ITEM_FORMAT_OTHER']::TEXT[]),
    -- Every campaign belongs to one strategy, which cannot be deleted under it.
    CONSTRAINT gtm_campaigns_strategy_same_org_fkey FOREIGN KEY (org_id, strategy_id)
        REFERENCES gtm_strategies (org_id, id) ON DELETE RESTRICT,
    CONSTRAINT gtm_campaigns_period_order CHECK (period_start IS NULL OR period_end IS NULL OR period_end >= period_start),
    -- The target of the same-org composite foreign keys below.
    CONSTRAINT gtm_campaigns_org_id_id_key UNIQUE (org_id, id)
);
CREATE INDEX gtm_campaigns_org_id_created_at_idx ON gtm_campaigns (org_id, created_at DESC);
CREATE INDEX gtm_campaigns_org_id_status_idx ON gtm_campaigns (org_id, status);
CREATE INDEX gtm_campaigns_org_id_strategy_id_idx ON gtm_campaigns (org_id, strategy_id);

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
    -- The idea bank entry it came from: a reference, not a foreign key (the
    -- service checks the key against the bank on every write).
    library_key TEXT CONSTRAINT gtm_ideas_library_key_slug CHECK (library_key ~ '^[a-z0-9][a-z0-9_-]{0,127}$'),
    -- A JSON array of GtmTailorAnswer ({"question", "answer"}): the entry's
    -- tailor_questions answered for this business, at most 20.
    -- Every element is an object whose question and answer are strings.
    tailor_answers JSONB NOT NULL DEFAULT '[]'
        CONSTRAINT gtm_ideas_tailor_answers_array CHECK (jsonb_typeof(tailor_answers) = 'array' AND jsonb_array_length(tailor_answers) <= 20)
        CONSTRAINT gtm_ideas_tailor_answers_shape CHECK (NOT jsonb_path_exists(tailor_answers, '$[*] ? (@.type() != "object" || !exists(@.question) || !exists(@.answer) || @.question.type() != "string" || @.answer.type() != "string")')),
    -- A tool-agnostic automation that carries it out (markdown); '' for none.
    suggested_workflow_md TEXT NOT NULL DEFAULT '',
    CONSTRAINT gtm_ideas_campaign_same_org_fkey FOREIGN KEY (org_id, campaign_id)
        REFERENCES gtm_campaigns (org_id, id) ON DELETE SET NULL (campaign_id),
    -- The target of gtm_items' same-org idea link.
    CONSTRAINT gtm_ideas_org_id_id_key UNIQUE (org_id, id)
);
CREATE INDEX gtm_ideas_org_id_created_at_idx ON gtm_ideas (org_id, created_at DESC);
CREATE INDEX gtm_ideas_org_id_status_idx ON gtm_ideas (org_id, status);
CREATE INDEX gtm_ideas_org_id_campaign_id_idx ON gtm_ideas (org_id, campaign_id) WHERE campaign_id IS NOT NULL;
CREATE INDEX gtm_ideas_org_id_library_key_idx ON gtm_ideas (org_id, library_key) WHERE library_key IS NOT NULL;

-- An item: a blog or social post, an email, a launch. With scheduled_at it is
-- on the calendar. Only the approved email path writes SENT and the email_*
-- send record; nothing in GTM publishes.
CREATE TABLE gtm_items (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    campaign_id TEXT,
    idea_id TEXT,
    -- What it is. EMAIL and COLD_OUTREACH are the email-type formats.
    format TEXT NOT NULL DEFAULT 'GTM_ITEM_FORMAT_SHORT_POST'
        CHECK (format IN ('GTM_ITEM_FORMAT_SHORT_POST', 'GTM_ITEM_FORMAT_LONG_POST', 'GTM_ITEM_FORMAT_EMAIL', 'GTM_ITEM_FORMAT_COLD_OUTREACH', 'GTM_ITEM_FORMAT_VIDEO', 'GTM_ITEM_FORMAT_MEDIA_PITCH', 'GTM_ITEM_FORMAT_AD', 'GTM_ITEM_FORMAT_OTHER')),
    -- Where it goes out; NULL for not chosen yet.
    channel_id TEXT,
    title TEXT NOT NULL CHECK (btrim(title) <> ''),
    body_md TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'GTM_ITEM_STATUS_DRAFT'
        CHECK (status IN ('GTM_ITEM_STATUS_DRAFT', 'GTM_ITEM_STATUS_REVIEW', 'GTM_ITEM_STATUS_APPROVED', 'GTM_ITEM_STATUS_SCHEDULED', 'GTM_ITEM_STATUS_SENT', 'GTM_ITEM_STATUS_PUBLISHED', 'GTM_ITEM_STATUS_PAUSED')),
    scheduled_at TIMESTAMPTZ,
    -- Email-type only.
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
    -- Where it went out, recorded by a person (required to mark an item on a
    -- MANUAL channel PUBLISHED; the service checks that, across tables).
    published_url TEXT CONSTRAINT gtm_items_published_url_http CHECK (published_url ~ '^https?://'),
    CONSTRAINT gtm_items_channel_same_org_fkey FOREIGN KEY (org_id, channel_id)
        REFERENCES gtm_channels (org_id, id) ON DELETE RESTRICT,
    CONSTRAINT gtm_items_campaign_same_org_fkey FOREIGN KEY (org_id, campaign_id)
        REFERENCES gtm_campaigns (org_id, id) ON DELETE SET NULL (campaign_id),
    CONSTRAINT gtm_items_idea_same_org_fkey FOREIGN KEY (org_id, idea_id)
        REFERENCES gtm_ideas (org_id, id) ON DELETE SET NULL (idea_id),
    -- Only an email-type item is sent, and a sent one carries its whole send
    -- record.
    CONSTRAINT gtm_items_sent_is_email CHECK (status <> 'GTM_ITEM_STATUS_SENT' OR format IN ('GTM_ITEM_FORMAT_EMAIL', 'GTM_ITEM_FORMAT_COLD_OUTREACH')),
    CONSTRAINT gtm_items_sent_has_record CHECK (
        status <> 'GTM_ITEM_STATUS_SENT'
        OR (gmail_message_id <> '' AND sent_at IS NOT NULL AND sent_by <> '' AND approval_hash <> '')),
    CONSTRAINT gtm_items_scheduled_has_date CHECK (status <> 'GTM_ITEM_STATUS_SCHEDULED' OR scheduled_at IS NOT NULL),
    -- An email-type item is dated while APPROVED and goes out as SENT; it is
    -- never SCHEDULED or PUBLISHED (GtmItemStatus).
    CONSTRAINT gtm_items_email_never_scheduled CHECK (NOT (format IN ('GTM_ITEM_FORMAT_EMAIL', 'GTM_ITEM_FORMAT_COLD_OUTREACH') AND status = 'GTM_ITEM_STATUS_SCHEDULED')),
    -- Backstops for the send path: an approval card is only ever made for an
    -- email-type item, and it is only held by an APPROVED (in-doubt) or SENT one.
    CONSTRAINT gtm_items_approval_hash_is_email CHECK (approval_hash = '' OR format IN ('GTM_ITEM_FORMAT_EMAIL', 'GTM_ITEM_FORMAT_COLD_OUTREACH')),
    CONSTRAINT gtm_items_approval_hash_status CHECK (approval_hash = '' OR status IN ('GTM_ITEM_STATUS_APPROVED', 'GTM_ITEM_STATUS_SENT')),
    CONSTRAINT gtm_items_email_never_published CHECK (NOT (format IN ('GTM_ITEM_FORMAT_EMAIL', 'GTM_ITEM_FORMAT_COLD_OUTREACH') AND status = 'GTM_ITEM_STATUS_PUBLISHED')),
    -- The target of gtm_assets' same-org item link.
    CONSTRAINT gtm_items_org_id_id_key UNIQUE (org_id, id)
);
-- Third-party recipient addresses: kept from the read-only role (forge db query).
COMMENT ON COLUMN gtm_items.email_to IS 'forge:secret';
COMMENT ON COLUMN gtm_items.approval_hash IS 'forge:readable SHA-256 of the approved item id, revision, recipients, subject and body; a content hash, not a credential';
CREATE INDEX gtm_items_org_id_scheduled_at_idx ON gtm_items (org_id, scheduled_at) WHERE scheduled_at IS NOT NULL;
CREATE INDEX gtm_items_org_id_created_at_idx ON gtm_items (org_id, created_at DESC);
CREATE INDEX gtm_items_org_id_status_idx ON gtm_items (org_id, status);
CREATE INDEX gtm_items_org_id_campaign_id_idx ON gtm_items (org_id, campaign_id) WHERE campaign_id IS NOT NULL;
CREATE INDEX gtm_items_org_id_idea_id_idx ON gtm_items (org_id, idea_id) WHERE idea_id IS NOT NULL;
CREATE INDEX gtm_items_org_id_channel_id_idx ON gtm_items (org_id, channel_id) WHERE channel_id IS NOT NULL;

-- An asset: a file attached to an item and/or a campaign. Metadata only;
-- the bytes are in the org's bucket under object_key, always
-- gtm/<org_id>/<id> (so the key names its tenant). Uploads land on the
-- staging key <object_key>.upload and the server copies them to object_key
-- at confirm, so no client ever writes a final key. PENDING from the upload
-- URL until ConfirmAssetUpload checked the bytes, then READY; the size and
-- type limits are the service's.
CREATE TABLE gtm_assets (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    org_id TEXT NOT NULL CHECK (org_id <> ''),
    item_id TEXT,
    campaign_id TEXT,
    object_key TEXT NOT NULL,
    filename TEXT NOT NULL DEFAULT '',
    content_type TEXT NOT NULL CHECK (content_type <> ''),
    -- Declared while PENDING (may be unknown), checked once READY.
    size_bytes BIGINT CHECK (size_bytes > 0 AND size_bytes <= 67108864),
    -- Hex SHA-256 of the bytes, set once READY.
    sha256 TEXT CONSTRAINT gtm_assets_sha256_hex CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    status TEXT NOT NULL DEFAULT 'GTM_ASSET_STATUS_PENDING'
        CHECK (status IN ('GTM_ASSET_STATUS_PENDING', 'GTM_ASSET_STATUS_READY')),
    uploaded_by TEXT NOT NULL CHECK (uploaded_by <> ''),
    source TEXT NOT NULL CHECK (source IN ('GTM_ASSET_SOURCE_CHAT', 'GTM_ASSET_SOURCE_WORKFLOW', 'GTM_ASSET_SOURCE_PERSON')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT gtm_assets_item_same_org_fkey FOREIGN KEY (org_id, item_id)
        REFERENCES gtm_items (org_id, id) ON DELETE SET NULL (item_id),
    CONSTRAINT gtm_assets_campaign_same_org_fkey FOREIGN KEY (org_id, campaign_id)
        REFERENCES gtm_campaigns (org_id, id) ON DELETE SET NULL (campaign_id),
    CONSTRAINT gtm_assets_attached CHECK (item_id IS NOT NULL OR campaign_id IS NOT NULL),
    CONSTRAINT gtm_assets_object_key_is_its_own CHECK (object_key = 'gtm/' || org_id || '/' || id),
    CONSTRAINT gtm_assets_ready_is_checked CHECK (status <> 'GTM_ASSET_STATUS_READY' OR (size_bytes IS NOT NULL AND sha256 IS NOT NULL))
);
COMMENT ON COLUMN gtm_assets.sha256 IS 'forge:readable hex SHA-256 of the file bytes, an integrity checksum, not a credential';
CREATE INDEX gtm_assets_org_id_item_id_idx ON gtm_assets (org_id, item_id);
CREATE INDEX gtm_assets_org_id_campaign_id_idx ON gtm_assets (org_id, campaign_id);
-- The 24-hour sweep's scan: the PENDING assets, oldest first.
CREATE INDEX gtm_assets_pending_created_at_idx ON gtm_assets (created_at) WHERE status = 'GTM_ASSET_STATUS_PENDING';

-- The org's positioning: one row per org, replaced whole. business_type and
-- business_models are what "fits my business" reads in the idea bank; '' and
-- '{}' mean not said.
CREATE TABLE gtm_positioning (
    org_id TEXT PRIMARY KEY CHECK (org_id <> ''),
    positioning_md TEXT NOT NULL DEFAULT '',
    audiences TEXT[] NOT NULL DEFAULT '{}',
    updated_by TEXT NOT NULL CHECK (updated_by <> ''),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    business_type TEXT NOT NULL DEFAULT ''
        CONSTRAINT gtm_positioning_business_type_known CHECK (business_type = '' OR business_type IN ('GTM_BUSINESS_TYPE_B2B_SAAS', 'GTM_BUSINESS_TYPE_B2C_APP', 'GTM_BUSINESS_TYPE_DEVELOPER_TOOLS', 'GTM_BUSINESS_TYPE_ECOMMERCE', 'GTM_BUSINESS_TYPE_MARKETPLACE', 'GTM_BUSINESS_TYPE_AGENCY_SERVICES', 'GTM_BUSINESS_TYPE_CREATOR_MEDIA', 'GTM_BUSINESS_TYPE_LOCAL_BUSINESS', 'GTM_BUSINESS_TYPE_OPEN_SOURCE', 'GTM_BUSINESS_TYPE_HARDWARE')),
    -- Each model at most once: the service enforces it (a CHECK cannot
    -- compare an array's elements with each other cheaply).
    business_models TEXT[] NOT NULL DEFAULT '{}'
        CONSTRAINT gtm_positioning_business_models_known CHECK (business_models <@ ARRAY['GTM_BUSINESS_MODEL_SUBSCRIPTION', 'GTM_BUSINESS_MODEL_USAGE_BASED', 'GTM_BUSINESS_MODEL_ONE_TIME', 'GTM_BUSINESS_MODEL_FREEMIUM', 'GTM_BUSINESS_MODEL_FREE_OSS', 'GTM_BUSINESS_MODEL_MARKETPLACE', 'GTM_BUSINESS_MODEL_SERVICES', 'GTM_BUSINESS_MODEL_AD_SUPPORTED']::TEXT[])
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
