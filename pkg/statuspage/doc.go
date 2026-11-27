// Package statuspage is the forge.status/v1 status page document and its
// renderer: a public page that tells an app's own users whether the app
// works, built from outside-in uptime probes.
//
// The [Document] is the public contract. It holds the page (title, logo,
// links, accent colour), one row per component with its current [State] and
// up to 90 daily availability bars, the published incidents and maintenance
// windows with their updates, and the time it was generated. A team that
// wants its own design builds it from status.json; a team with no Reliant
// account fills a Document from its own monitor's data and renders the same
// page.
//
// [Render] turns a Document into three files: index.html (no JavaScript
// required, no third-party script, every state shown as an icon and a word,
// never colour alone), feed.atom (one entry per incident and maintenance
// window) and the canonical status.json. It is pure: the same Document gives
// the same bytes, so a publisher may compare digests and write only on a
// change. [Decode] reads a status.json back, strictly.
//
// # What is public, and what never is
//
// The Document is an ALLOWLIST projection: it carries only the fields its
// types declare, and a test pins that set, so a field cannot become public
// without someone classifying it. A publisher copies values into it; it never
// embeds a telemetry record. Never public, and never a field here: uptime
// check names, URLs, paths and hostnames, HTTP statuses, error types,
// latency, certificate expiry, environment, organization and project ids,
// alert names, issue titles, who published an update, whether an agent
// drafted it, and any unpublished draft.
//
// Bodies are plain text: blank lines separate paragraphs, and every
// character is escaped. No HTML or Markdown reaches the page.
//
// Nothing here publishes anything: the package renders a document it is
// handed. In a forge env a page is opt-in (forge.StatusPage), and going
// public is a separate, audited act by a person in the control plane.
//
// [Resolve] derives a component's state from its checks' measured states
// (the worst wins, an active maintenance window takes precedence, a person's
// override takes precedence over both and is marked).
package statuspage
