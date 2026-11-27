package statuspage

import "time"

// Schema is the value of [Document.Schema] this package reads and writes.
// A document naming any other schema is refused, so a reader never guesses
// at a shape it does not know.
const Schema = "forge.status/v1"

// Bounds a [Document] is held to. They keep a page readable and its files
// small; a publisher trims to them (the newest incidents, the last 90 days)
// rather than failing.
const (
	MaxTitle         = 100
	MaxLinks         = 10
	MaxLinkLabel     = 40
	MaxURL           = 2048
	MaxComponents    = 50
	MaxComponentName = 64
	MaxDescription   = 200
	MaxDays          = 90
	MaxIncidents     = 100
	MaxMaintenance   = 50
	MaxIncidentTitle = 200
	MaxUpdates       = 50
	MaxUpdateBody    = 5000
	MaxID            = 64
)

// State is a component's state.
type State string

// The component states. Measured state comes from the uptime probes (the
// pkg/obsquery definitions, so the page, the Uptime dashboard and the alerts
// agree on what "down" means); maintenance comes from a published window.
const (
	// StateOperational: every check's newest probe passed and no check has
	// an open run of failures.
	StateOperational State = "operational"
	// StateDegraded: a recent probe failed, but no run of failures reached
	// an incident.
	StateDegraded State = "degraded"
	// StateOutage: an uptime incident is open on a check (a run of
	// consecutive failed probes).
	StateOutage State = "outage"
	// StateMaintenance: a published maintenance window covering the
	// component is active.
	StateMaintenance State = "maintenance"
	// StateNoData: the newest probe is stale, or there are no samples.
	// Silence from a prober is not evidence of health, so it is never shown
	// as operational.
	StateNoData State = "no_data"
)

// States is every [State], in the order a page explains them.
var States = []State{StateOperational, StateDegraded, StateOutage, StateMaintenance, StateNoData}

// Impact is how badly an incident affects users.
type Impact string

// The incident impacts.
const (
	ImpactMinor    Impact = "minor"
	ImpactMajor    Impact = "major"
	ImpactCritical Impact = "critical"
)

// IncidentStatus is an incident's lifecycle step, and the step each of its
// updates announced.
type IncidentStatus string

// The incident lifecycle.
const (
	IncidentInvestigating IncidentStatus = "investigating"
	IncidentIdentified    IncidentStatus = "identified"
	IncidentMonitoring    IncidentStatus = "monitoring"
	IncidentResolved      IncidentStatus = "resolved"
)

// MaintenanceStatus is a maintenance window's lifecycle step, and the step
// each of its updates announced.
type MaintenanceStatus string

// The maintenance lifecycle.
const (
	MaintenanceScheduled  MaintenanceStatus = "scheduled"
	MaintenanceInProgress MaintenanceStatus = "in_progress"
	MaintenanceCompleted  MaintenanceStatus = "completed"
)

// Document is a status page: the forge.status/v1 JSON document, status.json.
//
// Every field here is public. See the package documentation for what never
// is; TestDocumentFieldsArePinned fails when a field is added until it is
// classified.
type Document struct {
	// Schema is [Schema].
	Schema string `json:"schema"`
	// GeneratedAt is when the publisher built the document. A visitor tells
	// a live page from a frozen one by it.
	GeneratedAt time.Time `json:"generated_at"`
	// Page is what the page says about itself.
	Page Page `json:"page"`
	// Components are the page's rows, in display order.
	Components []Component `json:"components"`
	// Incidents are the published incidents the page shows: the open ones
	// and recent history.
	Incidents []Incident `json:"incidents,omitempty"`
	// Maintenance are the published maintenance windows: upcoming, active
	// and recent.
	Maintenance []Maintenance `json:"maintenance,omitempty"`
}

// Page is the page's own presentation.
type Page struct {
	// Title names the page ("Acme status").
	Title string `json:"title"`
	// URL is the page's own public address (https://status.acme.com/). The
	// feed's links and entry ids are built from it; a page without one (a
	// local preview) links relatively.
	URL string `json:"url,omitempty"`
	// LogoURL is an https image shown beside the title.
	LogoURL string `json:"logo_url,omitempty"`
	// AccentColor is a #rrggbb colour for the page's decoration. It is never
	// a text or state colour, so it cannot break the page's contrast.
	AccentColor string `json:"accent_color,omitempty"`
	// Links are shown in the page header (support, the app, a blog).
	Links []Link `json:"links,omitempty"`
}

// Link is one header link.
type Link struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Component is one row of the page: a public name for one or more checks.
type Component struct {
	// Name is unique within the page.
	Name string `json:"name"`
	// Description is a short line under the name.
	Description string `json:"description,omitempty"`
	// State is the current state.
	State State `json:"state"`
	// Overridden is true when a person set State instead of the probes.
	// The page says so.
	Overridden bool `json:"overridden,omitempty"`
	// Days are the daily availability bars, oldest first, one per UTC day,
	// at most [MaxDays].
	Days []Day `json:"days"`
}

// Day is one daily bar.
type Day struct {
	// Date is the UTC day, YYYY-MM-DD.
	Date string `json:"date"`
	// Availability is the share of the day's probes that passed, 0 to 1,
	// probes inside a published maintenance window excluded. Null when the
	// day has no probes: the bar is grey, never green.
	Availability *float64 `json:"availability"`
	// Maintenance is true when a published maintenance window covered part
	// of the day.
	Maintenance bool `json:"maintenance,omitempty"`
}

// Incident is a published incident notice. It is a communication, not a
// detection: a measured outage shows as a component's state on its own.
type Incident struct {
	// ID is stable for the incident's life; the page anchors and the feed's
	// entry ids are built from it. Letters, digits, '-' and '_'.
	ID string `json:"id"`
	// Title is the incident's headline.
	Title string `json:"title"`
	// Impact is how badly users are affected.
	Impact Impact `json:"impact"`
	// Status is the current lifecycle step: the newest update's.
	Status IncidentStatus `json:"status"`
	// Components names the affected components ([Component.Name]).
	Components []string `json:"components,omitempty"`
	// StartedAt is when the incident began.
	StartedAt time.Time `json:"started_at"`
	// ResolvedAt is set exactly when Status is resolved.
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	// Updates are the published updates, oldest first. At least one: an
	// incident is published with its first update.
	Updates []IncidentUpdate `json:"updates"`
}

// IncidentUpdate is one published update of an incident.
type IncidentUpdate struct {
	Status IncidentStatus `json:"status"`
	// Body is plain text; blank lines separate paragraphs.
	Body string    `json:"body"`
	At   time.Time `json:"at"`
}

// Maintenance is a published maintenance window.
type Maintenance struct {
	// ID is stable for the window's life, like [Incident.ID].
	ID         string            `json:"id"`
	Title      string            `json:"title"`
	Status     MaintenanceStatus `json:"status"`
	Components []string          `json:"components,omitempty"`
	StartsAt   time.Time         `json:"starts_at"`
	EndsAt     time.Time         `json:"ends_at"`
	// Updates are the published updates, oldest first. At least one: a
	// window is announced with its first update (usually "scheduled").
	Updates []MaintenanceUpdate `json:"updates"`
}

// MaintenanceUpdate is one published update of a maintenance window.
type MaintenanceUpdate struct {
	Status MaintenanceStatus `json:"status"`
	Body   string            `json:"body"`
	At     time.Time         `json:"at"`
}
