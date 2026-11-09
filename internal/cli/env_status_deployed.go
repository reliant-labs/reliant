package cli

// `forge env status <env>` for an env that runs NOTHING on this machine.
//
// On 2026-10-07 `forge env status prod`, in a clean release worktree, printed
// a `forge env up · prod` box whose reliant-web row read
// `http://localhost:3000  up (pid 20897, not forge-owned)`. pid 20897 was the
// DEV stack's Vite; prod's reliant-web is Firebase Hosting. The status of a
// cloud env had probed localhost and reported another env's process as its
// frontend. Every part of that was wrong: the env's frontend does not run
// here, a local port says nothing about it, and the "forge env up" frame
// describes a command nobody ran for this env.
//
// So for an env whose declaration targets no local machine
// (entitiesTargetThisMachine), status probes nothing local: a shipped frontend
// is probed at its DEPLOYED URL, and the rows are presented as deployed
// frontends, not as an `env up` stack.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// deployedFrontendRows lists the env's shipped frontends at their deployed
// URLs. A frontend whose URL the declaration does not determine (a bucket
// behind a CDN the author wires) gets a row with no URL, which says exactly
// that rather than inventing one.
func deployedFrontendRows(e *KCLEntities) []upServiceRow {
	if e == nil {
		return nil
	}
	var rows []upServiceRow
	for _, fe := range e.Frontends {
		if !fe.Runtime.Ships() {
			continue
		}
		r := upServiceRow{Name: fe.Name, Kind: "frontend"}
		switch {
		case fe.Runtime.Firebase != nil && fe.Runtime.Firebase.Site != "":
			// Firebase Hosting serves every site at <site>.web.app,
			// whatever custom domain is also mapped to it.
			r.URL = fmt.Sprintf("https://%s.web.app", fe.Runtime.Firebase.Site)
			r.Log = "firebase hosting site " + fe.Runtime.Firebase.Site
		case fe.Runtime.Bucket != nil:
			r.Log = "bucket " + fe.Runtime.Bucket.Bucket
		}
		rows = append(rows, r)
	}
	return rows
}

// probeDeployedURL reports whether a deployed URL answers. A seam: the
// package's tests reach no network.
var probeDeployedURL = func(ctx context.Context, url string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return false
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode < 500
}

// probeDeployedRows fills Listening for every row with a URL, concurrently.
func probeDeployedRows(ctx context.Context, rows []upServiceRow) {
	var wg sync.WaitGroup
	for i := range rows {
		if rows[i].URL == "" {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rows[i].Listening = probeDeployedURL(ctx, rows[i].URL)
		}(i)
	}
	wg.Wait()
}

// renderDeployedFrontends is the remote env's answer where a local env's
// `forge env up` box would go.
func renderDeployedFrontends(w io.Writer, env string, rows []upServiceRow) {
	if len(rows) == 0 {
		fmt.Fprintf(w, "[status] env %s runs nothing on this machine and ships no frontend from it\n", env)
		return
	}
	fmt.Fprintf(w, "Deployed frontends · %s  (nothing of this env runs on this machine; nothing local was probed)\n", env)
	for _, r := range rows {
		state := "no deployed URL declared"
		switch {
		case r.URL != "" && r.Listening:
			state = "up"
		case r.URL != "":
			state = "DOWN (no answer)"
		}
		fmt.Fprintf(w, "  ● %-16s %-40s %s   (%s)\n", r.Name, emptyAs(r.URL, "-"), state, r.Log)
	}
}
