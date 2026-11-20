// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/localprobe"
)

// fakeModelCatalog is CatalogService as the model picker uses it.
type fakeModelCatalog struct {
	reliantv1connect.UnimplementedCatalogServiceHandler
	bearer string
	orgs   []string
}

func (f *fakeModelCatalog) ListModels(_ context.Context, req *connect.Request[reliantv1.ListModelsRequest]) (*connect.Response[reliantv1.ListModelsResponse], error) {
	f.bearer = req.Header().Get("Authorization")
	f.orgs = append(f.orgs, req.Msg.GetOrgId())
	return connect.NewResponse(&reliantv1.ListModelsResponse{Models: []*reliantv1.ModelInfo{
		{Id: "claude-4-sonnet@anthropic", Name: "Claude Sonnet", Provider: "Anthropic", DriverId: "anthropic", ContextWindow: 200000, SupportsTools: true},
		{Id: "qwen3:latest@local", Name: "qwen3:latest", Provider: "Local", DriverId: "local", ContextWindow: 65536, SupportsTools: true,
			Local: &reliantv1.LocalModelSource{DaemonId: "d-laptop", MachineName: "laptop", EndpointId: "ollama", EndpointKind: "ollama", Online: true}},
		{Id: "llama3@local", Name: "llama3", Provider: "Local", DriverId: "local", ContextWindow: 8192,
			Local: &reliantv1.LocalModelSource{MachineName: "Lab GPU", EndpointId: "ep-1", EndpointKind: "openai_compatible", Online: false}},
	}}), nil
}

// fakeModelsRegistry is DaemonRegistryService for Local & custom, over
// machines that behave like real daemons: each keeps its configured base URLs
// as sent, and reports each as the daemon's prober does,
// localprobe.NormalizeRoot(url)+"/v1" (internal/localprobe).
type fakeModelsRegistry struct {
	reliantv1connect.UnimplementedDaemonRegistryServiceHandler
	daemons []*reliantv1.DaemonInfo
	// configured is each machine's configured list, by daemon id.
	configured map[string][]string
	// storedMissing makes ListDaemons return no stored inventory, as it does
	// when the stored copy cannot be read.
	storedMissing bool
	sets          []*reliantv1.SetLocalModelEndpointsRequest
	offline       bool
}

func (f *fakeModelsRegistry) inventory(daemonID string) *reliantv1.LocalModelInventory {
	inv := &reliantv1.LocalModelInventory{}
	if daemonID == "d-laptop" {
		inv.Endpoints = append(inv.Endpoints, &reliantv1.LocalModelEndpoint{Id: "ollama", Kind: "ollama", BaseUrl: "http://localhost:11434/v1", Source: "detected",
			Models: []*reliantv1.LocalModelInfo{{Name: "qwen3:latest", SupportsChat: true}, {Name: "nomic-embed", SupportsChat: false}}})
	}
	for _, u := range f.configured[daemonID] {
		root, _ := localprobe.NormalizeRoot(u)
		ep := &reliantv1.LocalModelEndpoint{Id: localprobe.EndpointID(root), Kind: "vllm", BaseUrl: root + "/v1", Source: "configured"}
		switch {
		case strings.Contains(u, "down"):
			ep.Error = "connection refused"
		case strings.Contains(u, "gpu-box"):
			ep.Error = "timeout"
		default:
			ep.Models = []*reliantv1.LocalModelInfo{{Name: "mistral", SupportsChat: true}, {Name: "embed", SupportsChat: false}}
		}
		inv.Endpoints = append(inv.Endpoints, ep)
	}
	return inv
}

func (f *fakeModelsRegistry) ListDaemons(context.Context, *connect.Request[reliantv1.ListDaemonsRequest]) (*connect.Response[reliantv1.ListDaemonsResponse], error) {
	out := make([]*reliantv1.DaemonInfo, 0, len(f.daemons))
	for _, d := range f.daemons {
		c := proto.Clone(d).(*reliantv1.DaemonInfo)
		if !f.storedMissing {
			c.LocalModels = f.inventory(d.GetDaemonId())
		}
		out = append(out, c)
	}
	return connect.NewResponse(&reliantv1.ListDaemonsResponse{Daemons: out}), nil
}

func (f *fakeModelsRegistry) RefreshLocalModels(_ context.Context, req *connect.Request[reliantv1.RefreshLocalModelsRequest]) (*connect.Response[reliantv1.RefreshLocalModelsResponse], error) {
	if f.offline {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("daemon is offline"))
	}
	return connect.NewResponse(&reliantv1.RefreshLocalModelsResponse{LocalModels: f.inventory(req.Msg.GetDaemonId())}), nil
}

func (f *fakeModelsRegistry) SetLocalModelEndpoints(_ context.Context, req *connect.Request[reliantv1.SetLocalModelEndpointsRequest]) (*connect.Response[reliantv1.SetLocalModelEndpointsResponse], error) {
	if f.offline {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("daemon is offline"))
	}
	f.sets = append(f.sets, req.Msg)
	f.configured[req.Msg.GetDaemonId()] = req.Msg.GetBaseUrls()
	return connect.NewResponse(&reliantv1.SetLocalModelEndpointsResponse{LocalModels: f.inventory(req.Msg.GetDaemonId())}), nil
}

type fakeCustomEndpoints struct {
	reliantv1connect.UnimplementedModelEndpointServiceHandler
}

func (fakeCustomEndpoints) ListModelEndpoints(context.Context, *connect.Request[reliantv1.ListModelEndpointsRequest]) (*connect.Response[reliantv1.ListModelEndpointsResponse], error) {
	return connect.NewResponse(&reliantv1.ListModelEndpointsResponse{Endpoints: []*reliantv1.ModelEndpoint{
		{Id: "ep-1", Name: "Lab GPU", BaseUrl: "https://llm.example.test/v1", Route: reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_DIRECT,
			Models: []*reliantv1.ModelEndpointModel{{Name: "llama3"}, {Name: "secret", Hidden: true}},
			Probe:  &reliantv1.LocalModelEndpoint{Kind: "openai_compatible"}},
		{Id: "ep-2", Name: "VPN box", BaseUrl: "http://vpn-box.internal:8000/v1", Route: reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_VIA_DAEMON,
			DaemonId: "d-laptop", Probe: &reliantv1.LocalModelEndpoint{Kind: "vllm"}},
	}}), nil
}

func laptopDaemon() *reliantv1.DaemonInfo {
	return &reliantv1.DaemonInfo{DaemonId: "d-laptop", Name: "laptop", Status: reliantv1.DaemonStatus_DAEMON_STATUS_ACTIVE}
}

func newModelsHarness(t *testing.T, daemons ...*reliantv1.DaemonInfo) (*fakeModelCatalog, *fakeModelsRegistry) {
	t.Helper()
	isolateCLI(t)
	catalog := &fakeModelCatalog{}
	registry := &fakeModelsRegistry{daemons: daemons, configured: map[string][]string{"d-laptop": {"http://gpu-box.lan:8000/v1/"}}}
	mux := http.NewServeMux()
	p1, h1 := reliantv1connect.NewCatalogServiceHandler(catalog)
	mux.Handle(p1, h1)
	p2, h2 := reliantv1connect.NewDaemonRegistryServiceHandler(registry)
	mux.Handle(p2, h2)
	p3, h3 := reliantv1connect.NewModelEndpointServiceHandler(fakeCustomEndpoints{})
	mux.Handle(p3, h3)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv(envServerURL, srv.URL)
	t.Setenv(envToken, "rlat_login")
	t.Setenv(daemonpolicy.AgentShellEnv, "")
	return catalog, registry
}

func TestModelsListLocalShowsTheMachineAndPin(t *testing.T) {
	catalog, _ := newModelsHarness(t)

	out, err := executeRoot(t, "models", "list", "--local")
	require.NoError(t, err)
	assert.Equal(t, "Bearer rlat_login", catalog.bearer)
	assert.NotContains(t, out, "claude-4-sonnet", "--local lists only local models")
	assert.Regexp(t, `qwen3:latest@local\s+laptop\s+ollama\s+yes\s+64K\s+yes\s+local:d-laptop`, out)
	assert.Regexp(t, `llama3@local\s+Lab GPU\s+openai_compatible\s+no\s+8K\s+no\s+endpoint:ep-1`, out)
	assert.Contains(t, out, `providers: ["<pin>"]`)

	out, err = executeRoot(t, "models", "list")
	require.NoError(t, err)
	assert.Regexp(t, `claude-4-sonnet@anthropic\s+Anthropic\s+200K\s+yes`, out)
	assert.Contains(t, out, "qwen3:latest@local")

	out, err = executeRoot(t, "models", "list", "--local", "--json", "--org", "org-acme")
	require.NoError(t, err)
	assert.Equal(t, "org-acme", catalog.orgs[len(catalog.orgs)-1])
	var doc struct {
		Models []modelRow `json:"models"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &doc), out)
	require.Len(t, doc.Models, 2)
	assert.Equal(t, "llama3@local", doc.Models[0].ID)
	assert.Equal(t, &localModelRow{Machine: "laptop", DaemonID: "d-laptop", EndpointID: "ollama", EndpointKind: "ollama", Online: true, Pin: "local:d-laptop"}, doc.Models[1].Local)
}

func TestModelsEndpointList(t *testing.T) {
	newModelsHarness(t, laptopDaemon())

	out, err := executeRoot(t, "models", "endpoint", "list")
	require.NoError(t, err)
	assert.Regexp(t, `laptop\s+ollama\s+http://localhost:11434/v1\s+detected\s+qwen3:latest\n`, out, "embedding-only models are not offered")
	assert.Regexp(t, `laptop\s+vllm\s+http://gpu-box.lan:8000/v1\s+configured\s+unreachable: timeout`, out)
	assert.Regexp(t, `Lab GPU\s+openai_compatible\s+https://llm.example.test/v1\s+custom\s+llama3\n`, out, "hidden models are left out")
}

func TestModelsEndpointAddAndRemove(t *testing.T) {
	_, registry := newModelsHarness(t, laptopDaemon())

	// A URL without /v1: the daemon reports it as <root>/v1.
	out, err := executeRoot(t, "models", "endpoint", "add", "http://localhost:9000/")
	require.NoError(t, err)
	require.Len(t, registry.sets, 1)
	assert.Equal(t, "d-laptop", registry.sets[0].GetDaemonId())
	assert.Equal(t, []string{"http://gpu-box.lan:8000/v1", "http://localhost:9000"}, registry.sets[0].GetBaseUrls(),
		"the machine's configured endpoints are kept, and the new one is added")
	assert.Contains(t, out, "Added http://localhost:9000 on laptop.")
	assert.Contains(t, out, "It serves: mistral@local")

	// The same server, spelled with /v1 as the inventory shows it, is a no-op.
	out, err = executeRoot(t, "models", "endpoint", "add", "http://localhost:9000/v1", "--machine", "laptop")
	require.NoError(t, err)
	assert.Contains(t, out, "laptop already looks for http://localhost:9000/v1")
	assert.Len(t, registry.sets, 1)

	// Removing it by the URL it was added with finds it in the canonical
	// inventory.
	out, err = executeRoot(t, "models", "endpoint", "remove", "http://localhost:9000", "--machine", "d-laptop")
	require.NoError(t, err)
	assert.Contains(t, out, "Removed http://localhost:9000 on laptop.")
	require.Len(t, registry.sets, 2)
	assert.Equal(t, []string{"http://gpu-box.lan:8000/v1"}, registry.sets[1].GetBaseUrls())

	_, err = executeRoot(t, "models", "endpoint", "remove", "http://localhost:11434/v1")
	assert.ErrorContains(t, err, "was not added to laptop")

	_, err = executeRoot(t, "models", "endpoint", "add", "localhost:9000")
	assert.ErrorContains(t, err, "is not a model server URL")
}

// The new list is built from what the machine itself reports, never from the
// stored copy: a missing stored inventory must not turn an add into the
// deletion of every endpoint the machine had.
func TestModelsEndpointAddKeepsEndpointsTheStoredCopyLacks(t *testing.T) {
	_, registry := newModelsHarness(t, laptopDaemon())
	registry.storedMissing = true

	_, err := executeRoot(t, "models", "endpoint", "add", "http://localhost:9000/v1")
	require.NoError(t, err)
	require.Len(t, registry.sets, 1)
	assert.Equal(t, []string{"http://gpu-box.lan:8000/v1", "http://localhost:9000/v1"}, registry.sets[0].GetBaseUrls())

	// A machine that cannot be read is not written.
	registry.offline = true
	_, err = executeRoot(t, "models", "endpoint", "add", "http://localhost:9001/v1")
	assert.ErrorContains(t, err, "laptop must be online")
	assert.ErrorContains(t, err, "nothing was changed")
	assert.Len(t, registry.sets, 1)
}

// A URL a custom endpoint relays through the machine is that endpoint's, and
// is removed by deleting it.
func TestModelsEndpointRemoveRefusesACustomEndpointsRelay(t *testing.T) {
	_, registry := newModelsHarness(t, laptopDaemon())
	registry.configured["d-laptop"] = append(registry.configured["d-laptop"], "http://vpn-box.internal:8000/v1")

	_, err := executeRoot(t, "models", "endpoint", "remove", "http://vpn-box.internal:8000")
	assert.ErrorContains(t, err, `your custom endpoint "VPN box" reaches its server through laptop`)
	assert.Empty(t, registry.sets)
}

func TestModelsEndpointAddNeedsAMachineWhenThereAreSeveral(t *testing.T) {
	desk := &reliantv1.DaemonInfo{DaemonId: "d-desk", Hostname: "desk", Status: reliantv1.DaemonStatus_DAEMON_STATUS_ACTIVE}
	_, registry := newModelsHarness(t, laptopDaemon(), desk)

	_, err := executeRoot(t, "models", "endpoint", "add", "http://localhost:9000/v1")
	assert.ErrorContains(t, err, "you have 2 machines (desk, laptop): pass --machine")

	out, err := executeRoot(t, "models", "endpoint", "add", "http://down.lan:9000/v1", "--machine", "desk")
	require.NoError(t, err)
	assert.Contains(t, out, "Added http://down.lan:9000/v1 on desk.")
	assert.Contains(t, out, "desk could not reach it yet: connection refused")
	assert.Equal(t, []string{"http://down.lan:9000/v1"}, registry.sets[0].GetBaseUrls())

	// An agent's shell may list, and is refused changes before any call.
	t.Setenv(daemonpolicy.AgentShellEnv, "1")
	_, err = executeRoot(t, "models", "endpoint", "add", "http://localhost:9000/v1", "--machine", "desk")
	assert.ErrorIs(t, err, errAgentCannotChangeModelEndpoints)
	_, err = executeRoot(t, "models", "endpoint", "remove", "http://down.lan:9000/v1", "--machine", "desk")
	assert.ErrorIs(t, err, errAgentCannotChangeModelEndpoints)
	assert.Len(t, registry.sets, 1)
	_, err = executeRoot(t, "models", "list", "--local")
	assert.NoError(t, err)
}
