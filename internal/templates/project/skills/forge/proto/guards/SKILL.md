---
name: proto-guards
description: Declare forge:guards on custom RPC request fields so scaffolded edit pages respect domain transitions; detect stale pages and misspelled markers.
---

# Guarded columns and custom RPCs

#### `forge:guards` — tell the scaffolded edit page what your RPCs enforce

The markers above are all statements about a field. `forge:guards` is the one
exception: it is written on a field of a **custom RPC's request**, and what it
declares is that *this RPC owns a column's writes*. The field it sits on is
just the attachment point.

```proto
message RecordPaymentRequest {
  string invoice_id = 1;
  int64 amount_cents = 2;  // forge:guards invoices.amount_paid_cents
}
```

Write it whenever a custom RPC enforces a rule the raw column write would
skip. Forge's page generator reasons from the entity's column list and can see
nothing of your RPCs, so without the marker the scaffolded edit page writes
every column raw — and a `RecordPayment` that refuses overpayment is bypassed
by the very form forge generated. The user lowers `amount_cents` below what has
been collected, the raw CHECK fires, and they get a 500.

With it, the column leaves the edit page's `update_mask` and renders as a
disabled row naming the RPC that owns it. It is shown rather than hidden on
purpose: a field that is simply missing tells the reader nothing, while a row
reading "changed through `RecordPayment`" points at the API that can make the
change. Create is untouched — a guard is a rule about *transitions*, and the
initial value is still yours to set.

Guards changes only the page: a client calling `UpdateInvoice` directly can
still write the column. When the API must refuse it too, also mark the entity
field `// forge:read-only` — the field then still renders as the disabled row
naming the rpc, where read-only alone would hide it (`db/write-policy`).

The target is `<table>.<column>` and both halves matter. Repeat the marker, one
per line, for an RPC that guards several columns:

```proto
message ScheduleJobRequest {
  string job_id = 1;
  // forge:guards jobs.crew_id
  // forge:guards jobs.scheduled_start
  // forge:guards jobs.scheduled_end
  string crew_id = 2;
}
```

**Why you have to name the column rather than forge inferring it.** Inference
was tried and is unsound. `RecordPaymentRequest` guards
`invoices.amount_paid_cents` — a column its request never mentions — while
`amount_cents`, the field name a matcher *would* latch onto, is a freely
editable column on a different table. Name-matching would miss the real guard
and freeze an unrelated column, both silently.

One gap the marker cannot close by itself: scaffolded pages are written **once**
and never regenerated. Add a guard after the pages exist and the old edit page
keeps writing the column forever — it typechecks, nothing logs, and the only
symptom is that 500. `forge lint --guarded-fields` finds exactly that case:

```
⚠ [forgeconv-guarded-field-written] frontends/web/src/app/invoices/edit/page.tsx:42
    → This page's update_mask names invoices.amount_paid_cents, but that column
      is declared `forge:guards invoices.amount_paid_cents` … RecordPayment owns
      its writes.
```

**Spell a marker wrong and nothing happens — quietly.** An unrecognized marker
is just a proto comment: it compiles, the birth exits zero, and the field keeps
its default behavior, so a mistyped `forge:read-only` leaves the field
client-writable. `forge generate` cannot catch this, since there is no error to
raise; `forge lint --proto-markers` flags any `forge:*` in a proto comment that
matches no known marker, naming the file, the line and the closest real marker:

```
⚠ [forge-proto-markers] proto/services/orders/v1/orders.proto:8
    → "forge:server-set" was renamed to "forge:read-only" and is no longer
      recognized — this comment currently does NOTHING (the field stays
      client-writable). Rewrite it as "forge:read-only".
```

Warnings only: it never fails the build, since an unrecognized marker might be
a future forge version's or prose that happens to contain `forge:`. It runs in
the default `forge lint` pass whenever a `proto/` tree exists.
