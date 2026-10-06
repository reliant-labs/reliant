// Copyright (c) 2025 Reliant Labs
package forgecred

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/credentials"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/cliauth"
)

// Pin names the daemon session that spawned this process. The daemon sets it
// on the helper command it exports to its children, so an agent's forge acts
// as the daemon the agent runs on rather than as whichever session sorts
// first on the machine.
type Pin struct {
	Server  string
	Account string
}

// Discover lists this machine's Reliant sessions, most preferred first:
//
//   - pinned: that daemon's credential, then the CLI login on the same
//     server (the same person's other session there);
//   - otherwise: preferredServer's CLI login and its default daemon, then
//     every other daemon and CLI login on the machine.
//
// Expired credentials are skipped, and a credential reachable two ways is
// listed once. Which control plane a session belongs to is NOT decided here:
// the server says so when asked, without minting.
func Discover(pin Pin, preferredServer, cliCredentialsPath string, now time.Time) ([]Session, error) {
	var out []Session
	seen := map[string]bool{}
	add := func(s Session) {
		if strings.TrimSpace(s.Token) == "" || seen[s.Token] {
			return
		}
		seen[s.Token] = true
		out = append(out, s)
	}

	logins, err := cliLogins(cliCredentialsPath, now)
	if err != nil {
		return nil, err
	}
	daemons, err := auth.ListDaemonCredentials()
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(pin.Server) != "" {
		pinned, err := auth.ReadDaemonCredentials(pin.Server, pin.Account)
		if err != nil {
			return nil, err
		}
		if pinned != nil && live(pinned.ExpiresAt, now) {
			add(daemonSession(pinned, pin.Server))
		}
		addLoginFor(add, logins, pin.Server)
		return out, nil
	}

	if strings.TrimSpace(preferredServer) != "" {
		addLoginFor(add, logins, preferredServer)
		if def, err := auth.ReadDaemonCredentials(preferredServer, ""); err == nil && def != nil && live(def.ExpiresAt, now) {
			add(daemonSession(def, preferredServer))
		}
	}
	for _, d := range daemons {
		if live(d.ExpiresAt, now) {
			add(daemonSession(d, d.ServerURL))
		}
	}
	for _, l := range logins {
		add(l)
	}
	return out, nil
}

func live(expiresAt *time.Time, now time.Time) bool {
	return expiresAt == nil || expiresAt.After(now)
}

func daemonSession(d *auth.DaemonCredentials, fallbackServer string) Session {
	server := d.ServerURL
	if strings.TrimSpace(server) == "" {
		server = fallbackServer
	}
	return Session{Server: server, Token: d.PAT, Kind: "daemon", Account: d.Sub}
}

func addLoginFor(add func(Session), logins []Session, server string) {
	key := serverKey(server)
	for _, l := range logins {
		if serverKey(l.Server) == key {
			add(l)
		}
	}
}

// cliLogins reads every live `reliant auth login` from the shared credentials
// file, sorted by server.
func cliLogins(path string, now time.Time) ([]Session, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	f, err := credentials.Load(path)
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, endpoint := range f.Endpoints() {
		c, err := f.Get(endpoint, cliauth.ClientID)
		if errors.Is(err, credentials.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if c.Expired(now) {
			continue
		}
		out = append(out, Session{Server: endpoint, Token: c.Token, Kind: "cli"})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Server < out[j].Server })
	return out, nil
}
