// Copyright (c) 2025 Reliant Labs
package local

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

// A local model is a property of a user's daemon, never of the process: it is
// synthesized per request from the inventory the daemon published, and carried
// to the driver as an http.RoundTripper that relays through that daemon. Nothing
// here registers into a global registry.

const (
	// PlaceholderBaseURL is the driver's base URL when a relay transport is set.
	// It is never dialed; the transport carries every request.
	PlaceholderBaseURL = "http://local-model.invalid/v1"

	// ProviderPrefix begins a ModelSelector provider that names a daemon:
	// "local:<daemonID>".
	ProviderPrefix = "local:"

	// IDSuffix is the driver suffix of a local model's catalog id: "<name>@local".
	IDSuffix = "local"

	// fallbackContextWindow applies when the daemon could not determine the
	// window. Conservative on purpose: overestimating makes compaction fire
	// after the server has already truncated.
	fallbackContextWindow = 8192

	// onlineStaleThreshold is how old a daemon's attachment lease may be and
	// still count as online (6 missed 15s heartbeats; matches the daemon
	// registry and daemonliveness.DefaultStaleThreshold).
	onlineStaleThreshold = 90 * time.Second
)

// DaemonInventory is one daemon's published local models plus its liveness.
type DaemonInventory struct {
	DaemonID  string
	Machine   string
	Online    bool
	Inventory *reliantv1.LocalModelInventory
}

// Directory lists a user's daemons that published local model inventories.
// Declared here, at the consumer; RepoDirectory is the production one.
type Directory interface {
	LocalDaemons(ctx context.Context, userID string) ([]DaemonInventory, error)
}

// TransportFactory returns the RoundTripper that relays to endpointID on
// daemonID for userID (toolexec.NewLocalModelTransport in production).
type TransportFactory func(userID, daemonID, endpointID string) http.RoundTripper

// Model is one chat-capable model on one daemon's endpoint.
type Model struct {
	Definition   models.ModelDefinition
	DaemonID     string
	Machine      string
	Online       bool
	EndpointID   string
	EndpointKind string
	Info         *reliantv1.LocalModelInfo

	// Custom is set for a model served by a user-configured endpoint
	// (model_endpoints) rather than one a daemon detected. For those, DaemonID
	// is the relaying machine (VIA_DAEMON) or empty (DIRECT), and EndpointID is
	// the id that daemon's relay knows the endpoint by.
	Custom *CustomEndpoint
}

// CatalogID is the id the picker shows and a selector carries.
func (m Model) CatalogID() string { return m.Definition.ID + "@" + IDSuffix }

// Provider is the selector provider string that pins this model's source: its
// daemon for a detected model, its endpoint for a configured one.
func (m Model) Provider() string {
	if m.Custom != nil {
		return EndpointProviderPrefix + m.Custom.ID
	}
	return ProviderPrefix + m.DaemonID
}

// UnavailableError is the user-facing failure for a local model that cannot be
// used right now. It must never read like an API-key problem.
type UnavailableError struct {
	Model   string
	Machine string
	Reason  string
}

func (e *UnavailableError) Error() string {
	if e.Machine == "" {
		return fmt.Sprintf("Local model %s is unavailable: %s", e.Model, e.Reason)
	}
	return fmt.Sprintf("Local model %s on %s is unavailable: %s", e.Model, e.Machine, e.Reason)
}

// IsSelector reports whether sel names a local model: an id with the @local
// suffix, or a provider "local:<daemonID>". Tags never do.
func IsSelector(sel models.ModelSelector) bool {
	if _, suffix, ok := strings.Cut(strings.TrimSpace(sel.ID), "@"); ok && suffix == IDSuffix {
		return true
	}
	return pinnedDaemon(sel) != "" || pinnedEndpoint(sel) != ""
}

func pinnedEndpoint(sel models.ModelSelector) string {
	for _, p := range sel.Providers {
		if id, ok := strings.CutPrefix(p, EndpointProviderPrefix); ok && id != "" {
			return id
		}
	}
	return ""
}

func pinnedDaemon(sel models.ModelSelector) string {
	for _, p := range sel.Providers {
		if id, ok := strings.CutPrefix(p, ProviderPrefix); ok && id != "" {
			return id
		}
	}
	return ""
}

func selectorModelName(sel models.ModelSelector) string {
	name, _, _ := strings.Cut(sel.ID, "@")
	if i := strings.LastIndex(sel.ID, "@"); i >= 0 {
		name = sel.ID[:i]
	}
	return name
}

// Synthesize builds the models one daemon serves. Embedding-only models are
// skipped; capabilities are what the server itself reported.
func Synthesize(d DaemonInventory) []Model {
	var out []Model
	if d.Inventory == nil {
		return nil
	}
	for _, ep := range d.Inventory.GetEndpoints() {
		for _, info := range ep.GetModels() {
			if !info.GetSupportsChat() || strings.TrimSpace(info.GetName()) == "" {
				continue
			}
			out = append(out, Model{
				Definition:   definitionFor(ep.GetKind(), info),
				DaemonID:     d.DaemonID,
				Machine:      d.Machine,
				Online:       d.Online,
				EndpointID:   ep.GetId(),
				EndpointKind: ep.GetKind(),
				Info:         info,
			})
		}
	}
	return out
}

func definitionFor(kind string, info *reliantv1.LocalModelInfo) models.ModelDefinition {
	window := int(info.GetContextWindow())
	if window <= 0 {
		window = fallbackContextWindow
	}
	caps := models.ModelCapabilities{
		SupportsTools:       info.GetSupportsTools(),
		SupportsAttachments: info.GetSupportsVision(),
		SupportsStreaming:   true,
		MaxContextWindow:    window,
		MaxOutputTokens:     min(window/2, 16384),
	}
	if levels := thinkingLevelsFor(kind, info); len(levels) > 0 {
		caps.CanReason = true
		caps.ThinkingLevels = levels
	}
	return models.ModelDefinition{
		ID:           info.GetName(),
		Name:         info.GetName(),
		Visibility:   models.VisibilityUser,
		Capabilities: caps,
		Providers:    []models.ProviderMapping{{Driver: string(Family), APIModel: info.GetName()}},
	}
}

// thinkingLevelsFor is the set of reasoning_effort levels the server accepts
// for this model, empty when none (the driver then sends nothing).
//
// Ollama maps reasoning_effort onto its `think` parameter and answers 400 for
// a level the model does not take; qwen3 takes none, gpt-oss takes
// low/medium/high (live, Ollama 0.12.3). The inventory only says "can think",
// so Ollama gets levels for the one family known to accept them. Such models
// still think and the driver still surfaces it; they just offer no dial.
// Other servers ignore a level they do not understand, so a thinking model
// there gets the standard three.
func thinkingLevelsFor(kind string, info *reliantv1.LocalModelInfo) []string {
	if !info.GetSupportsThinking() {
		return nil
	}
	if kind == "ollama" && !strings.HasPrefix(strings.ToLower(info.GetName()), "gpt-oss") {
		return nil
	}
	return []string{"low", "medium", "high"}
}

// ListModels is every chat-capable model across the user's daemons, online or
// not, in a stable order.
func ListModels(ctx context.Context, dir Directory, userID string) ([]Model, error) {
	if dir == nil {
		return nil, nil
	}
	daemons, err := dir.LocalDaemons(ctx, userID)
	if err != nil {
		return nil, err
	}
	var all []Model
	for _, d := range daemons {
		all = append(all, Synthesize(d)...)
	}
	if lister, ok := dir.(EndpointLister); ok {
		endpoints, err := lister.ConfiguredEndpoints(ctx, userID)
		if err != nil {
			// Detected models stay usable when the endpoint list cannot be read.
			logging.Warn("[LocalModels] Could not list configured endpoints", "userID", userID, "error", err)
		}
		for _, cfg := range endpoints {
			all = append(all, SynthesizeEndpoint(cfg)...)
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Machine != all[j].Machine {
			return all[i].Machine < all[j].Machine
		}
		if all[i].DaemonID != all[j].DaemonID {
			return all[i].DaemonID < all[j].DaemonID
		}
		if ci, cj := customID(all[i]), customID(all[j]); ci != cj {
			return ci < cj
		}
		return all[i].Definition.ID < all[j].Definition.ID
	})
	return all, nil
}

// Resolve picks the model a selector names.
//
// A provider "local:<daemonID>" pins that daemon. Without one, the user's
// online daemon serving the model wins, preferring preferDaemonID (the chat's
// worktree daemon). A pinned or only-offline match yields UnavailableError so
// the user learns which machine to bring back.
func Resolve(all []Model, sel models.ModelSelector, preferDaemonID string) (*Model, error) {
	name := selectorModelName(sel)
	pinned := pinnedDaemon(sel)
	if endpointID := pinnedEndpoint(sel); endpointID != "" {
		return resolveEndpoint(all, name, endpointID)
	}
	// A daemon pin names a detected model: a configured endpoint relayed by the
	// same machine is a different thing and must not satisfy it. Unpinned, any
	// source may serve the name.
	if pinned != "" {
		var detected []Model
		for _, m := range all {
			if m.Custom == nil {
				detected = append(detected, m)
			}
		}
		all = detected
	}
	if name == "" {
		return nil, &UnavailableError{Reason: "no model was named"}
	}

	var matches []Model
	for _, m := range all {
		if m.Definition.ID == name && (pinned == "" || m.DaemonID == pinned) {
			matches = append(matches, m)
		}
	}
	if len(matches) == 0 {
		if pinned != "" {
			for _, m := range all {
				if m.DaemonID == pinned {
					return nil, &UnavailableError{Model: name, Machine: m.Machine, Reason: "that machine does not serve this model (is it pulled and is the server running?)"}
				}
			}
			return nil, &UnavailableError{Model: name, Reason: "that machine has not published any local models (is it connected and is a model server running?)"}
		}
		return nil, &UnavailableError{Model: name, Reason: "none of your machines serve it (is the model pulled and the server running?)"}
	}

	rank := func(m Model) int {
		switch {
		case m.Online && m.DaemonID == preferDaemonID:
			return 0
		case m.Online:
			return 1
		}
		return 2
	}
	sort.SliceStable(matches, func(i, j int) bool { return rank(matches[i]) < rank(matches[j]) })
	best := matches[0]
	if !best.Online {
		return nil, &UnavailableError{Model: name, Machine: best.Machine, Reason: "that machine is offline"}
	}
	return &best, nil
}

func customID(m Model) string {
	if m.Custom == nil {
		return ""
	}
	return m.Custom.ID
}

// resolveEndpoint picks the model a selector pinned to a configured endpoint
// names. Hidden models are absent from all, so a hidden model reads as gone.
func resolveEndpoint(all []Model, name, endpointID string) (*Model, error) {
	if name == "" {
		return nil, &UnavailableError{Reason: "no model was named"}
	}
	var endpointName string
	for _, m := range all {
		if m.Custom == nil || m.Custom.ID != endpointID {
			continue
		}
		endpointName = m.Custom.Name
		if m.Definition.ID != name {
			continue
		}
		if !m.Online {
			return nil, &UnavailableError{Model: name, Machine: machineOf(m), Reason: "the machine that reaches it is offline"}
		}
		picked := m
		return &picked, nil
	}
	if endpointName != "" {
		return nil, &UnavailableError{Model: name, Machine: endpointName, Reason: "that endpoint does not serve this model (is it hidden, or not loaded?)"}
	}
	return nil, &UnavailableError{Model: name, Reason: "that custom endpoint no longer exists or serves no models"}
}

func machineOf(m Model) string {
	if m.Custom != nil && m.Custom.Name != "" {
		return m.Custom.Name
	}
	return m.Machine
}

// Source is the slice of db.Repository RepoDirectory reads.
type Source interface {
	ListDaemonsByUserID(ctx context.Context, userID string) ([]*db.Daemon, error)
	ListDaemonLocalModels(ctx context.Context, userID string) (map[string]string, error)
	ListFreshDaemonAttachmentsForUser(ctx context.Context, userID string, staleThreshold time.Duration) ([]*db.DaemonAttachment, error)
}

// RepoDirectory reads inventories from the database; a daemon is online iff it
// holds a fresh attachment lease (the same rule the daemon registry uses).
type RepoDirectory struct{ src Source }

func NewRepoDirectory(src Source) *RepoDirectory { return &RepoDirectory{src: src} }

func (r *RepoDirectory) LocalDaemons(ctx context.Context, userID string) ([]DaemonInventory, error) {
	if r == nil || r.src == nil {
		return nil, errors.New("local model directory has no data source")
	}
	stored, err := r.src.ListDaemonLocalModels(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("listing local model inventories: %w", err)
	}
	if len(stored) == 0 {
		return nil, nil
	}
	daemons, err := r.src.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("listing daemons: %w", err)
	}
	attachments, err := r.src.ListFreshDaemonAttachmentsForUser(ctx, userID, onlineStaleThreshold)
	if err != nil {
		return nil, fmt.Errorf("listing daemon attachments: %w", err)
	}
	online := make(map[string]bool, len(attachments))
	for _, a := range attachments {
		online[a.DaemonID] = true
	}
	hostnames := make(map[string]string, len(daemons))
	for _, d := range daemons {
		if d.Hostname != nil {
			hostnames[d.ID] = *d.Hostname
		}
	}

	out := make([]DaemonInventory, 0, len(stored))
	for daemonID, raw := range stored {
		inv := &reliantv1.LocalModelInventory{}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(raw), inv); err != nil {
			logging.Warn("[LocalModels] Skipping unreadable stored inventory", "daemonID", daemonID, "error", err)
			continue
		}
		machine := hostnames[daemonID]
		if machine == "" {
			machine = daemonID
		}
		out = append(out, DaemonInventory{DaemonID: daemonID, Machine: machine, Online: online[daemonID], Inventory: inv})
	}
	return out, nil
}
