---
name: observability/product-events/actor-privacy
description: Who a server-side product event is about (observe.WithActor, opaque ids only), the no-personal-data rule for properties and its three enforcement points (observe.LooksLikePII, the ingest gateway's scrub, forgeconv-product-event-pii), and opt-out with observe.SetConsentSource (fails closed).
---

# Product events: the actor, personal data and consent

## Who the event is about: `observe.WithActor`

Code with no request claims (a webhook, a reaper, a queue consumer) names the
actor itself:

```go
ctx = observe.WithActor(ctx, observe.Actor{OrgID: d.OrgID, UserID: d.CreatedBy, AnonymousID: installID})
observe.Track(ctx, "deploy_succeeded", slog.String("mode", "account"), slog.Bool("first_for_org", first))
```

A non-empty `Actor` field wins over the claims; an empty one falls back to
them. Ids must be opaque (an auth subject, an org id, a random install id):
an id with an `@`, an IP address or a JWT is dropped (the event is kept),
with one warning per process.

## No personal data in properties

Properties are tags: enums, booleans, counts, buckets. Never free text, emails,
names, hostnames, paths, URLs or error text. Enforced three ways:

- `observe.Track` (and the CLI emitter) drop a property whose value looks like
  an email, an IP, a JWT, a bearer token, a provider key, URL userinfo or a
  long opaque run (`observe.LooksLikePII`), with one warning per key that names
  the key and never the value.
- On Reliant hosting the ingest gateway scrubs `product.*` attributes with the
  same patterns.
- `forge lint` (`forgeconv-product-event-pii`) flags a property key with an
  `email`, `name`, `ip`, `url`, `path` or `token` part.

## Consent: `observe.SetConsentSource`

An app whose users can opt out of product analytics registers its lookup once
at startup. It is asked about every server-side event's actor, and it fails
closed: an error or a panic drops the event.

```go
observe.SetConsentSource(observe.ConsentFunc(func(ctx context.Context, a observe.Actor) (bool, error) {
	return !optOuts.Has(a.UserID), nil // answer from memory or a cache: it runs on every event
}))
```
