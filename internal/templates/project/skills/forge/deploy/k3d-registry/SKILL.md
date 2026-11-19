---
name: k3d-registry
description: The k3d local-registry mirror (`localhost:5050` ↔ `registry.localhost:5000`) — why in-cluster pulls need it, what forge configures automatically, and how to fix a pre-existing cluster that ImagePullBackOffs.
---

# k3d local-registry mirror (`localhost:5050` ↔ `registry.localhost:5000`)

`forge env deploy dev` builds and pushes images to host-side
`localhost:5050`. In-cluster pulls hit `registry.localhost:5000`. A
containerd mirror config bridges the two — without it, `docker push`
succeeds and pods `ImagePullBackOff` because `localhost:5050` doesn't
resolve from inside the node container.

Forge-managed k3d clusters get the mirror automatically:

- The project-templated `deploy/k3d.yaml` carries the mirrors inline
  (`registries.config` block of the k3d Simple config).
- The fallback `forge env deploy dev` create path (no `deploy/k3d.yaml`)
  writes a temp `registries.yaml` and passes `--registry-config` to
  `k3d cluster create`.

**Pre-existing k3d cluster mirror fix.** If your cluster pre-dates this
behavior — `forge env deploy dev` reuses the existing cluster, and pulls
fail with `localhost:5050: connection refused` from inside the node —
add the mirror to the running node container directly:

```bash
# Replace `k3d-dev-server-0` with your cluster's server node name
# (`docker ps --filter name=k3d`).
docker exec k3d-dev-server-0 sh -c 'cat > /etc/rancher/k3s/registries.yaml <<EOF
mirrors:
  "registry.localhost:5000":
    endpoint:
      - http://registry.localhost:5000
  "registry.localhost:5050":
    endpoint:
      - http://registry.localhost:5000
  "localhost:5050":
    endpoint:
      - http://registry.localhost:5000
EOF
'
docker restart k3d-dev-server-0
```

Or, simpler: `k3d cluster delete dev && forge env deploy dev` recreates
the cluster with the mirror in place.
