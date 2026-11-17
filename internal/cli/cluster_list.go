package cli

// `forge cluster list` / `forge cluster show <name>` — read what the control
// plane holds for the clusters connected with `forge cluster connect`.
//
// Same client as connect and disconnect (clusterConnectClient), same two RPCs
// (ListClusters, GetCluster). Read-only, and nothing credential-shaped is ever
// printed: the control plane returns neither the token nor the CA (Cluster in
// cluster.proto has no field for either), and this file's output types have no
// field that could carry one, so a future server that did send one would still
// not reach the terminal or the JSON document.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// The JSON documents. Members are stable under a schema name; a breaking
// change is a new version. Optional members are present-and-null rather than
// omitted, so a script indexes them without an existence check.
const (
	schemaClusterList = "forge.cluster.list/v1"
	schemaClusterShow = "forge.cluster.show/v1"
)

type clusterListDoc struct {
	Schema   string        `json:"schema"`
	Env      string        `json:"env"`
	Clusters []clusterView `json:"clusters"`
}

type clusterShowDoc struct {
	Schema  string      `json:"schema"`
	Env     string      `json:"env"`
	Cluster clusterView `json:"cluster"`
}

type clusterView struct {
	Name  string `json:"name"`
	ID    string `json:"id"`
	State string `json:"state"`
	// Auth is how the control plane authenticates to the cluster. The token is
	// the only mode there is; the credential itself is never returned.
	Auth    string `json:"auth"`
	Address string `json:"address"`
	// CAFingerprint is null when the control plane did not report one.
	CAFingerprint *string           `json:"ca_fingerprint"`
	Issuer        clusterIssuerView `json:"issuer"`
	Environments  []clusterEnvView  `json:"environments"`
	CreatedAt     *string           `json:"created_at"`
	UpdatedAt     *string           `json:"updated_at"`
}

type clusterIssuerView struct {
	Registered     bool    `json:"registered"`
	StoreClusterID *string `json:"store_cluster_id"`
	Issuer         *string `json:"issuer"`
	// Mode is "discover" (the store fetches the keys) or "upload" (a key set
	// was sent); null when not registered.
	Mode      *string `json:"mode"`
	KeyCount  int     `json:"key_count"`
	UpdatedAt *string `json:"updated_at"`
}

type clusterEnvView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Project string `json:"project"`
}

func newClusterListCmd() *cobra.Command {
	var envName, token string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the clusters connected to the control plane, with their state and env bindings",
		Long: `List every cluster the organization has connected, as the control plane sees it:
name, id, state, auth, registered address and CA fingerprint, the secret-store
issuer registration, the environments bound to it, and created/updated times.

  forge cluster list --env prod
  forge cluster list --env prod --json

State is the control plane's own: CONNECTED (it holds usable connection
material), WAITING (it holds nothing to try with) or STALE (the token's expiry
has passed). Use it to verify a 'forge cluster connect'.

--json prints one document, schema ` + schemaClusterList + `. A member the control plane did not
report is null. No token or certificate is ever printed.

--env names which control plane to talk to (required).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runClusterList(cmd.Context(), envName, token, asJSON, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	addClusterReadFlags(cmd, &envName, &token, &asJSON)
	return cmd
}

func newClusterShowCmd() *cobra.Command {
	var envName, token string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show one connected cluster",
		Long: `Show one connected cluster by exact name: the same fields as 'forge cluster list',
read through the control plane's GetCluster.

  forge cluster show prod-us --env prod
  forge cluster show prod-us --env prod --json

--json prints one document, schema ` + schemaClusterShow + `. No token or certificate is ever printed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClusterShow(cmd.Context(), args[0], envName, token, asJSON, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	addClusterReadFlags(cmd, &envName, &token, &asJSON)
	return cmd
}

func addClusterReadFlags(cmd *cobra.Command, envName, token *string, asJSON *bool) {
	cmd.Flags().StringVar(envName, "env", "", "Environment whose control plane to talk to (required)")
	cmd.Flags().StringVar(token, "token", "", "Credential to use, ahead of the env var and the credentials file")
	cmd.Flags().BoolVar(asJSON, "json", false, "Print one machine-readable document")
}

func runClusterList(ctx context.Context, envName, token string, asJSON bool, out, errOut io.Writer) error {
	client, _, err := clusterConnectClient(ctx, envName, token)
	if err != nil {
		return err
	}
	var resp struct {
		Clusters []wireConnectedCluster `json:"clusters"`
	}
	if err := client.Call(ctx, procListClusters, map[string]any{}, &resp); err != nil {
		return fmt.Errorf("list clusters: %w", err)
	}
	views := viewClusters(ctx, client, resp.Clusters, errOut)
	if asJSON {
		return writeClusterJSON(out, clusterListDoc{Schema: schemaClusterList, Env: envName, Clusters: views})
	}
	if len(views) == 0 {
		fmt.Fprintln(out, "no connected clusters: connect one with `forge cluster connect <name> --context <ctx> --env "+envName+"`")
		return nil
	}
	for i, v := range views {
		if i > 0 {
			fmt.Fprintln(out)
		}
		writeClusterText(out, v)
	}
	return nil
}

func runClusterShow(ctx context.Context, name, envName, token string, asJSON bool, out, errOut io.Writer) error {
	name = strings.TrimSpace(name)
	client, _, err := clusterConnectClient(ctx, envName, token)
	if err != nil {
		return err
	}
	id, err := lookupConnectedCluster(ctx, client, name)
	if err != nil {
		return err
	}
	var resp wireConnectClusterResponse
	if err := client.Call(ctx, procGetCluster, map[string]any{"id": id}, &resp); err != nil {
		return fmt.Errorf("get cluster %q: %w", name, err)
	}
	v := viewClusters(ctx, client, []wireConnectedCluster{resp.Cluster}, errOut)[0]
	if asJSON {
		return writeClusterJSON(out, clusterShowDoc{Schema: schemaClusterShow, Env: envName, Cluster: v})
	}
	writeClusterText(out, v)
	return nil
}

// viewClusters projects the wire clusters, naming each bound environment.
//
// Naming is best effort and one ListEnvironments call at most: a caller that
// may read clusters may not be allowed to read environments, and that must not
// cost them the listing. An id that cannot be named is kept, unnamed — a
// binding the caller cannot name is still a binding.
func viewClusters(ctx context.Context, client cloudCaller, wire []wireConnectedCluster, errOut io.Writer) []clusterView {
	byID := map[string]cloudEnvironment{}
	bound := false
	for _, c := range wire {
		bound = bound || len(c.EnvironmentIDs) > 0
	}
	if bound {
		var envs cloudEnvironmentsResponse
		if err := client.Call(ctx, "controlplane.v1.DeployService/ListEnvironments", map[string]any{}, &envs); err != nil {
			fmt.Fprintf(errOut, "note: could not resolve environment names (showing ids): %v\n", err)
		}
		for _, e := range envs.Environments {
			byID[e.ID] = e
		}
	}
	views := make([]clusterView, 0, len(wire))
	for _, c := range wire {
		envs := make([]clusterEnvView, 0, len(c.EnvironmentIDs))
		for _, id := range c.EnvironmentIDs {
			e := byID[id]
			envs = append(envs, clusterEnvView{ID: id, Name: e.Name, Project: e.Project})
		}
		sort.SliceStable(envs, func(i, j int) bool { return envs[i].ID < envs[j].ID })
		views = append(views, clusterView{
			Name: c.Name, ID: c.ID,
			State:         strings.TrimPrefix(c.Connection, "CLUSTER_CONNECTION_"),
			Auth:          "token",
			Address:       c.Address,
			CAFingerprint: presentOrNil(c.CAFingerprint),
			Issuer:        viewIssuer(c.Issuer),
			Environments:  envs,
			CreatedAt:     presentOrNil(c.CreatedAt),
			UpdatedAt:     presentOrNil(c.UpdatedAt),
		})
	}
	return views
}

func viewIssuer(w *wireClusterIssuer) clusterIssuerView {
	if w == nil || (w.StoreClusterID == "" && w.Issuer == "") {
		return clusterIssuerView{}
	}
	mode := "upload"
	if w.Discover {
		mode = "discover"
	}
	return clusterIssuerView{
		Registered: true, StoreClusterID: presentOrNil(w.StoreClusterID), Issuer: presentOrNil(w.Issuer),
		Mode: &mode, KeyCount: int(w.KeyCount), UpdatedAt: presentOrNil(w.UpdatedAt),
	}
}

func presentOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func clusterPtrOr(p *string, none string) string {
	if p == nil {
		return none
	}
	return *p
}

func writeClusterJSON(out io.Writer, doc any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

func writeClusterText(out io.Writer, v clusterView) {
	fmt.Fprintf(out, "%s\n", v.Name)
	fmt.Fprintf(out, "  id           %s\n", v.ID)
	fmt.Fprintf(out, "  state        %s\n", clusterPtrOr(presentOrNil(v.State), "UNKNOWN"))
	fmt.Fprintf(out, "  auth         %s\n", v.Auth)
	fmt.Fprintf(out, "  address      %s\n", clusterPtrOr(presentOrNil(v.Address), "-"))
	fmt.Fprintf(out, "  ca           %s\n", clusterPtrOr(v.CAFingerprint, "- (the control plane does not report one)"))
	if !v.Issuer.Registered {
		fmt.Fprintf(out, "  issuer       not registered\n")
	} else {
		keys := "the store fetches the keys itself"
		if *v.Issuer.Mode == "upload" {
			keys = fmt.Sprintf("%d key(s) uploaded", v.Issuer.KeyCount)
		}
		fmt.Fprintf(out, "  issuer       registered: %s (%s)\n", clusterPtrOr(v.Issuer.Issuer, "-"), *v.Issuer.Mode)
		fmt.Fprintf(out, "               %s; store id %s\n", keys, clusterPtrOr(v.Issuer.StoreClusterID, "-"))
	}
	if len(v.Environments) == 0 {
		fmt.Fprintf(out, "  envs         none\n")
	}
	for i, e := range v.Environments {
		label := "  envs         "
		if i > 0 {
			label = "               "
		}
		switch {
		case e.Name == "":
			fmt.Fprintf(out, "%s%s\n", label, e.ID)
		case e.Project != "":
			fmt.Fprintf(out, "%s%s (%s)\n", label, e.Name, e.Project)
		default:
			fmt.Fprintf(out, "%s%s\n", label, e.Name)
		}
	}
	fmt.Fprintf(out, "  created      %s\n", clusterPtrOr(v.CreatedAt, "-"))
	fmt.Fprintf(out, "  updated      %s\n", clusterPtrOr(v.UpdatedAt, "- (the control plane does not report one)"))
}
