// Package cli — `forge cluster status` command.
//
// Renders a human- or machine-readable snapshot of the dev cluster,
// running pods, and ingress URLs derived from rendered KCL. Replaces a
// 30-line bash recipe every k8s-targeting forge project would otherwise
// hand-write.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

func newDevStatusCmd() *cobra.Command {
	var (
		configPath string
		jsonOut    bool
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Print dynamic dev-loop state (cluster up/down, pods, ingress URLs)",
		Long: `Print the dynamic state of the local dev environment.

Dynamic means "what's actually happening right now" — does the k3d
cluster exist, what's the current kubectl context, what pods are in
the dev namespace, what ingress URLs are exposed by the dev env's
KCL gateways, what sibling dev namespaces exist on this cluster.

For static config (declared cluster name, expected context, declared
service/frontend ports) run ` + "`forge cluster info`" + `.

Examples:
  forge cluster status
  forge cluster status --json    # machine-readable for scripts/dashboards`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDevStatus(cmd.Context(), configPath, jsonOut)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", defaultK3dConfigPath, "k3d config file")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit machine-readable JSON")
	return cmd
}

// devStatusSummary is the rendered shape — kept stable so --json output
// is consumable by dashboards and scripts.
type devStatusSummary struct {
	Cluster     clusterStatusSummary `json:"cluster"`
	Namespace   string               `json:"namespace"`
	Pods        []podStatusEntry     `json:"pods"`
	IngressURLs []ingressURLEntry    `json:"ingress_urls"`
	Siblings    []string             `json:"siblings"`
}

type podStatusEntry struct {
	Name    string `json:"name"`
	Ready   string `json:"ready"`    // e.g. "1/1"
	Status  string `json:"status"`   // e.g. "Running"
	Restart string `json:"restarts"` // e.g. "0"
	Age     string `json:"age"`
}

// ingressURLEntry is one row of the "Ingress URLs:" section. URL is the
// already-assembled scheme://host:port/path string callers can curl
// against; the remaining fields are projected from the underlying
// HTTPRoute/GRPCRoute so dashboards can filter without re-parsing URL.
type ingressURLEntry struct {
	URL      string `json:"url"`
	Kind     string `json:"kind"` // "HTTPRoute" | "GRPCRoute"
	Route    string `json:"route"`
	Gateway  string `json:"gateway"`
	Listener string `json:"listener"`
	Service  string `json:"service"`
	Port     int    `json:"port"`
}

func runDevStatus(ctx context.Context, configPath string, jsonOut bool) error {
	clusterName, err := resolveClusterName(configPath)
	if err != nil {
		return err
	}

	exists, _ := clusterExists(ctx, clusterName)
	_, statErr := os.Stat(configPath)

	cluster := clusterStatusSummary{
		Name:        clusterName,
		Exists:      exists,
		Context:     "k3d-" + clusterName,
		ConfigPath:  configPath,
		ConfigFound: statErr == nil,
	}

	ns := devNamespace(clusterName)
	summary := devStatusSummary{
		Cluster:   cluster,
		Namespace: ns,
	}

	// Ingress feature gating: when off (or forge.yaml unreadable), the
	// section stays empty and the human render prints a "disabled" hint.
	// When on, render KCL for the dev env and project routes into URLs —
	// best-effort, mirroring the pod listing's "render what you can"
	// stance. ingressKnown distinguishes "feature gated off" from
	// "feature on but no routes" / "feature on but KCL unreadable".
	ingressEnabled := false
	if store, cfgErr := loadProjectStore(); cfgErr == nil {
		ingressEnabled = store.Features().IngressEnabled()
	}

	if exists {
		summary.Pods = listPodsInNamespace(ctx, ns)
		summary.Siblings = listSiblingNamespaces(ctx, clusterName, ns)
	}
	if ingressEnabled {
		if entities, kclErr := RenderKCL(ctx, ".", "dev"); kclErr == nil && entities != nil {
			summary.IngressURLs = buildDevStatusIngressURLs(entities)
		}
	}

	if jsonOut {
		if summary.IngressURLs == nil {
			summary.IngressURLs = []ingressURLEntry{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(summary)
	}

	// Dynamic state: is the cluster up, what's the current kubectl
	// context, what namespace are we reading from. Declared values
	// (expected cluster name, expected context) live in `forge cluster info`.
	fmt.Printf("Cluster %s: %s\n", cluster.Name, boolUpDown(cluster.Exists))
	current := currentKubectlContext(ctx)
	switch current {
	case "":
		fmt.Printf("kubectl context (current): (none)\n")
	case cluster.Context:
		fmt.Printf("kubectl context (current): %s (matches expected)\n", current)
	default:
		fmt.Printf("kubectl context (current): %s (expected %s — run `kubectl config use-context %s`)\n",
			current, cluster.Context, cluster.Context)
	}
	// Config path + found/missing — folded in from the old nested
	// `dev cluster status` so nothing was lost when that command was
	// dropped in favour of this superset implementation.
	fmt.Printf("Config: %s (%s)\n", cluster.ConfigPath, foundOrMissing(cluster.ConfigFound))
	fmt.Printf("Namespace: %s\n", ns)
	fmt.Println()
	if !exists {
		fmt.Println("Cluster is down — run `forge cluster up` to start.")
		fmt.Println("Run `forge cluster info` for the declared config.")
		return nil
	}

	fmt.Println("Pods:")
	if len(summary.Pods) == 0 {
		fmt.Println("  (none)")
	} else {
		fmt.Printf("  %-40s %-8s %-12s %-10s %s\n", "NAME", "READY", "STATUS", "RESTARTS", "AGE")
		for _, p := range summary.Pods {
			fmt.Printf("  %-40s %-8s %-12s %-10s %s\n", p.Name, p.Ready, p.Status, p.Restart, p.Age)
		}
	}

	fmt.Println()
	writeIngressURLsSection(os.Stdout, summary.IngressURLs, ingressEnabled)

	if len(summary.Siblings) > 0 {
		fmt.Println()
		fmt.Println("Sibling dev namespaces on this cluster:")
		for _, s := range summary.Siblings {
			fmt.Printf("  - %s\n", s)
		}
	}
	fmt.Println()
	fmt.Println("For declared port mappings, run `forge cluster info`.")
	return nil
}

// buildDevStatusIngressURLs projects the dev env's rendered KCL gateways +
// HTTP/GRPC routes into a flat list of URLs the human/JSON renderers
// consume. Pure — no I/O — so tests can stub *KCLEntities directly.
//
// URL construction:
//   - scheme: "https" when the matched listener.Protocol == "HTTPS";
//     "grpc" for GRPCRoute; else "http".
//   - host: route.Host > listener.Hostname > gateway.Host > "localhost".
//   - port: matched listener.Port (zero is rendered, callers can
//     filter if needed — the KCL schema requires Port).
//   - path: listener.PathPrefix concatenated with route.Path; double
//     slashes collapsed; empty path becomes "/".
//
// Routes whose Gateway or Listener can't be resolved are skipped
// silently — best-effort, like the surrounding render-what-you-can
// pattern.
func buildDevStatusIngressURLs(entities *KCLEntities) []ingressURLEntry {
	if entities == nil {
		return nil
	}
	gwByName := make(map[string]*GatewayEntity, len(entities.Gateways))
	for i := range entities.Gateways {
		gwByName[entities.Gateways[i].Name] = &entities.Gateways[i]
	}
	var out []ingressURLEntry
	for _, r := range entities.HTTPRoutes {
		if e, ok := buildRouteURL(routeSpec{
			kind: "HTTPRoute", name: r.Name, gateway: r.Gateway, listener: r.Listener,
			service: r.Service, port: r.Port, host: r.Host, path: r.Path,
		}, gwByName); ok {
			out = append(out, e)
		}
	}
	for _, r := range entities.GRPCRoutes {
		if e, ok := buildRouteURL(routeSpec{
			kind: "GRPCRoute", name: r.Name, gateway: r.Gateway, listener: r.Listener,
			service: r.Service, port: r.Port, host: r.Host, path: r.Path,
		}, gwByName); ok {
			out = append(out, e)
		}
	}
	return out
}

// routeSpec is the subset of an HTTPRoute/GRPCRoute entity that
// buildRouteURL needs to resolve a concrete ingress URL. The kind field
// selects the Gateway API resource kind (and thus the URL scheme).
type routeSpec struct {
	kind     string
	name     string
	gateway  string
	listener string
	service  string
	port     int
	host     string
	path     string
}

func buildRouteURL(r routeSpec, gwByName map[string]*GatewayEntity) (ingressURLEntry, bool) {
	gw, ok := gwByName[r.gateway]
	if !ok {
		return ingressURLEntry{}, false
	}
	var l *GatewayListenerEntity
	for i := range gw.Listeners {
		if gw.Listeners[i].Name == r.listener {
			l = &gw.Listeners[i]
			break
		}
	}
	if l == nil {
		return ingressURLEntry{}, false
	}
	scheme := "http"
	if r.kind == "GRPCRoute" {
		scheme = "grpc"
	} else if strings.EqualFold(l.Protocol, "HTTPS") {
		scheme = "https"
	}
	host := gw.EffectiveHost(l, r.host)
	if host == "" {
		host = "localhost"
	}
	path := collapseSlashes(l.PathPrefix + r.path)
	if path == "" {
		path = "/"
	}
	url := fmt.Sprintf("%s://%s:%d%s", scheme, host, l.Port, path)
	return ingressURLEntry{
		URL:      url,
		Kind:     r.kind,
		Route:    r.name,
		Gateway:  r.gateway,
		Listener: r.listener,
		Service:  r.service,
		Port:     r.port,
	}, true
}

// writeIngressURLsSection prints the human-output "Ingress URLs:"
// section. Factored out so tests can drive the three states (disabled /
// empty / populated) against a buffer without needing a live cluster.
func writeIngressURLsSection(w io.Writer, urls []ingressURLEntry, ingressEnabled bool) {
	_, _ = fmt.Fprintln(w, "Ingress URLs:")
	switch {
	case !ingressEnabled:
		_, _ = fmt.Fprintln(w, "  (ingress feature disabled)")
	case len(urls) == 0:
		_, _ = fmt.Fprintln(w, "  (none — see deploy/kcl/dev/ingress.k)")
	default:
		_, _ = fmt.Fprintf(w, "  %-10s %-20s %-15s %s\n", "KIND", "ROUTE", "SERVICE", "URL")
		for _, u := range urls {
			_, _ = fmt.Fprintf(w, "  %-10s %-20s %-15s %s\n", u.Kind, u.Route, u.Service, u.URL)
		}
	}
}

func collapseSlashes(s string) string {
	for strings.Contains(s, "//") {
		s = strings.ReplaceAll(s, "//", "/")
	}
	return s
}

// devNamespace resolves the namespace forge cluster operates against. Reads
// the dev environment's namespace from the rendered KCL's K8sCluster
// when present; falls back to <project>-dev (which matches forge env deploy
// dev's behavior).
func devNamespace(clusterName string) string {
	store, err := loadProjectStore()
	if err != nil {
		return clusterName + "-dev"
	}
	if ns := k8sClusterNamespaceForEnv(context.Background(), "dev"); ns != "" {
		return ns
	}
	return store.Meta().Name + "-dev"
}

// listPodsInNamespace returns a compact pod table for the given
// namespace. Failures are non-fatal — we return an empty slice and let
// the caller render "(none)".
//
// Uses kubectl's native `get pods` output (no --output= override) so
// READY renders as "1/1" / "0/1" and AGE as relative ("2m", "3h") —
// the custom-columns path returned a literal "true"/"false" for READY
// and an ISO timestamp for AGE, both visually noisy. Standard kubectl
// output gives exactly five columns: NAME READY STATUS RESTARTS AGE.
func listPodsInNamespace(ctx context.Context, ns string) []podStatusEntry {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "pods", "-n", ns, "--no-headers")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var entries []podStatusEntry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		// RESTARTS may have a parenthesized suffix like "2 (3m ago)" —
		// kubectl emits a 6th-or-7th field. Glue the trailing fields
		// after AGE back into AGE so the table stays 5 columns.
		age := fields[4]
		if len(fields) > 5 {
			age = strings.Join(fields[4:], " ")
		}
		entries = append(entries, podStatusEntry{
			Name:    fields[0],
			Ready:   fields[1],
			Status:  fields[2],
			Restart: fields[3],
			Age:     age,
		})
	}
	return entries
}

// listSiblingNamespaces returns all forge-managed namespaces on the
// cluster other than the project's primary dev namespace. Used to
// surface the multi-worktree workflow (each worktree gets its own
// namespace via the env override).
func listSiblingNamespaces(ctx context.Context, clusterName, primary string) []string {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "namespaces",
		"-l", "app.kubernetes.io/managed-by=forge",
		"-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var siblings []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == primary {
			continue
		}
		siblings = append(siblings, line)
	}
	return siblings
}
