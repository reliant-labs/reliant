package collector

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/reliant-labs/forge/pkg/telemetry"
)

// profiles renders t's pprof pulls: one pprof receiver per (scrape, type),
// through a receiver_creator when t's pods are discovered. It returns the
// receivers' names.
func (b *builder) profiles(t telemetry.Target) ([]string, error) {
	svc := t.ServiceName
	templates := map[string]any{}
	var names []string
	for i, p := range t.Profiles {
		auth, err := b.pprofAuth(t, i, p)
		if err != nil {
			return nil, err
		}
		for _, ty := range p.Types {
			key := fmt.Sprintf("%d_%s", i, ty)
			if b.pods(t) {
				templates["pprof/"+key] = map[string]any{
					"rule":   lit(podRule(t)),
					"config": pprofConfig(p, ty, podEndpoint(p.Port), auth),
				}
				continue
			}
			name := "pprof/telemetry_" + svc + "_" + key
			b.f.Receivers[name] = pprofConfig(p, ty, lit(net.JoinHostPort(t.Address, strconv.Itoa(p.Port))), auth)
			names = append(names, name)
		}
	}
	if b.pods(t) {
		b.observerUsed = true
		name := "receiver_creator/telemetry_" + svc + "_profiles"
		b.f.Receivers[name] = map[string]any{"watch_observers": strs(ObserverExtension), "receivers": templates}
		names = []string{name}
	}
	return names, nil
}

// pprofAuth is the client settings p's credentials add: a bearertokenauth
// extension reading the mounted token (rendered once per scrape), and TLS.
func (b *builder) pprofAuth(t telemetry.Target, i int, p telemetry.PprofScrape) (map[string]any, error) {
	out := map[string]any{}
	if p.BearerToken != nil {
		file, err := b.mountFile(t, fmt.Sprintf("profiles[%d].bearer_token", i), p.BearerToken)
		if err != nil {
			return nil, err
		}
		name := fmt.Sprintf("bearertokenauth/telemetry_%s_%d", t.ServiceName, i)
		if b.authExtensions == nil {
			b.authExtensions = map[string]any{}
		}
		b.authExtensions[name] = map[string]any{"filename": file}
		out["auth"] = map[string]any{"authenticator": name}
	}
	if p.TLS != nil {
		tls := map[string]any{}
		if p.TLS.InsecureSkipVerify {
			tls["insecure_skip_verify"] = true
		}
		if p.TLS.ServerName != "" {
			tls["server_name_override"] = lit(p.TLS.ServerName)
		}
		if p.TLS.CA != nil {
			file, err := b.mountFile(t, fmt.Sprintf("profiles[%d].tls.ca", i), p.TLS.CA)
			if err != nil {
				return nil, err
			}
			tls["ca_file"] = file
		}
		out["tls"] = tls
	}
	return out, nil
}

// pprofConfig is one pprof receiver: a remote pull of profile type ty from
// hostport every p.Interval. A CPU profile is a capture of p.CPUSeconds, so
// its request may take that long; the rest are snapshots.
func pprofConfig(p telemetry.PprofScrape, ty, hostport string, auth map[string]any) map[string]any {
	path := lit(p.PathPrefix) + "/" + ty
	timeout := 30 * time.Second
	if ty == "cpu" {
		path = lit(p.PathPrefix) + "/profile?seconds=" + strconv.Itoa(p.CPUSeconds)
		timeout = time.Duration(p.CPUSeconds)*time.Second + 15*time.Second
	}
	remote := map[string]any{
		"endpoint":            p.Scheme + "://" + hostport + path,
		"collection_interval": p.Interval,
		"timeout":             timeout.String(),
	}
	for k, v := range auth {
		remote[k] = v
	}
	return map[string]any{"remote": remote}
}
