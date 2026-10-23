package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const k3sNodeIPDriftFailure = "failed to find interface with specified node ip"

var (
	k3sNodeIPRepairTimeout      = 90 * time.Second
	k3sNodeIPRepairPollInterval = 2 * time.Second

	// nodeReadyProbeTimeout bounds ONE readiness probe end to end, and
	// nodeReadyProbeCondition is the condition budget handed to kubectl
	// inside it. kubectl's --timeout covers only the wait for the
	// condition: TLS setup, API discovery and the initial list all happen
	// before it starts counting. `forge env up` runs this probe on the
	// same host it is about to build on, so the outer budget carries
	// deliberate headroom over the inner one — a loaded but healthy host
	// must not read as a down API.
	nodeReadyProbeTimeout   = 15 * time.Second
	nodeReadyProbeCondition = 5 * time.Second

	// k3sProcessGrace is how long "no k3s process" has to stay true before it
	// counts as EXITED. k3d's entrypoint does its own setup (mounts, iptables,
	// the registry config) before it execs k3s, so a node sampled moments after
	// a restart legitimately has no k3s yet. A single sample there would
	// misreport a healthy node that is merely starting as a dead one — sending
	// forge into the log-diagnosis path, where it would find no failure
	// signature (nothing has failed) and abort with a misleading error.
	k3sProcessGrace        = 20 * time.Second
	k3sProcessPollInterval = 2 * time.Second

	// k3sObserveTimeout bounds a foreground diagnostic start. The drift
	// failure surfaces within seconds of "Starting k3s"; a start that runs
	// this long without failing has nothing to show.
	k3sObserveTimeout = 60 * time.Second

	runningClusterNodesReadyFn = clusterNodesReadyOnce
	containerHasK3sProcessFn   = containerHasK3sProcess
	inspectDockerK3dNodeFn     = inspectDockerK3dNode
	dockerLogsSinceStartFn     = dockerLogsSinceStart
	observeK3sStartFn          = observeK3sStart
	repairK3sNodeIPDriftFn     = repairK3sNodeIPDrift
	nodeReadyHeartbeatsFn      = kubectlNodeReadyHeartbeats
	nodeContainerStartedAtFn   = containerStartedAt
)

// errK3sExitedDuringWait reports that k3s was alive when a readiness wait
// began and gone before the API confirmed Ready.
var errK3sExitedDuringWait = errors.New("k3s exited before its nodes reported Ready")

// dockerK3dNodeInspect is the subset of `docker container inspect` needed to
// recover a k3d server after Docker assigns it a different dynamic IP. Docker
// reports the container as running even when the k3d entrypoint's k3s child
// has exited, so container state alone is not a Kubernetes health signal.
type dockerK3dNodeInspect struct {
	Args  []string `json:"Args"`
	State struct {
		StartedAt string `json:"StartedAt"`
	} `json:"State"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

// ensureRunningK3dClusterHealthy closes the gap between Docker liveness and
// Kubernetes liveness. In particular, k3d's entrypoint can remain alive in an
// uncordon retry loop after k3s fatally exits because Docker reassigned the
// node IP. For that exact, known failure we repair the persisted Kubernetes
// Node IP in place and then restart k3s normally with network policy enabled.
func ensureRunningK3dClusterHealthy(ctx context.Context, c ClusterEntity) error {
	ready, probeErr := runningClusterNodesReadyFn(ctx, c.Name)
	if ready {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	node := "k3d-" + c.Name + "-server-0"
	k3sRunning, err := waitForK3sProcess(ctx, node)
	if err != nil {
		return err
	}
	if k3sRunning {
		// k3s is alive, so this is a not-yet rather than a failure — but say
		// WHAT did not answer. The probe reason distinguishes a cluster that
		// is genuinely still coming up from a transient blip on a warm one
		// (a k3d node whose Docker IP is churning answers intermittently),
		// and the two want very different reactions from the operator.
		fmt.Printf("  cluster %q did not confirm nodes Ready: %v\n", c.Name, probeErr)
		fmt.Printf("  k3s is running in %s, so this is a not-yet, not a failure — retrying for up to %s...\n",
			node, nodeReadyWaitBudget)
		// "Running" is a sample, not a verdict. The node-IP drift failure
		// kills k3s a few seconds AFTER it logs "k3s is up and running" and
		// its API starts answering, so a node sampled in that window looks
		// alive. Keep watching the process while waiting: if it exits, this
		// is the failure path below, not a timeout to report.
		waitErr := waitNodeReadyWhile(ctx, c.Name, func(ctx context.Context) (bool, error) {
			return waitForK3sProcess(ctx, node)
		})
		if !errors.Is(waitErr, errK3sExitedDuringWait) {
			return waitErr
		}
		fmt.Printf("  k3s in %s exited while forge waited for its API — diagnosing why...\n", node)
		probeErr = waitErr
	}

	info, err := inspectDockerK3dNodeFn(ctx, node)
	if err != nil {
		return err
	}
	logs, err := dockerLogsSinceStartFn(ctx, node, info.State.StartedAt)
	if err != nil {
		return err
	}
	if !strings.Contains(logs, k3sNodeIPDriftFailure) {
		// `docker logs` is not a reliable witness. A Docker Desktop VM that
		// stops uncleanly can leave NUL bytes in a container's json-file log,
		// and Docker's reader silently returns nothing past them — for the
		// rest of that container's life. Observe a start directly instead of
		// concluding "unknown failure" from logs that cannot be read.
		fmt.Printf("  %s's current-start logs do not say why k3s exited — starting k3s once in the foreground to observe it...\n", node)
		observed, observeErr := observeK3sStartFn(ctx, node, info.Args)
		if observeErr != nil {
			return fmt.Errorf(
				"container %s is running but its k3s process exited; its current-start logs do not show why, and a foreground start could not be observed: %w. The Kubernetes API reported: %v",
				node, observeErr, probeErr)
		}
		if !strings.Contains(observed, k3sNodeIPDriftFailure) {
			return fmt.Errorf(
				"container %s is running but its k3s process exited; neither its current-start logs nor a foreground start showed the known Docker node-IP drift failure. The Kubernetes API reported: %v\nLast output of the foreground start:%s",
				node, probeErr, formatCommandStderr(observed))
		}
	}
	if effectiveServers(c) != 1 || c.Agents != 0 {
		return fmt.Errorf(
			"detected Docker node-IP drift in %s, but automatic in-place repair is limited to single-server clusters without agents (servers=%d agents=%d)",
			node, effectiveServers(c), c.Agents)
	}

	currentIP, err := k3dNodeCurrentIP(info, c)
	if err != nil {
		return fmt.Errorf("resolve current IP for %s: %w", node, err)
	}
	fmt.Printf("  detected k3s node-IP drift in %q after Docker reassigned %s to %s — repairing in place...\n",
		c.Name, node, currentIP)
	return repairK3sNodeIPDriftFn(ctx, c, node, currentIP, info.Args)
}

// clusterNodesReadyOnce is a quick healthy-path probe. A healthy warm cluster
// returns immediately; an API that is merely starting gets a bounded grace
// period before the deeper process/log diagnosis runs.
//
// It returns the FAILURE REASON alongside the verdict. A probe that collapses
// every distinguishable failure — no such kubectl context, connection refused,
// an expired client certificate, an answer that merely arrived slowly — into a
// bare false leaves the operator, and the next reader of the logs, with nothing
// to act on, and leaves forge asserting a diagnosis ("the API is still
// starting") it never actually established. docker_preflight.go makes the same
// argument for the Docker daemon; this is the Kubernetes half of it.
func clusterNodesReadyOnce(ctx context.Context, clusterName string) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, nodeReadyProbeTimeout)
	defer cancel()

	kctx := "k3d-" + clusterName
	out, err := exec.CommandContext(probeCtx, "kubectl", "--context", kctx,
		"wait", "--for=condition=Ready", "nodes", "--all",
		"--timeout="+nodeReadyProbeCondition.String()).CombinedOutput()
	if err == nil {
		if err := verifyNodesReadyThisBoot(probeCtx, kctx); err != nil {
			return false, err
		}
		return true, nil
	}
	// Only the parent ctx being live tells us the deadline was OURS; a
	// cancelled parent means the user interrupted, not a slow API.
	if errors.Is(probeCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return false, fmt.Errorf("kubectl did not answer within %s%s",
			nodeReadyProbeTimeout, formatCommandStderr(string(out)))
	}
	return false, fmt.Errorf("%w%s", err, formatCommandStderr(string(out)))
}

// verifyNodesReadyThisBoot rejects a Ready verdict the CURRENT boot did not
// produce. Node conditions are persisted in the datastore, so the instant a
// restarted k3s API answers, every node still reads Ready=True as written by
// the kubelet of the PREVIOUS boot — and keeps reading it until the node
// lifecycle controller's grace period expires. `kubectl wait
// --for=condition=Ready` accepts that stale record immediately. A k3s that is
// about to exit (Docker node-IP drift kills it seconds after its API comes up)
// therefore passes the wait, forge reports the cluster healthy, and the next
// step fails with an opaque "Unable to connect to the server: EOF".
//
// The kubelet stamps lastHeartbeatTime on its first status sync after it
// starts, so a heartbeat older than the node container's start is proof the
// Ready status is a leftover. Nodes that are not containers on this Docker
// host (zero start time) are not judged.
func verifyNodesReadyThisBoot(ctx context.Context, kctx string) error {
	heartbeats, err := nodeReadyHeartbeatsFn(ctx, kctx)
	if err != nil {
		return err
	}
	started := make(map[string]time.Time, len(heartbeats))
	for node := range heartbeats {
		startedAt, err := nodeContainerStartedAtFn(ctx, node)
		if err != nil {
			return err
		}
		started[node] = startedAt
	}
	if stale := staleReadyNodes(heartbeats, started); len(stale) > 0 {
		return fmt.Errorf(
			"node(s) %s read Ready only from status persisted before the node container last started — this boot's kubelet has not reported yet",
			strings.Join(stale, ", "))
	}
	return nil
}

// staleReadyNodes returns, sorted, the nodes whose Ready heartbeat predates
// their container's start. Heartbeats carry whole-second precision, so the
// start is truncated to match before comparing.
func staleReadyNodes(heartbeats, started map[string]time.Time) []string {
	var stale []string
	for node, heartbeat := range heartbeats {
		startedAt := started[node]
		if startedAt.IsZero() {
			continue
		}
		if heartbeat.IsZero() || heartbeat.Before(startedAt.Truncate(time.Second)) {
			stale = append(stale, node)
		}
	}
	sort.Strings(stale)
	return stale
}

// kubectlNodeReadyHeartbeats reads every node's Ready-condition
// lastHeartbeatTime. A node without one maps to the zero time.
func kubectlNodeReadyHeartbeats(ctx context.Context, kctx string) (map[string]time.Time, error) {
	out, err := exec.CommandContext(ctx, "kubectl", "--context", kctx, "get", "nodes", "-o",
		`jsonpath={range .items[*]}{.metadata.name}{"\t"}{.status.conditions[?(@.type=="Ready")].lastHeartbeatTime}{"\n"}{end}`).
		CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("read node heartbeats in %s: %w%s", kctx, err, formatCommandStderr(string(out)))
	}
	return parseNodeReadyHeartbeats(string(out))
}

func parseNodeReadyHeartbeats(out string) (map[string]time.Time, error) {
	heartbeats := map[string]time.Time{}
	for _, line := range strings.Split(out, "\n") {
		name, stamp, _ := strings.Cut(strings.TrimSpace(line), "\t")
		if name == "" {
			continue
		}
		var heartbeat time.Time
		if stamp = strings.TrimSpace(stamp); stamp != "" {
			parsed, err := time.Parse(time.RFC3339, stamp)
			if err != nil {
				return nil, fmt.Errorf("parse Ready heartbeat %q for node %s: %w", stamp, name, err)
			}
			heartbeat = parsed
		}
		heartbeats[name] = heartbeat
	}
	return heartbeats, nil
}

// waitForK3sProcess reports whether k3s is running inside the node, treating
// absence as inconclusive until k3sProcessGrace has elapsed. Present returns
// immediately; only a sustained absence is reported as exited.
func waitForK3sProcess(ctx context.Context, node string) (bool, error) {
	deadline := time.Now().Add(k3sProcessGrace)
	for {
		running, err := containerHasK3sProcessFn(ctx, node)
		if err != nil || running {
			return running, err
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(k3sProcessPollInterval):
		}
	}
}

func containerHasK3sProcess(ctx context.Context, node string) (bool, error) {
	out, err := exec.CommandContext(ctx, "docker", "exec", node, "pidof", "k3s").CombinedOutput()
	if err == nil {
		return strings.TrimSpace(string(out)) != "", nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("inspect k3s process in %s: %w: %s", node, err, strings.TrimSpace(string(out)))
}

func inspectDockerK3dNode(ctx context.Context, node string) (dockerK3dNodeInspect, error) {
	out, err := exec.CommandContext(ctx, "docker", "container", "inspect", node).CombinedOutput()
	if err != nil {
		return dockerK3dNodeInspect{}, fmt.Errorf("inspect %s: %w: %s", node, err, strings.TrimSpace(string(out)))
	}
	var inspected []dockerK3dNodeInspect
	if err := json.Unmarshal(out, &inspected); err != nil {
		return dockerK3dNodeInspect{}, fmt.Errorf("decode docker inspect for %s: %w", node, err)
	}
	if len(inspected) != 1 {
		return dockerK3dNodeInspect{}, fmt.Errorf("docker inspect for %s returned %d records; want 1", node, len(inspected))
	}
	return inspected[0], nil
}

func dockerLogsSinceStart(ctx context.Context, node, startedAt string) (string, error) {
	args := []string{"logs"}
	if startedAt != "" {
		args = append(args, "--since", startedAt)
	} else {
		args = append(args, "--tail", "500")
	}
	args = append(args, node)
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("read current-start logs for %s: %w: %s", node, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func k3dNodeCurrentIP(info dockerK3dNodeInspect, c ClusterEntity) (string, error) {
	network := c.Network
	if network == "" {
		network = "k3d-" + c.Name
	}
	if endpoint, ok := info.NetworkSettings.Networks[network]; ok && endpoint.IPAddress != "" {
		return endpoint.IPAddress, nil
	}
	if len(info.NetworkSettings.Networks) == 1 {
		for _, endpoint := range info.NetworkSettings.Networks {
			if endpoint.IPAddress != "" {
				return endpoint.IPAddress, nil
			}
		}
	}
	return "", fmt.Errorf("network %q has no container IP", network)
}

func safeModeK3sArgs(containerArgs []string) ([]string, error) {
	if len(containerArgs) == 0 || containerArgs[0] != "server" {
		return nil, fmt.Errorf("unexpected k3d server command args %q", containerArgs)
	}
	args := make([]string, 0, len(containerArgs)+1)
	for _, arg := range containerArgs {
		if arg == "--disable-network-policy" || strings.HasPrefix(arg, "--disable-network-policy=") {
			continue
		}
		args = append(args, arg)
	}
	return append(args, "--disable-network-policy"), nil
}

func findK3dEntrypointPID(processes string) (int, error) {
	for _, line := range strings.Split(processes, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.Contains(strings.Join(fields[1:], " "), "/bin/k3d-entrypoint.sh") {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid == 1 {
			continue
		}
		return pid, nil
	}
	return 0, fmt.Errorf("could not find non-PID-1 k3d entrypoint shell")
}

func k3dEntrypointPID(ctx context.Context, node string) (int, error) {
	out, err := exec.CommandContext(ctx, "docker", "exec", node, "ps", "-eo", "pid,args").CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("list processes in %s: %w: %s", node, err, strings.TrimSpace(string(out)))
	}
	pid, err := findK3dEntrypointPID(string(out))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", node, err)
	}
	return pid, nil
}

// observeK3sStart starts the node's k3s server once, in the foreground and
// with its original arguments, and returns the tail of what it printed. It is
// the diagnosis of last resort for a node whose k3s exited without leaving a
// readable trace in `docker logs`.
//
// k3d's entrypoint is paused for the duration, as in repairK3sNodeIPDrift:
// its uncordon loop would otherwise succeed against the observed k3s, fall
// through to `wait` on the original (dead) k3s, exit, and let Docker restart
// the container mid-observation. The observed k3s is always stopped before
// the entrypoint resumes, leaving the node exactly as it was found.
func observeK3sStart(ctx context.Context, node string, containerArgs []string) (string, error) {
	if len(containerArgs) == 0 || containerArgs[0] != "server" {
		return "", fmt.Errorf("unexpected k3d server command args %q", containerArgs)
	}
	entrypointPID, err := k3dEntrypointPID(ctx, node)
	if err != nil {
		return "", err
	}
	if out, err := exec.CommandContext(ctx, "docker", "exec", node,
		"kill", "-STOP", strconv.Itoa(entrypointPID)).CombinedOutput(); err != nil {
		return "", fmt.Errorf("pause k3d entrypoint in %s: %w: %s", node, err, strings.TrimSpace(string(out)))
	}
	defer func() {
		_ = exec.Command("docker", "exec", node, "killall", "-TERM", "k3s").Run()
		waitK3sStopped(node)
		_ = exec.Command("docker", "exec", node, "kill", "-CONT", strconv.Itoa(entrypointPID)).Run()
	}()

	observeCtx, cancel := context.WithTimeout(ctx, k3sObserveTimeout)
	defer cancel()
	watcher := newK3sStartWatcher(40, cancel)
	cmd := exec.CommandContext(observeCtx, "docker", append([]string{"exec", node, "/bin/k3s"}, containerArgs...)...)
	cmd.Stdout = watcher
	cmd.Stderr = watcher
	cmd.WaitDelay = 5 * time.Second
	runErr := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if runErr != nil && observeCtx.Err() == nil && watcher.Lines() == "" {
		return "", fmt.Errorf("docker exec %s /bin/k3s: %w", node, runErr)
	}
	return watcher.Lines(), nil
}

// waitK3sStopped gives a TERMed k3s a bounded moment to exit, so the
// entrypoint is never resumed while a second k3s still holds the ports.
func waitK3sStopped(node string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for {
		running, err := containerHasK3sProcess(ctx, node)
		if err != nil || !running {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// k3sStartWatcher is the io.Writer an observed k3s start prints into. It keeps
// the last few lines and calls stop as soon as the drift signature appears —
// the observation has its answer, and every further second is a second k3s
// spends half-started on someone's machine.
type k3sStartWatcher struct {
	mu      sync.Mutex
	partial []byte
	tail    []string
	limit   int
	stop    func()
}

func newK3sStartWatcher(limit int, stop func()) *k3sStartWatcher {
	return &k3sStartWatcher{limit: limit, stop: stop}
}

func (w *k3sStartWatcher) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.partial = append(w.partial, p...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		w.addLine(string(w.partial[:i]))
		w.partial = w.partial[i+1:]
	}
	return len(p), nil
}

func (w *k3sStartWatcher) addLine(line string) {
	w.tail = append(w.tail, line)
	if len(w.tail) > w.limit {
		w.tail = w.tail[len(w.tail)-w.limit:]
	}
	if strings.Contains(line, k3sNodeIPDriftFailure) && w.stop != nil {
		w.stop()
	}
}

// Lines returns the retained tail, including any unterminated final line.
func (w *k3sStartWatcher) Lines() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	lines := w.tail
	if len(w.partial) > 0 {
		lines = append(slices.Clone(lines), string(w.partial))
	}
	return strings.Join(lines, "\n")
}

// repairK3sNodeIPDrift performs the upstream-recommended safe-mode recovery:
// pause k3d's otherwise-infinite uncordon loop, start the same k3s server once
// with network policy disabled, wait for kubelet to publish the container's
// current IP, stop safe mode, and restart the original container command.
// Network policy is therefore disabled only during repair and is enabled again
// before this function returns.
func repairK3sNodeIPDrift(ctx context.Context, c ClusterEntity, node, currentIP string, containerArgs []string) error {
	safeArgs, err := safeModeK3sArgs(containerArgs)
	if err != nil {
		return err
	}
	entrypointPID, err := k3dEntrypointPID(ctx, node)
	if err != nil {
		return err
	}

	paused := false
	safeK3sStarted := false
	defer func() {
		if safeK3sStarted {
			_ = exec.Command("docker", "exec", node, "killall", "-TERM", "k3s").Run()
		}
		if paused {
			_ = exec.Command("docker", "exec", node, "kill", "-CONT", strconv.Itoa(entrypointPID)).Run()
		}
	}()

	if out, err := exec.CommandContext(ctx, "docker", "exec", node,
		"kill", "-STOP", strconv.Itoa(entrypointPID)).CombinedOutput(); err != nil {
		return fmt.Errorf("pause k3d entrypoint in %s: %w: %s", node, err, strings.TrimSpace(string(out)))
	}
	paused = true

	args := []string{"exec", "-d", node, "/bin/k3s"}
	args = append(args, safeArgs...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("start temporary network-policy-disabled k3s in %s: %w", node, err)
	}
	safeK3sStarted = true

	if err := waitForK3sNodeInternalIP(ctx, c.Name, node, currentIP); err != nil {
		return err
	}

	if out, err := exec.CommandContext(ctx, "docker", "exec", node,
		"killall", "-TERM", "k3s").CombinedOutput(); err != nil {
		return fmt.Errorf("stop temporary k3s in %s: %w: %s", node, err, strings.TrimSpace(string(out)))
	}
	safeK3sStarted = false
	if out, err := exec.CommandContext(ctx, "docker", "exec", node,
		"kill", "-CONT", strconv.Itoa(entrypointPID)).CombinedOutput(); err != nil {
		return fmt.Errorf("resume k3d entrypoint in %s: %w: %s", node, err, strings.TrimSpace(string(out)))
	}
	paused = false

	fmt.Printf("  node %s now advertises %s — restarting with network policy enabled...\n", node, currentIP)
	if err := dockerRestart(ctx, node); err != nil {
		return fmt.Errorf("restart repaired node %s: %w", node, err)
	}
	if err := refreshK3dLoadBalancer(ctx, c.Name); err != nil {
		return fmt.Errorf("refresh %s load balancer after node-IP repair: %w", c.Name, err)
	}
	if err := waitNodeReady(ctx, c.Name); err != nil {
		return fmt.Errorf("wait for repaired cluster %q: %w", c.Name, err)
	}
	return nil
}

func waitForK3sNodeInternalIP(ctx context.Context, clusterName, node, wantIP string) error {
	deadline := time.NewTimer(k3sNodeIPRepairTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(k3sNodeIPRepairPollInterval)
	defer ticker.Stop()
	kctx := "k3d-" + clusterName

	for {
		out, err := exec.CommandContext(ctx, "kubectl", "--request-timeout=2s", "--context", kctx,
			"get", "node", node,
			"-o", "jsonpath={.status.addresses[?(@.type==\"InternalIP\")].address}").Output()
		if err == nil && strings.TrimSpace(string(out)) == wantIP {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("temporary k3s did not update node %s to current IP %s within %s", node, wantIP, k3sNodeIPRepairTimeout)
		case <-ticker.C:
		}
	}
}
