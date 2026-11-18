---
name: observability/product-events/engaged-time
description: The automatic page_engaged event every generated web frontend records per page view — view_id, engaged_ms (visible, focused and active time, 30 s idle cutoff, 30 min cap), scroll_pct, entry, resumed — when it is reported (route change, hidden tab, pagehide with a keepalive export), and how to query it (the last report per view_id).
---

# Engaged time: `page_engaged`

Each page view also ends in one `page_engaged`, in every tier above `off`. Its
one identifier, `view_id`, names the view, not a person:

| prop | value |
|---|---|
| `route` | the view's route, named as its `page_viewed` was |
| `view_id` | 16 random hex characters minted when the view starts, kept only in memory, shared by the view's `resumed` reports and by no other view |
| `engaged_ms` | the milliseconds the page was **visible**, the window **focused** (or in use anyway: a wheel over a background window) and the person **active**: a pointer, key, wheel, touch or scroll in the last 30 s. Past that idle cutoff the clock stops until the next input. Capped at 1,800,000 (30 minutes) |
| `scroll_pct` | the deepest the bottom of the viewport reached in the document during the view, 0-100: `(scrollY + innerHeight) / scrollHeight`. A page that fits the viewport is 100. Content scrolling inside an app's own element is not the document's depth |
| `entry` | `"true"` when the view was the first of its page load, as on `page_viewed` |
| `resumed` | `"true"` on a later report of the same view (below) |

It is reported once per view, at the first of a route change (SPAs), the tab
being hidden, or `pagehide` (unload or the back/forward cache: a static
multi-page site's only report). On hide and unload the runtime then asks the
SDK to export at once; the exporter posts with `fetch(…, {keepalive: true})`,
which the browser completes after the page is gone. A view shorter than 500 ms
of engagement (a redirect) is not reported.

A reader who comes back to a view (the tab shown again, a back/forward-cache
restore) keeps adding to it: the next report has the same `view_id`,
`resumed: "true"`, and the view's **cumulative** `engaged_ms` and `scroll_pct`.
So per view, take the **last** report, which is also the largest. Key it on
`view_id`, never on route and time order: a back/forward-cache restore can
resume a view of a route the same visitor has since viewed again.

```sql
SELECT EnvId, Props['view_id'] AS view, any(Props['route']) AS route,
       max(toFloat64OrZero(Props['engaged_ms'])) AS engaged_ms,
       max(toFloat64OrZero(Props['scroll_pct'])) AS scroll_pct
FROM product_events
WHERE EventName = 'page_engaged' AND Props['view_id'] != '' AND …
GROUP BY EnvId, view
```

`page_engaged` is not an interactive event: it neither starts nor extends a
visit, and a view with only `page_engaged` after it is still a bounce.
