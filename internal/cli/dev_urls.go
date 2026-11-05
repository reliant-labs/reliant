// Package cli — `forge cluster urls` command.
//
// Reads the rendered dev-env KCL and prints the ingress URL table —
// one row per HTTPRoute/GRPCRoute, grouped by gateway + listener.
// Lets users (and sub-agents) discover "what URL do I hit" without
// reading deploy/kcl/dev/ingress.k by hand.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

func newDevUrlsCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "urls",
		Short: "Print the ingress URL table for the dev env",
		Long: `Print the ingress URL table for the dev env.

Reads deploy/kcl/dev/ via the same KCL render the deploy pipeline
uses, then prints one URL per HTTPRoute/GRPCRoute grouped by gateway
and listener.

When features.ingress is disabled this prints a short notice and
exits 0. When the dev env has no gateways declared yet it prints a
pointer to deploy/kcl/dev/ingress.k.

Examples:
  forge cluster urls
  forge cluster urls --json    # machine-readable for scripts/dashboards`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDevUrls(cmd.Context(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit machine-readable JSON")
	return cmd
}

// devUrlsSummary is the rendered shape — kept stable so --json output
// is consumable by dashboards, sub-agents, and scripts.
type devUrlsSummary struct {
	Env  string       `json:"env"`
	URLs []IngressURL `json:"urls"`
}

// IngressURL is one row of the dev ingress URL table.
//
// Warning is set (and URL may be empty) when the route references a
// gateway/listener that doesn't resolve. Surfacing this as a row rather
// than aborting matches the audit-shaped commands' graceful-degradation
// posture.
type IngressURL struct {
	Route    string `json:"route"`
	Kind     string `json:"kind"` // "HTTPRoute" | "GRPCRoute"
	URL      string `json:"url"`
	Gateway  string `json:"gateway"`
	Listener string `json:"listener"`
	Service  string `json:"service"`
	Port     int    `json:"port"`
	Warning  string `json:"warning,omitempty"`
}

func runDevUrls(ctx context.Context, jsonOut bool) error {
	const env = "dev"

	store, err := loadProjectStore()
	if err != nil {
		return err
	}

	emitEmpty := func(msg string) error {
		if jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(devUrlsSummary{Env: env, URLs: []IngressURL{}})
		}
		fmt.Println(msg)
		return nil
	}

	if !store.Features().IngressEnabled() {
		return emitEmpty("Ingress feature is disabled (set features.ingress: true in forge.yaml)")
	}

	projectDir := "."
	if cfgPath, perr := findProjectConfigFile(); perr == nil {
		projectDir = filepath.Dir(cfgPath)
	}

	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return fmt.Errorf("render dev KCL: %w\nhint: ensure `kcl` is on PATH and deploy/kcl/dev/ exists", err)
	}

	if entities == nil || len(entities.Gateways) == 0 {
		return emitEmpty("No ingress declared — see deploy/kcl/dev/ingress.k")
	}

	urls := buildIngressURLs(entities)
	summary := devUrlsSummary{Env: env, URLs: urls}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(summary)
	}

	renderIngressURLs(os.Stdout, entities, urls)
	return nil
}

// buildIngressURLs is the pure projection from KCL entities to URL
// rows. Kept separate from the cobra wiring so unit tests can feed it
// stubbed KCLEntities without a live kcl binary.
func buildIngressURLs(entities *KCLEntities) []IngressURL {
	if entities == nil {
		return nil
	}
	out := make([]IngressURL, 0, len(entities.HTTPRoutes)+len(entities.GRPCRoutes))
	for _, r := range entities.HTTPRoutes {
		out = append(out, buildOneIngressURL(entities, r, "HTTPRoute"))
	}
	for _, r := range entities.GRPCRoutes {
		// GRPCRouteEntity is field-for-field identical to HTTPRouteEntity;
		// coerce so we don't need two near-duplicate row builders.
		coerced := HTTPRouteEntity{
			Name:     r.Name,
			Gateway:  r.Gateway,
			Listener: r.Listener,
			Service:  r.Service,
			Port:     r.Port,
			Host:     r.Host,
			Path:     r.Path,
		}
		out = append(out, buildOneIngressURL(entities, coerced, "GRPCRoute"))
	}
	return out
}

func buildOneIngressURL(entities *KCLEntities, r HTTPRouteEntity, kind string) IngressURL {
	row := IngressURL{
		Route:    r.Name,
		Kind:     kind,
		Gateway:  r.Gateway,
		Listener: r.Listener,
		Service:  r.Service,
		Port:     r.Port,
	}
	gw := findGateway(entities, r.Gateway)
	if gw == nil {
		row.Warning = fmt.Sprintf("gateway %q not found", r.Gateway)
		return row
	}
	listener := findListener(gw, r.Listener)
	if listener == nil {
		row.Warning = fmt.Sprintf("listener %q not found on gateway %q", r.Listener, r.Gateway)
		return row
	}

	scheme := "http"
	switch {
	case kind == "GRPCRoute":
		scheme = "grpc"
	case strings.EqualFold(listener.Protocol, "HTTPS"):
		scheme = "https"
	}

	host := gw.EffectiveHost(listener, r.Host)
	if host == "" {
		host = "localhost"
	}

	path := listener.PathPrefix + r.Path
	// path_prefix often ends in "/" and route.Path often starts in "/";
	// collapse the resulting "//" to a single "/" rather than emitting
	// a malformed URL.
	path = strings.Replace(path, "//", "/", 1)
	if path == "" {
		path = "/"
	}

	row.URL = fmt.Sprintf("%s://%s:%d%s", scheme, host, listener.Port, path)
	return row
}

func findGateway(entities *KCLEntities, name string) *GatewayEntity {
	for i := range entities.Gateways {
		if entities.Gateways[i].Name == name {
			return &entities.Gateways[i]
		}
	}
	return nil
}

func findListener(gw *GatewayEntity, name string) *GatewayListenerEntity {
	for i := range gw.Listeners {
		if gw.Listeners[i].Name == name {
			return &gw.Listeners[i]
		}
	}
	return nil
}

// renderIngressURLs prints the human-shape table grouped by gateway +
// listener. Gateways/listeners with no routes are still shown so users
// can sanity-check that a listener is wired but unused.
func renderIngressURLs(w io.Writer, entities *KCLEntities, urls []IngressURL) {
	byKey := map[string][]IngressURL{}
	for _, u := range urls {
		key := u.Gateway + "\x00" + u.Listener
		byKey[key] = append(byKey[key], u)
	}

	gateways := append([]GatewayEntity(nil), entities.Gateways...)
	sort.SliceStable(gateways, func(i, j int) bool { return gateways[i].Name < gateways[j].Name })

	for gi, gw := range gateways {
		if gi > 0 {
			_, _ = fmt.Fprintln(w)
		}
		_, _ = fmt.Fprintln(w, gw.Name)
		listeners := append([]GatewayListenerEntity(nil), gw.Listeners...)
		sort.SliceStable(listeners, func(i, j int) bool { return listeners[i].Port < listeners[j].Port })
		for _, l := range listeners {
			_, _ = fmt.Fprintf(w, "  %s (port %d, %s)\n", l.Name, l.Port, l.Protocol)
			rows := byKey[gw.Name+"\x00"+l.Name]
			if len(rows) == 0 {
				_, _ = fmt.Fprintln(w, "    (no routes)")
				continue
			}
			for _, r := range rows {
				if r.Warning != "" {
					_, _ = fmt.Fprintf(w, "    %-22s WARNING: %s\n", r.Route, r.Warning)
					continue
				}
				_, _ = fmt.Fprintf(w, "    %-22s %-40s -> %s:%d\n", r.Route, r.URL, r.Service, r.Port)
			}
		}
	}

	// Surface orphan rows (route references a gateway/listener that
	// doesn't exist on entities.Gateways) so they aren't silently dropped.
	var orphans []IngressURL
	for _, u := range urls {
		if u.Warning == "" {
			continue
		}
		if findGateway(entities, u.Gateway) == nil {
			orphans = append(orphans, u)
		}
	}
	if len(orphans) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "Unresolved routes:")
		for _, u := range orphans {
			_, _ = fmt.Fprintf(w, "  %-22s WARNING: %s\n", u.Route, u.Warning)
		}
	}
}
