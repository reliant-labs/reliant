// Copyright (c) 2025 Reliant Labs
package modelendpoints

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/localprobe"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/netguard"
)

// ErrDaemonOffline means the machine that should reach the endpoint is not
// connected right now.
var ErrDaemonOffline = errors.New("that machine is offline")

// ErrDaemonNotOwned means the daemon does not exist or belongs to someone
// else; the two are indistinguishable on purpose.
var ErrDaemonNotOwned = errors.New("machine not found")

// Store is the slice of db.Repository the manager uses. Declared here, at the
// consumer.
type Store interface {
	CreateModelEndpoint(ctx context.Context, e *db.ModelEndpoint) error
	GetModelEndpoint(ctx context.Context, userID, id string) (*db.ModelEndpoint, error)
	ListModelEndpoints(ctx context.Context, userID string) ([]*db.ModelEndpoint, error)
	UpdateModelEndpoint(ctx context.Context, e *db.ModelEndpoint) error
	SetModelEndpointProbe(ctx context.Context, userID, id, probeJSON string) error
	DeleteModelEndpoint(ctx context.Context, userID, id string) error
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
}

// DaemonControl is how the manager reaches a user's daemon. It is two calls
// the daemon registry already exposes (RefreshLocalModels and
// SetLocalModelEndpoints); the production adapter lives next to the handler.
type DaemonControl interface {
	// Refresh makes the daemon re-probe and returns its fresh inventory.
	// ErrDaemonOffline when it is not connected.
	Refresh(ctx context.Context, userID, daemonID string) (*reliantv1.LocalModelInventory, error)
	// SetConfiguredEndpoints REPLACES the daemon's user-configured base URLs
	// with exactly baseURLs, re-probes and returns the fresh inventory.
	SetConfiguredEndpoints(ctx context.Context, userID, daemonID string, baseURLs []string) (*reliantv1.LocalModelInventory, error)
}

// Config wires a Manager.
type Config struct {
	Store       Store
	Daemons     DaemonControl
	Credentials EndpointCredentials
	// Policy governs DIRECT endpoints. Hosted deployments must leave
	// AllowPrivate false.
	Policy netguard.Policy
	// ProbeClient, when set, replaces the guarded network client for DIRECT
	// probes. Tests only.
	ProbeClient *http.Client
	Now         func() time.Time
}

// Manager owns custom model endpoints: validation, route policy, probing and
// the daemon-side authorization of VIA_DAEMON endpoints.
type Manager struct {
	cfg Config
}

// NewManager returns a Manager. Credentials defaults to
// NotAvailableCredentials.
func NewManager(cfg Config) *Manager {
	if cfg.Credentials == nil {
		cfg.Credentials = NotAvailableCredentials{}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Manager{cfg: cfg}
}

const probeBudget = 25 * time.Second

// ---- reads ----

// List returns the user's endpoints as the API presents them.
func (m *Manager) List(ctx context.Context, userID string) ([]*reliantv1.ModelEndpoint, error) {
	rows, err := m.cfg.Store.ListModelEndpoints(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]*reliantv1.ModelEndpoint, 0, len(rows))
	for _, row := range rows {
		out = append(out, m.toProto(ctx, row))
	}
	return out, nil
}

func (m *Manager) toProto(ctx context.Context, row *db.ModelEndpoint) *reliantv1.ModelEndpoint {
	out := &reliantv1.ModelEndpoint{
		Id: row.ID, Name: row.Name, BaseUrl: row.BaseURL, Route: routeFromDB(row.Route),
		HeaderNames: append([]string(nil), row.HeaderNames...),
		Probe:       local.DecodeProbe(row.ProbeJSON),
	}
	if row.DaemonID != nil {
		out.DaemonId = *row.DaemonID
	}
	if row.CredentialConnectionID != nil {
		if key, _, err := m.cfg.Credentials.Get(ctx, row.UserID, *row.CredentialConnectionID); err == nil && key != "" {
			out.HasApiKey, out.MaskedApiKey = true, maskKey(key)
		}
	}
	configured, err := local.DecodeEndpointModels(row.ModelsJSON)
	if err != nil {
		logging.Warn("[ModelEndpoints] Unreadable stored models", "endpoint", row.ID, "error", err)
	}
	out.Models = mergeDiscovered(configured, out.Probe)
	return out
}

// mergeDiscovered returns the user's model settings plus a default entry for
// every probed chat model they have not configured, so the UI lists every
// model the server serves.
func mergeDiscovered(configured []*reliantv1.ModelEndpointModel, probe *reliantv1.LocalModelEndpoint) []*reliantv1.ModelEndpointModel {
	out := append([]*reliantv1.ModelEndpointModel(nil), configured...)
	have := map[string]bool{}
	for _, c := range configured {
		have[c.GetName()] = true
	}
	for _, info := range probe.GetModels() {
		if info.GetSupportsChat() && !have[info.GetName()] {
			out = append(out, &reliantv1.ModelEndpointModel{Name: info.GetName()})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return out
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "••••"
	}
	return key[:3] + "…" + key[len(key)-4:]
}

// ---- writes ----

// Create validates, stores and probes a new endpoint. A failing probe does not
// prevent saving: the server may simply be down right now.
func (m *Manager) Create(ctx context.Context, userID string, in *reliantv1.ModelEndpointInput) (*reliantv1.ModelEndpoint, error) {
	n, err := validateInput(in)
	if err != nil {
		return nil, err
	}
	if err := m.checkRoute(ctx, userID, n); err != nil {
		return nil, err
	}
	existing, err := m.cfg.Store.ListModelEndpoints(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(existing) >= maxEndpointsPer {
		return nil, invalid("You've reached the limit of %d custom endpoints.", maxEndpointsPer)
	}

	row := &db.ModelEndpoint{
		ID: uuid.NewString(), UserID: userID, Name: n.Name, BaseURL: n.BaseURL, Route: n.Route,
	}
	if n.DaemonID != "" {
		row.DaemonID = &n.DaemonID
	}
	row.ModelsJSON, err = local.EncodeEndpointModels(n.Models)
	if err != nil {
		return nil, err
	}
	connID, names, err := m.applyCredentials(ctx, userID, row.ID, nil, nil, in)
	if err != nil {
		return nil, err
	}
	row.CredentialConnectionID, row.HeaderNames = connID, names

	probe, _ := m.probeRow(ctx, row, in)
	row.ProbeJSON = local.EncodeProbe(probe)
	if err := m.cfg.Store.CreateModelEndpoint(ctx, row); err != nil {
		// The credential was sealed for an endpoint that never got stored.
		if connID != nil {
			_ = m.cfg.Credentials.Delete(ctx, userID, *connID)
		}
		return nil, err
	}
	return m.toProto(ctx, row), nil
}

// Update replaces an endpoint's settings and re-probes it.
func (m *Manager) Update(ctx context.Context, userID, id string, in *reliantv1.ModelEndpointInput) (*reliantv1.ModelEndpoint, error) {
	row, err := m.cfg.Store.GetModelEndpoint(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	n, err := validateInput(in)
	if err != nil {
		return nil, err
	}
	if err := m.checkRoute(ctx, userID, n); err != nil {
		return nil, err
	}
	oldDaemonID, oldBase := derefStr(row.DaemonID), row.BaseURL

	connID, names, err := m.applyCredentials(ctx, userID, row.ID, row.CredentialConnectionID, row.HeaderNames, in)
	if err != nil {
		return nil, err
	}
	row.Name, row.BaseURL, row.Route = n.Name, n.BaseURL, n.Route
	row.DaemonID = nil
	if n.DaemonID != "" {
		row.DaemonID = &n.DaemonID
	}
	row.CredentialConnectionID, row.HeaderNames = connID, names
	if row.ModelsJSON, err = local.EncodeEndpointModels(n.Models); err != nil {
		return nil, err
	}

	probe, _ := m.probeRow(ctx, row, in)
	row.ProbeJSON = local.EncodeProbe(probe)
	if err := m.cfg.Store.UpdateModelEndpoint(ctx, row); err != nil {
		return nil, err
	}

	// Moved off a daemon (or changed URL on it): drop the stale authorization.
	if oldDaemonID != "" && (oldDaemonID != n.DaemonID || oldBase != n.BaseURL) {
		if err := m.revokeFrom(ctx, userID, oldDaemonID, oldBase); err != nil && !errors.Is(err, ErrDaemonOffline) {
			logging.Warn("[ModelEndpoints] Could not revoke endpoint from previous machine", "daemonID", oldDaemonID, "error", err)
		}
	}
	return m.toProto(ctx, row), nil
}

// Delete removes an endpoint, its credential, and its authorization on the
// daemon that relayed it.
func (m *Manager) Delete(ctx context.Context, userID, id string) error {
	row, err := m.cfg.Store.GetModelEndpoint(ctx, userID, id)
	if err != nil {
		return err
	}
	if err := m.cfg.Store.DeleteModelEndpoint(ctx, userID, id); err != nil {
		return err
	}
	if row.CredentialConnectionID != nil {
		if err := m.cfg.Credentials.Delete(ctx, userID, *row.CredentialConnectionID); err != nil {
			logging.Warn("[ModelEndpoints] Could not delete endpoint credential", "endpoint", id, "error", err)
		}
	}
	if row.DaemonID != nil {
		if err := m.revokeFrom(ctx, userID, *row.DaemonID, row.BaseURL); err != nil && !errors.Is(err, ErrDaemonOffline) {
			logging.Warn("[ModelEndpoints] Could not revoke endpoint from machine", "daemonID", *row.DaemonID, "error", err)
		}
	}
	return nil
}

// Test probes a saved endpoint (id) or an unsaved draft, returning the probe
// and its round-trip time. A saved VIA_DAEMON endpoint is re-authorized on its
// machine first, which is what repairs one that was saved while offline.
func (m *Manager) Test(ctx context.Context, userID, id string, draft *reliantv1.ModelEndpointInput) (*reliantv1.LocalModelEndpoint, time.Duration, error) {
	start := m.cfg.Now()
	if id != "" {
		row, err := m.cfg.Store.GetModelEndpoint(ctx, userID, id)
		if err != nil {
			return nil, 0, err
		}
		probe, perr := m.probeRow(ctx, row, nil)
		if perr == nil {
			_ = m.cfg.Store.SetModelEndpointProbe(ctx, userID, id, local.EncodeProbe(probe))
		}
		return probe, m.cfg.Now().Sub(start), perr
	}

	n, err := validateInput(draft)
	if err != nil {
		return nil, 0, err
	}
	if err := m.checkRoute(ctx, userID, n); err != nil {
		return nil, 0, err
	}
	if _, isNA := m.cfg.Credentials.(NotAvailableCredentials); isNA && hasSecrets(draft) {
		return nil, 0, ErrCredentialStoreUnavailable
	}
	row := &db.ModelEndpoint{UserID: userID, Name: n.Name, BaseURL: n.BaseURL, Route: n.Route}
	if n.DaemonID != "" {
		row.DaemonID = &n.DaemonID
	}
	probe, perr := m.probeRow(ctx, row, draft)
	return probe, m.cfg.Now().Sub(start), perr
}

// ---- policy ----

// checkRoute enforces the route rules that need I/O: a VIA_DAEMON daemon must
// belong to the user, and a DIRECT endpoint must be one Reliant's servers are
// allowed to dial.
func (m *Manager) checkRoute(ctx context.Context, userID string, n *normalizedInput) error {
	switch n.Route {
	case db.ModelEndpointRouteViaDaemon:
		d, err := m.cfg.Store.GetDaemon(ctx, n.DaemonID)
		if err != nil || d == nil || d.UserID != userID {
			return ErrDaemonNotOwned
		}
	case db.ModelEndpointRouteDirect:
		if err := m.cfg.Policy.CheckURL(ctx, n.BaseURL); err != nil {
			return invalid("%s", err.Error())
		}
	}
	return nil
}

// ---- credentials ----

func hasSecrets(in *reliantv1.ModelEndpointInput) bool {
	if in == nil {
		return false
	}
	if in.ApiKey != nil && in.GetApiKey() != "" {
		return true
	}
	for _, v := range in.GetHeaders() {
		if v != "" {
			return true
		}
	}
	return false
}

// applyCredentials stores the key and header VALUES in the credential store
// and returns the connection id and header NAMES to record on the row.
// Nothing secret is returned or kept here.
func (m *Manager) applyCredentials(ctx context.Context, userID, endpointID string, existingConn *string, existingNames []string, in *reliantv1.ModelEndpointInput) (*string, []string, error) {
	names := map[string]bool{}
	for _, n := range existingNames {
		names[n] = true
	}
	for k, v := range in.GetHeaders() {
		if v == "" {
			delete(names, k)
		} else {
			names[k] = true
		}
	}
	outNames := make([]string, 0, len(names))
	for n := range names {
		outNames = append(outNames, n)
	}
	sort.Strings(outNames)

	touched := in.ApiKey != nil || len(in.GetHeaders()) > 0
	if !touched {
		return existingConn, outNames, nil
	}
	connID, err := m.cfg.Credentials.Put(ctx, userID, endpointID, in.ApiKey, in.GetHeaders())
	if err != nil {
		return nil, nil, err
	}
	if connID == "" {
		return nil, outNames, nil
	}
	return &connID, outNames, nil
}

// ---- probing ----

// probeRow probes the endpoint a row describes. It always returns a probe
// (carrying Error when the server did not answer); the error is for the
// caller's caller to act on only when it matters, and is nil for "the server
// said no".
func (m *Manager) probeRow(ctx context.Context, row *db.ModelEndpoint, draft *reliantv1.ModelEndpointInput) (*reliantv1.LocalModelEndpoint, error) {
	ctx, cancel := context.WithTimeout(ctx, probeBudget)
	defer cancel()
	if row.Route == db.ModelEndpointRouteViaDaemon {
		return m.probeViaDaemon(ctx, row), nil
	}
	return m.probeDirect(ctx, row, draft), nil
}

func (m *Manager) probeDirect(ctx context.Context, row *db.ModelEndpoint, draft *reliantv1.ModelEndpointInput) *reliantv1.LocalModelEndpoint {
	root, err := localprobe.NormalizeRoot(row.BaseURL)
	if err != nil {
		return &reliantv1.LocalModelEndpoint{Id: row.ID, Kind: "openai_compatible", BaseUrl: row.BaseURL, Source: "configured", Error: err.Error()}
	}
	target := localprobe.Target{Root: root}
	switch {
	case row.CredentialConnectionID != nil:
		key, headers, err := m.cfg.Credentials.Get(ctx, row.UserID, *row.CredentialConnectionID)
		if err != nil {
			return &reliantv1.LocalModelEndpoint{Id: row.ID, Kind: "openai_compatible", BaseUrl: root + "/v1", Source: "configured", Error: "its stored credentials could not be read"}
		}
		target.APIKey, target.Headers = key, headers
	case draft != nil:
		if draft.GetApiKey() != "" {
			target.APIKey = draft.GetApiKey()
		}
		target.Headers = draft.GetHeaders()
	}
	client := m.cfg.ProbeClient
	if client == nil {
		client = &http.Client{Transport: m.cfg.Policy.Transport()}
	}
	return localprobe.Probe(ctx, client, row.ID, target, localprobe.Options{})
}

// probeViaDaemon authorizes the endpoint on its machine's relay and returns
// what the daemon's own prober saw.
func (m *Manager) probeViaDaemon(ctx context.Context, row *db.ModelEndpoint) *reliantv1.LocalModelEndpoint {
	daemonID := derefStr(row.DaemonID)
	root, rootErr := localprobe.NormalizeRoot(row.BaseURL)
	failed := func(msg string) *reliantv1.LocalModelEndpoint {
		return &reliantv1.LocalModelEndpoint{Id: row.ID, Kind: "openai_compatible", BaseUrl: row.BaseURL, Source: "configured", Error: msg}
	}
	if rootErr != nil {
		return failed(rootErr.Error())
	}
	if m.cfg.Daemons == nil {
		return failed("machine relay is not available on this server")
	}

	// A row being created is not stored yet; the sync reads the stored rows,
	// so include this one explicitly.
	inv, err := m.syncDaemonWith(ctx, row.UserID, daemonID, row, nil)
	if errors.Is(err, ErrDaemonOffline) {
		return failed("That machine is offline. The endpoint is saved and will be reachable once it reconnects and you run Test connection.")
	}
	if err != nil {
		return failed(localprobe.ShortError(err))
	}
	id := localprobe.EndpointID(root)
	for _, ep := range inv.GetEndpoints() {
		if ep.GetId() == id {
			out := proto.Clone(ep).(*reliantv1.LocalModelEndpoint)
			out.Id = row.ID
			return out
		}
	}
	return failed("The machine did not report this endpoint.")
}

// ---- VIA_DAEMON relay authorization ----

// DesiredURLs computes the complete set of user-configured base URLs a daemon
// should hold: what is already configured there (the user's hand-added
// endpoints in the daemon-local Settings) plus every VIA_DAEMON endpoint the
// user has pointed at that daemon. SetLocalModelEndpoints REPLACES the daemon's
// list, so sending only ours would silently delete the hand-added ones.
func desiredURLs(current []string, rows []*db.ModelEndpoint, daemonID string, extra *db.ModelEndpoint, drop map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	add := func(u string) {
		root, err := localprobe.NormalizeRoot(u)
		if err != nil || seen[root] {
			return
		}
		seen[root] = true
		out = append(out, u)
	}
	for _, u := range current {
		if root, err := localprobe.NormalizeRoot(u); err == nil && drop[root] {
			continue
		}
		add(u)
	}
	include := func(r *db.ModelEndpoint) {
		if r.Route == db.ModelEndpointRouteViaDaemon && derefStr(r.DaemonID) == daemonID {
			add(r.BaseURL)
		}
	}
	for _, r := range rows {
		if extra != nil && r.ID == extra.ID {
			continue
		}
		include(r)
	}
	if extra != nil {
		include(extra)
	}
	return out
}

func configuredURLs(inv *reliantv1.LocalModelInventory) []string {
	var out []string
	for _, ep := range inv.GetEndpoints() {
		if ep.GetSource() == "configured" {
			out = append(out, ep.GetBaseUrl())
		}
	}
	return out
}

// SyncDaemon makes daemonID's relay authorize exactly the endpoints the user
// has stored for it (plus what was already configured there). Idempotent.
func (m *Manager) SyncDaemon(ctx context.Context, userID, daemonID string) (*reliantv1.LocalModelInventory, error) {
	return m.syncDaemon(ctx, userID, daemonID)
}

func (m *Manager) syncDaemon(ctx context.Context, userID, daemonID string) (*reliantv1.LocalModelInventory, error) {
	return m.syncDaemonWith(ctx, userID, daemonID, nil, nil)
}

// revokeFrom re-syncs daemonID after an endpoint stopped pointing at
// oldBaseURL there. Only that URL is dropped, and only if no stored endpoint
// still needs it; anything the user typed into the daemon's own settings stays.
func (m *Manager) revokeFrom(ctx context.Context, userID, daemonID, oldBaseURL string) error {
	drop := map[string]bool{}
	if root, err := localprobe.NormalizeRoot(oldBaseURL); err == nil {
		drop[root] = true
	}
	_, err := m.syncDaemonWith(ctx, userID, daemonID, nil, drop)
	return err
}

func (m *Manager) syncDaemonWith(ctx context.Context, userID, daemonID string, pending *db.ModelEndpoint, drop map[string]bool) (*reliantv1.LocalModelInventory, error) {
	if m.cfg.Daemons == nil {
		return nil, errors.New("machine relay is not available on this server")
	}
	rows, err := m.cfg.Store.ListModelEndpoints(ctx, userID)
	if err != nil {
		return nil, err
	}
	inv, err := m.cfg.Daemons.Refresh(ctx, userID, daemonID)
	if err != nil {
		return nil, err
	}
	current := configuredURLs(inv)
	want := desiredURLs(current, rows, daemonID, pending, drop)
	if sameSet(current, want) {
		return inv, nil
	}
	return m.cfg.Daemons.SetConfiguredEndpoints(ctx, userID, daemonID, want)
}

func sameSet(a, b []string) bool {
	norm := func(in []string) map[string]bool {
		out := map[string]bool{}
		for _, u := range in {
			if r, err := localprobe.NormalizeRoot(u); err == nil {
				out[r] = true
			}
		}
		return out
	}
	na, nb := norm(a), norm(b)
	if len(na) != len(nb) {
		return false
	}
	for k := range na {
		if !nb[k] {
			return false
		}
	}
	return true
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}
