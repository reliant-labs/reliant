#!/usr/bin/env bash

set -euo pipefail

VERSION="${VERSION:-${1:-}}"
if [[ -z "$VERSION" ]]; then
  echo "Error: VERSION is required (env or first arg)" >&2
  exit 1
fi

RELEASE_FILE="${RELEASE_FILE:-mintlify-docs/data/releases/${VERSION}.yaml}"
CHANGELOG_URL="${CHANGELOG_URL:-https://docs.reliantlabs.io/changelog#${VERSION}}"
DRY_RUN="${DRY_RUN:-false}"

if [[ ! -f "$RELEASE_FILE" ]]; then
  echo "::warning::No Mintlify changelog file found at $RELEASE_FILE — skipping changelog email"
  exit 0
fi

if [[ -z "${CUSTOMERIO_BROADCAST_ID:-}" ]]; then
  echo "::warning::CUSTOMERIO_CHANGELOG_BROADCAST_ID not set — skipping changelog email"
  exit 0
fi

if [[ "$DRY_RUN" != "true" && -z "${CUSTOMERIO_APP_API_KEY:-}" ]]; then
  echo "Error: CUSTOMERIO_APP_API_KEY is required unless DRY_RUN=true" >&2
  exit 1
fi

RELEASE_JSON=$(yq -o=json "$RELEASE_FILE")

RAW_DATE=$(echo "$RELEASE_JSON" | jq -r '.date // empty')
if [[ -n "$RAW_DATE" ]]; then
  DISPLAY_DATE=$(date -d "$RAW_DATE" '+%B %-d, %Y' 2>/dev/null || echo "$RAW_DATE")
else
  DISPLAY_DATE="Latest"
fi

PAYLOAD=$(jq -n \
  --argjson release "$RELEASE_JSON" \
  --arg version "$VERSION" \
  --arg changelog_url "$CHANGELOG_URL" \
  --arg display_date "$DISPLAY_DATE" \
  '{
    data: {
      version: ($release.version // $version),
      title: $release.title,
      date: $display_date,
      summary: $release.summary,
      changelog_url: $changelog_url,
      items: $release.items
    }
  }')

echo "Triggering changelog broadcast for ${VERSION}..."
echo "Payload preview:"
echo "$PAYLOAD" | jq '.data | {version, title, date, item_count: (.items | length)}'

if [[ "$DRY_RUN" == "true" ]]; then
  echo "DRY_RUN=true — skipping Customer.io trigger"
  exit 0
fi

HTTP_CODE=$(curl -s -o /tmp/cio-response.json -w "%{http_code}" \
  -X POST \
  "https://api.customer.io/v1/campaigns/${CUSTOMERIO_BROADCAST_ID}/triggers" \
  -H "Authorization: Bearer ${CUSTOMERIO_APP_API_KEY}" \
  -H "Content-Type: application/json" \
  -d "$PAYLOAD")

if [[ "$HTTP_CODE" -ge 200 && "$HTTP_CODE" -lt 300 ]]; then
  echo "Changelog broadcast triggered successfully (HTTP ${HTTP_CODE})"
else
  BODY=$(cat /tmp/cio-response.json)
  echo "::warning::Changelog broadcast trigger returned HTTP ${HTTP_CODE}: ${BODY}"
fi