// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// `reliant models` shows the models a person's chats and workflow steps can
// run on, through the CatalogService the app's model picker uses, and manages
// the local model servers a machine looks for, through the DaemonRegistryService
// Settings -> AI providers -> Local & custom uses.
//
// A local model is reached through the machine whose daemon found it, so its
// id is "<name>@local" and the machine (or a configured endpoint) is what pins
// it: a ModelSelector provider "local:<daemon id>" or "endpoint:<id>"
// (internal/llm/drivers/local). `models list --local` prints both.
//
// An agent's shell may list, and is refused adding or removing an endpoint
// (refuseModelsFromAgentShell): that changes what the user's machine connects
// to, which is the owner's call.

var errAgentCannotChangeModelEndpoints = errors.New("an agent cannot add or remove a machine's local model endpoints from its shell: ask the user to do it in Settings -> AI providers -> Local & custom, or with `reliant models endpoint add`")

func refuseModelsFromAgentShell(*cobra.Command, []string) error {
	if inAgentShell() {
		return errAgentCannotChangeModelEndpoints
	}
	return nil
}

// localModelsPlace is where the app shows and configures local models.
const localModelsPlace = "Settings -> AI providers -> Local & custom"

// localModelIDSuffix is the driver suffix of a local model's id (local.IDSuffix).
const localModelIDSuffix = "@local"

func newModelsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "models",
		Short: "List the models your chats and workflows can use, and your machines' local model servers",
		Long: `Lists the models your chats and workflow steps can run on, and manages the
local model servers (Ollama, LM Studio, llama.cpp, vLLM, any OpenAI-compatible
server) your machines look for.

A local model's id is its name on the server plus @local, e.g.
"qwen3:latest@local": use it as a workflow step's model id. Reliant's servers
reach it through the machine whose daemon found it, so that machine must be
online when the step runs. 'models list --local' shows each local model with the
machine serving it.

A machine looks for servers on localhost's well-known ports (Ollama 11434,
LM Studio 1234, llama.cpp 8080, vLLM 8000) by itself. 'models endpoint add'
adds one it would not find. Endpoints with an API key, or served from the
internet, are added in the app: ` + localModelsPlace + ` -> Add custom endpoint.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newModelsListCmd(), newModelsEndpointCmd())
	return cmd
}

// --- models list ------------------------------------------------------------

// modelRow is one model as `models list` shows it.
type modelRow struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	Driver        string `json:"driver"`
	ContextWindow int64  `json:"context_window"`
	SupportsTools bool   `json:"supports_tools"`
	// Set for a local model only.
	Local *localModelRow `json:"local,omitempty"`
}

type localModelRow struct {
	// Machine is the machine serving it, or a configured endpoint's name.
	Machine      string `json:"machine"`
	DaemonID     string `json:"daemon_id,omitempty"`
	EndpointID   string `json:"endpoint_id"`
	EndpointKind string `json:"endpoint_kind"`
	Online       bool   `json:"online"`
	// Pin is the workflow model selector provider that keeps a step on this
	// machine or endpoint: model: {id: <id>, providers: [<pin>]}.
	Pin string `json:"pin"`
}

func newModelsListCmd() *cobra.Command {
	var (
		localOnly bool
		orgID     string
		jsonOut   bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the models you can use",
		Long: `Lists the models your chats and workflow steps can run on: those of every
provider you connected, and the local models your machines serve.

--local lists only the local models, with the machine (or configured endpoint)
serving each, whether it is online, and the provider pin that keeps a workflow
step on it. A model whose machine is offline is listed and cannot run until the
machine is back.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			client := reliantv1connect.NewCatalogServiceClient(conn.httpClient(), conn.ServerURL)
			req := &reliantv1.ListModelsRequest{}
			if org := strings.TrimSpace(orgID); org != "" {
				req.OrgId = &org
			}
			resp, err := client.ListModels(cmd.Context(), connect.NewRequest(req))
			if err != nil {
				return conn.annotate(fmt.Errorf("listing models: %w", err))
			}
			rows := modelRows(resp.Msg.GetModels(), localOnly)
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), map[string]any{"models": rows})
			}
			if localOnly {
				writeLocalModels(cmd.OutOrStdout(), rows)
				return nil
			}
			writeModels(cmd.OutOrStdout(), rows)
			return nil
		},
	}
	cmd.Flags().BoolVar(&localOnly, "local", false, "Only the local models your machines serve, with the machine serving each")
	cmd.Flags().StringVar(&orgID, "org", "", "List what a chat of this organization may run on (its accounts and policy)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print JSON")
	return cmd
}

func modelRows(models []*reliantv1.ModelInfo, localOnly bool) []modelRow {
	rows := make([]modelRow, 0, len(models))
	for _, m := range models {
		src := m.GetLocal()
		isLocal := src != nil || strings.HasSuffix(m.GetId(), localModelIDSuffix)
		if localOnly && !isLocal {
			continue
		}
		row := modelRow{
			ID:            m.GetId(),
			Name:          m.GetName(),
			Provider:      m.GetProvider(),
			Driver:        m.GetDriverId(),
			ContextWindow: m.GetContextWindow(),
			SupportsTools: m.GetSupportsTools(),
		}
		if src != nil {
			row.Local = &localModelRow{
				Machine:      src.GetMachineName(),
				DaemonID:     src.GetDaemonId(),
				EndpointID:   src.GetEndpointId(),
				EndpointKind: src.GetEndpointKind(),
				Online:       src.GetOnline(),
				Pin:          localModelPin(src),
			}
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Provider != rows[j].Provider {
			return rows[i].Provider < rows[j].Provider
		}
		return rows[i].ID < rows[j].ID
	})
	return rows
}

// localModelPin is the selector provider that pins a local model to where it
// is served: the machine that detected it, or the configured endpoint (which
// carries no daemon id).
func localModelPin(src *reliantv1.LocalModelSource) string {
	if src.GetDaemonId() == "" {
		return "endpoint:" + src.GetEndpointId()
	}
	return "local:" + src.GetDaemonId()
}

func writeModels(w io.Writer, rows []modelRow) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "No models: connect a provider in Settings -> AI providers, or serve a local model on one of your machines.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPROVIDER\tCONTEXT\tTOOLS")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.ID, r.Provider, contextWindowLabel(r.ContextWindow), yesNo(r.SupportsTools))
	}
	_ = tw.Flush()
}

func writeLocalModels(w io.Writer, rows []modelRow) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "No local models. Start a model server (e.g. `ollama serve`) on a machine running Reliant, or add its address with `reliant models endpoint add`; `reliant models endpoint list` shows what each machine found.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tMACHINE\tSERVER\tONLINE\tCONTEXT\tTOOLS\tPIN")
	for _, r := range rows {
		l := r.Local
		if l == nil {
			l = &localModelRow{}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, l.Machine, l.EndpointKind, yesNo(l.Online),
			contextWindowLabel(r.ContextWindow), yesNo(r.SupportsTools), l.Pin)
	}
	_ = tw.Flush()
	fmt.Fprintln(w, "\nUse the id as a workflow step's model: {id: \"<id>\"}; add providers: [\"<pin>\"] to keep it on that machine or endpoint.")
}

func contextWindowLabel(n int64) string {
	switch {
	case n <= 0:
		return "-"
	case n >= 1000 && n%1000 == 0:
		return fmt.Sprintf("%dK", n/1000)
	case n >= 1024 && n%1024 == 0:
		return fmt.Sprintf("%dK", n/1024)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// --- models endpoint --------------------------------------------------------

// modelEndpointsClient is the slice of DaemonRegistryService and
// ModelEndpointService `models endpoint` uses.
type modelEndpointsClient interface {
	ListDaemons(context.Context, *connect.Request[reliantv1.ListDaemonsRequest]) (*connect.Response[reliantv1.ListDaemonsResponse], error)
	SetLocalModelEndpoints(context.Context, *connect.Request[reliantv1.SetLocalModelEndpointsRequest]) (*connect.Response[reliantv1.SetLocalModelEndpointsResponse], error)
	ListModelEndpoints(context.Context, *connect.Request[reliantv1.ListModelEndpointsRequest]) (*connect.Response[reliantv1.ListModelEndpointsResponse], error)
}

type modelEndpointsRPC struct {
	reliantv1connect.DaemonRegistryServiceClient
	custom reliantv1connect.ModelEndpointServiceClient
}

func (c modelEndpointsRPC) ListModelEndpoints(ctx context.Context, req *connect.Request[reliantv1.ListModelEndpointsRequest]) (*connect.Response[reliantv1.ListModelEndpointsResponse], error) {
	return c.custom.ListModelEndpoints(ctx, req)
}

func newModelEndpointsClient(conn *connection) modelEndpointsClient {
	return modelEndpointsRPC{
		DaemonRegistryServiceClient: newDaemonRegistryClient(conn),
		custom:                      reliantv1connect.NewModelEndpointServiceClient(conn.httpClient(), conn.ServerURL),
	}
}

func newModelsEndpointCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "endpoint",
		Short: "List, add and remove the local model servers your machines look for",
		Long: `A machine looks for local model servers on localhost's well-known ports by
itself. An endpoint is a server it would not find (another port, or another host
the machine reaches, e.g. http://gpu-box.lan:8000/v1). It is kept in that
machine's ~/.reliant/config.yaml (models.providers.local) and probed with the
detected ones.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newModelsEndpointListCmd(), newModelsEndpointAddCmd(), newModelsEndpointRemoveCmd())
	return cmd
}

// endpointRow is one model server as `models endpoint list` shows it.
type endpointRow struct {
	Machine  string   `json:"machine"`
	DaemonID string   `json:"daemon_id,omitempty"`
	Online   bool     `json:"online"`
	Kind     string   `json:"kind"`
	BaseURL  string   `json:"base_url"`
	Source   string   `json:"source"`
	Error    string   `json:"error,omitempty"`
	Models   []string `json:"models"`
}

func newModelsEndpointListCmd() *cobra.Command {
	var (
		machine string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List each machine's local model servers, and your custom endpoints",
		Long: `Lists each machine's local model servers: the ones it detected and the ones
added to it, with the chat models each serves or why the machine could not reach
it. Your custom endpoints (added in the app) are listed after them.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			client := newModelEndpointsClient(conn)
			rows, err := loadEndpointRows(cmd.Context(), client, strings.TrimSpace(machine))
			if err != nil {
				return conn.annotate(err)
			}
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), map[string]any{"endpoints": rows})
			}
			writeEndpoints(cmd.OutOrStdout(), rows)
			return nil
		},
	}
	cmd.Flags().StringVar(&machine, "machine", "", "Only this machine, by id or name")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print JSON")
	return cmd
}

func loadEndpointRows(ctx context.Context, client modelEndpointsClient, machine string) ([]endpointRow, error) {
	resp, err := client.ListDaemons(ctx, connect.NewRequest(&reliantv1.ListDaemonsRequest{}))
	if err != nil {
		return nil, explainRegistryErr(fmt.Errorf("listing machines: %w", err))
	}
	daemons := resp.Msg.GetDaemons()
	if machine != "" {
		d, err := pickMachine(daemons, machine)
		if err != nil {
			return nil, err
		}
		daemons = []*reliantv1.DaemonInfo{d}
	}
	var rows []endpointRow
	for _, d := range daemons {
		for _, ep := range d.GetLocalModels().GetEndpoints() {
			rows = append(rows, endpointRow{
				Machine: machineDisplayName(d), DaemonID: d.GetDaemonId(), Online: daemonOnline(d),
				Kind: ep.GetKind(), BaseURL: ep.GetBaseUrl(), Source: ep.GetSource(), Error: ep.GetError(),
				Models: chatModelNames(ep),
			})
		}
	}
	if machine != "" {
		return rows, nil
	}
	custom, err := client.ListModelEndpoints(ctx, connect.NewRequest(&reliantv1.ListModelEndpointsRequest{}))
	switch {
	case err == nil:
		for _, e := range custom.Msg.GetEndpoints() {
			row := endpointRow{Machine: e.GetName(), Kind: e.GetProbe().GetKind(), BaseURL: e.GetBaseUrl(), Source: "custom", Error: e.GetProbe().GetError(), Online: true}
			if e.GetRoute() == reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_VIA_DAEMON {
				row.DaemonID = e.GetDaemonId()
			}
			for _, m := range e.GetModels() {
				if !m.GetHidden() {
					row.Models = append(row.Models, m.GetName())
				}
			}
			rows = append(rows, row)
		}
	case connect.CodeOf(err) == connect.CodeUnimplemented:
		// A server from before custom endpoints has none.
	default:
		return nil, fmt.Errorf("listing custom endpoints: %w", err)
	}
	return rows, nil
}

func chatModelNames(ep *reliantv1.LocalModelEndpoint) []string {
	var names []string
	for _, m := range ep.GetModels() {
		if m.GetSupportsChat() {
			names = append(names, m.GetName())
		}
	}
	return names
}

func daemonOnline(d *reliantv1.DaemonInfo) bool {
	return d.GetStatus() == reliantv1.DaemonStatus_DAEMON_STATUS_ACTIVE || d.GetStatus() == reliantv1.DaemonStatus_DAEMON_STATUS_IDLE
}

func writeEndpoints(w io.Writer, rows []endpointRow) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "No local model servers found. Start one (e.g. `ollama serve`) on a machine running Reliant, or add its address with `reliant models endpoint add <url>`.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MACHINE\tSERVER\tURL\tSOURCE\tMODELS")
	for _, r := range rows {
		machine := r.Machine
		if r.Source != "custom" && !r.Online {
			machine += " (offline)"
		}
		models := strings.Join(r.Models, ", ")
		if r.Error != "" {
			models = "unreachable: " + r.Error
		} else if models == "" {
			models = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", machine, r.Kind, r.BaseURL, r.Source, models)
	}
	_ = tw.Flush()
}

func newModelsEndpointAddCmd() *cobra.Command {
	var machine string
	cmd := &cobra.Command{
		Use:   "add <base-url>",
		Short: "Add a local model server a machine should look for",
		Long: `Adds the OpenAI-compatible base URL of a model server, as the machine sees it
(e.g. http://localhost:9000/v1 or http://gpu-box.lan:8000/v1), to the servers the
machine probes. The machine probes it at once; its chat models are then listed
by 'reliant models list --local' as <name>@local.

--machine names the machine by id or name; with one machine it may be left out.
The machine must be online.`,
		Args:    cobra.ExactArgs(1),
		PreRunE: refuseModelsFromAgentShell,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseURL, err := normalizeEndpointURL(args[0])
			if err != nil {
				return err
			}
			return changeMachineEndpoints(cmd, machine, func(current []string) ([]string, error) {
				for _, u := range current {
					if u == baseURL {
						return nil, errEndpointUnchanged
					}
				}
				return append(current, baseURL), nil
			}, baseURL, "Added")
		},
	}
	cmd.Flags().StringVar(&machine, "machine", "", "The machine, by id or name (default: your only machine)")
	return cmd
}

func newModelsEndpointRemoveCmd() *cobra.Command {
	var machine string
	cmd := &cobra.Command{
		Use:   "remove <base-url>",
		Short: "Stop a machine looking for a local model server you added",
		Long: `Removes a base URL added with 'models endpoint add' (or Add endpoint in the
app) from the machine's servers. A detected server cannot be removed: it is found
again on its well-known port.`,
		Args:    cobra.ExactArgs(1),
		PreRunE: refuseModelsFromAgentShell,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseURL, err := normalizeEndpointURL(args[0])
			if err != nil {
				return err
			}
			return changeMachineEndpoints(cmd, machine, func(current []string) ([]string, error) {
				kept := make([]string, 0, len(current))
				for _, u := range current {
					if u != baseURL {
						kept = append(kept, u)
					}
				}
				if len(kept) == len(current) {
					return nil, fmt.Errorf("%s was not added to this machine: 'reliant models endpoint list' shows its servers", baseURL)
				}
				return kept, nil
			}, baseURL, "Removed")
		},
	}
	cmd.Flags().StringVar(&machine, "machine", "", "The machine, by id or name (default: your only machine)")
	return cmd
}

var errEndpointUnchanged = errors.New("endpoint already added")

// changeMachineEndpoints replaces a machine's configured endpoints with
// edit(current). SetLocalModelEndpoints takes the whole list, so the current
// one is read from the machine's last inventory (its "configured" endpoints).
func changeMachineEndpoints(cmd *cobra.Command, machine string, edit func([]string) ([]string, error), baseURL, verb string) error {
	conn, err := resolveConnection(cmd)
	if err != nil {
		return err
	}
	client := newModelEndpointsClient(conn)
	resp, err := client.ListDaemons(cmd.Context(), connect.NewRequest(&reliantv1.ListDaemonsRequest{}))
	if err != nil {
		return conn.annotate(explainRegistryErr(fmt.Errorf("listing machines: %w", err)))
	}
	d, err := pickMachine(resp.Msg.GetDaemons(), strings.TrimSpace(machine))
	if err != nil {
		return err
	}
	name := machineDisplayName(d)
	var current []string
	for _, ep := range d.GetLocalModels().GetEndpoints() {
		if ep.GetSource() == "configured" {
			if u, err := normalizeEndpointURL(ep.GetBaseUrl()); err == nil {
				current = append(current, u)
			}
		}
	}
	next, err := edit(current)
	if errors.Is(err, errEndpointUnchanged) {
		fmt.Fprintf(cmd.OutOrStdout(), "%s already looks for %s.\n", name, baseURL)
		return nil
	}
	if err != nil {
		return err
	}
	set, err := client.SetLocalModelEndpoints(cmd.Context(), connect.NewRequest(&reliantv1.SetLocalModelEndpointsRequest{
		DaemonId: d.GetDaemonId(), BaseUrls: next,
	}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeUnavailable || connect.CodeOf(err) == connect.CodeFailedPrecondition {
			return fmt.Errorf("%s must be online to change its local model servers: %w", name, err)
		}
		return conn.annotate(fmt.Errorf("updating %s's local model servers: %w", name, err))
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "%s %s on %s.\n", verb, baseURL, name)
	if verb != "Added" {
		return nil
	}
	for _, ep := range set.Msg.GetLocalModels().GetEndpoints() {
		u, err := normalizeEndpointURL(ep.GetBaseUrl())
		if err != nil || u != baseURL {
			continue
		}
		if ep.GetError() != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s could not reach it yet: %s. It is probed again every minute.\n", name, ep.GetError())
			return nil
		}
		models := chatModelNames(ep)
		if len(models) == 0 {
			fmt.Fprintln(w, "It answered, but serves no chat models yet.")
			return nil
		}
		ids := make([]string, len(models))
		for i, m := range models {
			ids[i] = m + localModelIDSuffix
		}
		fmt.Fprintf(w, "It serves: %s\n", strings.Join(ids, ", "))
		return nil
	}
	return nil
}

// pickMachine finds one machine by id or name; with no ref, the only machine.
func pickMachine(daemons []*reliantv1.DaemonInfo, ref string) (*reliantv1.DaemonInfo, error) {
	if ref == "" {
		switch len(daemons) {
		case 1:
			return daemons[0], nil
		case 0:
			return nil, errors.New("you have no machines: start the desktop app or `reliant daemon start` on the computer that runs the model server")
		}
		names := make([]string, 0, len(daemons))
		for _, d := range daemons {
			names = append(names, machineDisplayName(d))
		}
		sort.Strings(names)
		return nil, fmt.Errorf("you have %d machines (%s): pass --machine", len(daemons), strings.Join(names, ", "))
	}
	var byName []*reliantv1.DaemonInfo
	for _, d := range daemons {
		if d.GetDaemonId() == ref {
			return d, nil
		}
		if machineDisplayName(d) == ref {
			byName = append(byName, d)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return nil, fmt.Errorf("no machine %q: 'reliant machine list' shows yours", ref)
	}
	return nil, fmt.Errorf("%d machines are named %q: pass the id instead", len(byName), ref)
}

// normalizeEndpointURL checks a base URL and drops a trailing slash, so the
// same server is never added twice.
func normalizeEndpointURL(raw string) (string, error) {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%q is not a model server URL: pass its OpenAI-compatible base URL, e.g. http://localhost:11434/v1", raw)
	}
	return s, nil
}
