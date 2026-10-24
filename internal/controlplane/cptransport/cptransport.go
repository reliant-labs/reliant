// Copyright (c) 2025 Reliant Labs
//
// Package cptransport is the one rule for which transport a reliant server
// role may use to reach its control plane (RELIANT_CONTROL_PLANE_URL) with an
// internal-service bearer on the request. It is a leaf so that a small
// fail-closed client (hosteduse) can apply it without importing the
// control-plane adapter.
//
//forge:exclude-contract: a pure URL predicate with no I/O
package cptransport

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// ErrInsecure is returned when a control-plane URL would send the bearer in
// plaintext over a network that may leave the machine or the cluster.
var ErrInsecure = errors.New("control-plane URL must use https unless its host is loopback or an in-cluster Kubernetes Service (<svc>.<namespace>.svc[.cluster.local])")

// Check accepts https to any host, and plaintext http only to a host that
// never leaves the machine or the cluster: localhost, a loopback IP, or a
// Kubernetes Service DNS name. That last is how every server role reaches
// control-plane's admin-server in a hosted deployment
// (http://admin-server.<ns>.svc.cluster.local:8090) — the same hop that already
// carries the GitHub installation tokens credwire fetches — so admitting it
// adds no exposure. An http:// URL naming any other host (a public name, a
// bare IP, a one-label name resolved through search domains) is an operator
// mistake and is refused.
func Check(u *url.URL) error {
	if u == nil || u.Hostname() == "" {
		return ErrInsecure
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if loopback(u.Hostname()) || clusterService(u.Hostname()) {
			return nil
		}
	}
	return ErrInsecure
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// clusterService reports whether host is a Kubernetes Service name qualified
// at least to its namespace: <svc>.<ns>.svc or <svc>.<ns>.svc.cluster.local.
// Neither suffix is a public DNS zone, so such a name resolves only through
// the cluster's DNS.
func clusterService(host string) bool {
	labels := strings.Split(strings.TrimSuffix(strings.ToLower(host), "."), ".")
	for _, l := range labels {
		if l == "" {
			return false
		}
	}
	switch len(labels) {
	case 3:
		return labels[2] == "svc"
	case 5:
		return labels[2] == "svc" && labels[3] == "cluster" && labels[4] == "local"
	}
	return false
}
