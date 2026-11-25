package doctor

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// CheckAppHealth verifies the application answers its probes: the ones its
// workload declares (RuntimeTarget.Probe — a TCP connect, or its own HTTP
// paths), else forge's /healthz ("ok") and /readyz.
//
// The address is the one the CALLER resolved (RuntimeTarget.HTTP) — this
// check never guesses a port. When nothing resolved one, the answer is
// UNDETERMINED, not a skip: "forge could not find the app" and "this project
// has no app" are different reports and must not render alike.
func CheckAppHealth(ctx context.Context, env *Environment) CheckResult {
	addr, ok := env.GetPort("app", 8080)
	if !ok {
		return CheckResult{
			Status: StatusUnknown,
			Message: "could not resolve an HTTP address for any host service" +
				" — is the stack up? (`forge env up <env>`)",
		}
	}

	client := &http.Client{Timeout: 3 * time.Second}
	// Name the service that answered. A project runs several host services;
	// "healthz=ok" without a subject says nothing about which one.
	subject := addr
	if env.Target.Service != "" {
		subject = env.Target.Service + " " + addr
	}

	probe := env.Target.Probe
	if probe.TCP {
		conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return CheckResult{Status: StatusFail, Message: fmt.Sprintf("%s: not accepting connections (its declared tcp probe): %v", subject, err)}
		}
		_ = conn.Close()
		return CheckResult{Status: StatusPass, Message: subject + ": accepts connections (its declared tcp probe)"}
	}
	if probe.ReadinessPath != "" || probe.LivenessPath != "" || probe.StartupPath != "" {
		// The app's own endpoints: any 200 is up. Only forge's /healthz
		// promises the "ok" body. A declared startup probe goes first: until
		// it answers, the workload has not started, whatever the others say.
		var paths []string
		if probe.StartupPath != "" {
			paths = append(paths, probe.StartupPath)
		}
		paths = append(paths, orPath(probe.LivenessPath, "/healthz"), orPath(probe.ReadinessPath, "/readyz"))
		var answered []string
		for _, path := range paths {
			if _, err := httpGetBody(ctx, client, fmt.Sprintf("http://%s%s", addr, path)); err != nil {
				return CheckResult{Status: StatusFail, Message: fmt.Sprintf("%s: %s failed (its declared probe): %v", subject, path, err)}
			}
			answered = append(answered, path+"=200")
		}
		return CheckResult{Status: StatusPass, Message: subject + ": " + strings.Join(answered, " ")}
	}

	// Check /healthz
	healthzBody, err := httpGetBody(ctx, client, fmt.Sprintf("http://%s/healthz", addr))
	if err != nil {
		return CheckResult{Status: StatusFail, Message: fmt.Sprintf("%s: healthz failed: %v", subject, err)}
	}
	healthzStr := strings.TrimSpace(healthzBody)
	if healthzStr != "ok" {
		return CheckResult{
			Status:   StatusFail,
			Message:  fmt.Sprintf("%s: healthz returned %q, expected \"ok\"", subject, healthzStr),
			Evidence: healthzBody,
		}
	}

	// Check /readyz
	_, err = httpGetBody(ctx, client, fmt.Sprintf("http://%s/readyz", addr))
	if err != nil {
		return CheckResult{Status: StatusFail, Message: fmt.Sprintf("%s: readyz failed: %v", subject, err)}
	}

	return CheckResult{Status: StatusPass, Message: subject + ": healthz=ok readyz=ok"}
}

func orPath(path, def string) string {
	if path == "" {
		return def
	}
	return path
}

// CheckPprof verifies the pprof debug endpoint is reachable and reports
// the available profile types.
func CheckPprof(ctx context.Context, env *Environment) CheckResult {
	addr, ok := env.GetPort("app", 6060)
	if !ok {
		return CheckResult{
			Status: StatusUnknown,
			Message: "could not resolve a pprof address" +
				" — declare PPROF_ADDR on the host service (serverkit binds pprof on its own listener)",
		}
	}

	client := &http.Client{Timeout: 3 * time.Second}

	body, err := httpGetBody(ctx, client, fmt.Sprintf("http://%s/debug/pprof/", addr))
	if err != nil {
		return CheckResult{Status: StatusFail, Message: fmt.Sprintf("pprof failed: %v", err)}
	}

	// Parse rows like: <tr><td>91</td><td><a href='allocs?debug=1'>allocs</a></td></tr>
	re := regexp.MustCompile(`<td>(\d+)</td><td><a href='[^']*'>([^<]+)</a></td>`)
	matches := re.FindAllStringSubmatch(body, -1)

	if len(matches) == 0 {
		return CheckResult{
			Status:   StatusPass,
			Message:  "pprof reachable (no profile rows parsed)",
			Evidence: body,
		}
	}

	var parts []string
	for _, m := range matches {
		parts = append(parts, fmt.Sprintf("%s=%s", m[2], m[1]))
	}
	sort.Strings(parts)

	if owner := pprofOwnerMismatch(ctx, client, addr, env.ProjectName); owner != "" {
		return CheckResult{
			Status: StatusWarn,
			Message: fmt.Sprintf("the pprof listener on %s belongs to another process (%s), not %s — "+
				"this app's own listener did not bind; give it a free PPROF_ADDR", addr, owner, env.ProjectName),
		}
	}

	return CheckResult{
		Status:  StatusPass,
		Message: strings.Join(parts, " "),
	}
}

// pprofOwnerMismatch returns the command line of the process answering pprof
// at addr when it is evidently NOT this project's binary, and "" otherwise.
//
// A loopback pprof port is machine-wide. When the app loses the bind (it
// logs "pprof listener unavailable" and carries on), whatever process holds
// the port still answers /debug/pprof/, and the check reported that process's
// profiles as the app's. /debug/pprof/cmdline names the responder. An
// unreadable answer is not evidence of a mismatch, so it reports nothing.
func pprofOwnerMismatch(ctx context.Context, client *http.Client, addr, project string) string {
	if project == "" {
		return ""
	}
	cmdline, err := httpGetBody(ctx, client, fmt.Sprintf("http://%s/debug/pprof/cmdline", addr))
	if err != nil || strings.TrimSpace(cmdline) == "" {
		return ""
	}
	args := strings.Fields(strings.ReplaceAll(cmdline, "\x00", " "))
	if len(args) == 0 || strings.Contains(strings.Join(args, " "), project) {
		return ""
	}
	return args[0]
}

// httpGetBody performs a GET request and returns the response body as a string.
// It returns an error if the status code is not 200.
func httpGetBody(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return string(b), fmt.Errorf("status %d", resp.StatusCode)
	}

	return string(b), nil
}
