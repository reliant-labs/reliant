---
name: multi-cluster-e2e
description: Debug a k3d multi-cluster e2e env — read the pod status before theorizing the network, hold the pod up on failure, and the nested-cluster gotchas (image pull, host-gateway DNS, path-MTU) that bite only across clusters.
emit: both
---

# Debugging a Multi-Cluster e2e Env

## When to Use

A workload stays `Pending` / not-ready, or a flow that passes in a single-cluster
dev env fails in a **k3d multi-cluster e2e env** (a secondary k3d cluster joined
to the primary cluster's docker network). The class of bug here is almost never
app logic — it's image-pull, node-setup, or cross-cluster networking that a
single-cluster dev loop never exercises.

## Golden Rule: Read the Workload Error First

Before you touch ports, DNS, or the message bus, get the **actual** pod status
and the container's real error. A stuck workload is overwhelmingly an
image-pull or node-setup gap, not a wire problem.

```bash
kubectl --context <ctx> -n <ns> get pod              # READ the STATUS column first
# Init:ImagePullBackOff, ErrImagePull, CreateContainerError → image/node setup, NOT app logic
kubectl --context <ctx> -n <ns> describe pod <pod>   # Events: the real pull/mount error
kubectl --context <ctx> -n <ns> logs <pod> -c <container> --previous   # last crash, if it ran
```

Chasing the network layer while the pod says `ImagePullBackOff` wastes hours.
The STATUS column points at the layer that's actually broken.

## Hold the Pod Up on Failure (so you can exec in)

An e2e test that tears down the workload pod on failure makes live debugging
impossible — by the time you look, the pod is gone. Gate teardown behind an
opt-in env flag so a failing run **blocks with the pod still up**:

```go
// In your e2e harness, on failure:
func holdOnFail(t *testing.T, ctx, ns, pod string) {
    if t.Failed() && os.Getenv("E2E_HOLD_ON_FAIL") == "1" {
        t.Logf("HOLD: pod still up — kubectl --context %s -n %s exec -it %s -- sh", ctx, ns, pod)
        select {} // block; Ctrl-C to release. Skip t.Cleanup teardown of the pod.
    }
}
```

```bash
E2E_HOLD_ON_FAIL=1 <run the e2e suite>     # leaves the pod live on first failure
# then, in another shell, reproduce by hand:
kubectl --context <ctx> -n <ns> exec -it <pod> -- sh
```

Reproducing the failing step by hand inside the real pod is worth ten rounds of
log-staring.

## Nested-Cluster Gotchas (don't reproduce in single-cluster dev)

A secondary k3d cluster joined to another cluster's docker network has three
failure modes the primary cluster hides. Each is symptom → cause → diagnostic →
fix.

### 1. ImagePullBackOff: `lookup <registry-host>: no such host`

- **Cause:** the registry mirror was *written* to the node's `registries.yaml`
  but containerd never *loaded* it (config is read at containerd start).
- **Diagnostic:** confirm the catalog is reachable and the host maps:
  ```bash
  curl -s http://<registry-host>:<port>/v2/_catalog        # is the image even pushed?
  kubectl --context <ctx> get node -o wide                  # which node pulls
  docker exec <node-container> cat /etc/rancher/k3s/registries.yaml   # is the mirror there?
  ```
- **Fix:** ensure `registries.yaml` maps the image's registry host, **and
  restart the node** so containerd reloads it (`docker restart <node-container>`,
  or recreate the node).

### 2. NXDOMAIN on `host.k3d.internal` (or any host-gateway alias)

- **Cause:** k3d injects the host-gateway alias into CoreDNS on the **owner
  cluster only**. A pod on the secondary cluster resolving the same name gets
  NXDOMAIN — the alias isn't in its CoreDNS `NodeHosts`.
- **Diagnostic:** compare the `NodeHosts` block across both clusters:
  ```bash
  kubectl --context <primary>   -n kube-system get cm coredns -o yaml | grep -A20 NodeHosts
  kubectl --context <secondary> -n kube-system get cm coredns -o yaml | grep -A20 NodeHosts
  ```
  If the alias is present on the primary and absent on the secondary, that's it.
- **Fix:** add the host-gateway alias to the secondary cluster's CoreDNS
  `NodeHosts` (or point the workload at a name that resolves on both).

### 3. TLS handshake dies but raw TCP works (path-MTU)

- **Symptom:** `git clone` / `curl https://…` fails
  `gnutls_handshake() ... The TLS connection was non-properly terminated`, yet a
  plain TCP connect to the same host:443 succeeds.
- **Cause:** the pod's interface MTU *number* is normal, but its **effective
  path MTU to the internet** is lower (extra encap on the nested cluster's
  network). Large TLS handshake packets (`Don't Fragment` set) get silently
  dropped; small packets pass — so TCP connects but the handshake hangs.
- **Diagnostic:** binary-search the real path MTU from inside the pod:
  ```bash
  kubectl --context <ctx> -n <ns> exec -it <pod> -- ping -M do -s 1472 <host>   # 1500 MTU; shrink -s until it stops failing
  # "message too long" / 100% loss at a size that should fit ⇒ path MTU < iface MTU
  ```
- **Fix:** clamp TCP MSS to the path MTU on the node's FORWARD chain:
  ```bash
  iptables -t mangle -A FORWARD -p tcp --tcp-flags SYN,RST SYN \
    -j TCPMSS --clamp-mss-to-pmtu
  # or pin it: --set-mss <pathMTU - 40>
  ```

## Diagnostic Recipes (generic)

| Question | Command |
|---|---|
| What image is this pod, and why won't it pull? | `kubectl -n <ns> get pod <p> -o jsonpath='{.spec.containers[*].image}'; kubectl -n <ns> describe pod <p>` |
| Is the image actually in the registry? | `curl -s http://<registry>/v2/_catalog` then `.../v2/<img>/tags/list` |
| Does a hostname resolve on BOTH clusters? | compare CoreDNS `NodeHosts` (recipe 2 above) |
| Is the path MTU smaller than the interface MTU? | `ping -M do -s <size> <host>` from inside the pod, shrink until it passes |
| Did a service race a message-bus stream/consumer at boot? | check the stream/consumer exists on the broker; if a consumer logs "stream not found" only on cold start, a producer created it after the consumer subscribed — a startup ordering race, not a delivery bug |

<!-- @forge-only:start -->
## In a Forge Project

- **Bring the stack up, then test:** `forge env up <env>` builds + deploys every
  workload to the kubectl context its `forge.OnCluster` target declares; `task test:e2e` runs the suite
  against the live multi-cluster stack. See the `forge/testing/e2e` skill.
- **The pod is still up after a failure** — `forge env up` leaves the stack running, so
  you can `kubectl exec` in without the `E2E_HOLD_ON_FAIL` dance, or
  `forge debug start` to attach Delve. The hold-on-fail flag still earns its keep
  when the suite itself owns short-lived per-test pods it would otherwise reap.
- **Capture the gotcha:** if a nested-cluster gotcha cost you real time, file
  it upstream so the generator can grow a guardrail (e.g. seeding the
  secondary cluster's CoreDNS or MSS clamp at `forge env up`).

### Forge Tooling for a Cross-Cluster App-Flow Bug

Worked example: a service dials a peer's pod IP across two k3d clusters, the
gateway logs a `write envelope: EOF` reconnect loop, and callers see
"no <peer> connected." Use the forge tools in this order — and mind where each
one stops short.

1. **Read pod status across BOTH clusters — directly with kubectl.**
   `forge cluster status` / `forge cluster instances` resolve a **single**
   cluster name from config; they will never enumerate the secondary cluster's
   pods. For a multi-cluster app, query each context yourself:
   ```bash
   kubectl config get-contexts -o name                  # discover both contexts
   kubectl --context <primary>   -n <ns> get pod -o wide
   kubectl --context <secondary> -n <ns> get pod -o wide
   ```
   Resolve each context from the service's `deploy` block in
   `deploy/kcl/<env>/main.k` (the `cluster` field IS the context).

2. **Pull the symptom from the gateway's own logs.**
   `forge cluster logs --service <gateway>` is kubectl-backed (no file-grepping),
   but it only tails the pod in the cluster whose name is in config — the
   **owner-cluster half**. Set the right context first; for the peer half, run
   `kubectl --context <secondary> -n <ns> logs <peer-pod>` directly.
   ```bash
   forge cluster logs --service <gateway> --follow=false --tail 200   # owner half
   ```

3. **Localize with `forge project introspect handlers`.**
   Prints every RPC path the assembled binary registers. If the failing RPC
   isn't in the list, the fault is a downstream/remote hop, not this binary —
   that alone collapses the search space to the cross-cluster edge.

4. **ASSERT the app-flow invariant with a declarative, exit-coded health check.**
   A green `forge env smoke` / `forge env status <env>` does **NOT** mean the app flow works:
   they check listeners, local compose, and telemetry — never app-flow
   invariants. The same is true of `forge cluster status`, which is green when
   every pod is `Running` regardless of whether the cross-cluster dial succeeds.
   Prove the fix with a declarative check that fails non-zero when the invariant
   is violated. Model to copy: a project `doctor:<flow>` task (e.g. the
   daemon-flow pattern — "is a peer actually connected and serving?") plus a full
   `task test:e2e`. Only those two **prove** the app-flow is healthy; the generic
   forge tools localize, they don't certify.

See also: `forge/debug/reproduce` (runtime evidence), `forge/debug/investigate`
(hypothesis ranking), `forge/dev` (cluster lifecycle primitives).
<!-- @forge-only:end -->
