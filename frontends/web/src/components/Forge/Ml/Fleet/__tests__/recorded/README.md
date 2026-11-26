# Recorded ML fleet responses

`realData.render.test.tsx` serves these files through a router transport and
renders Fleet, Node, GPU and Setup over them. Each file is the protojson of a
`controlplane.v1.MlEarlyAccessService` response.

**They are real recordings.** control-plane's `e2e/ml_fleet` writes them.
That e2e calls the real B1 handlers (`GetMlFleet`, `GetMlNode`, `GetMlDevice`,
`GetMlStack`), which read real telemetry from the k3d cluster `ml-fleet-e2e`:
2 k3d agents and 2 KWOK nodes, each with 8 × `NVIDIA-A100-SXM4-80GB` from
fake-gpu-operator 0.2.1. Agent 0 carries a `GpuXidError` condition. The
recording normalises node names and GPU UUIDs. Do not edit these files by hand.

Regenerate them in control-plane (needs docker and k3d), then copy them here:

```bash
cd ../control-plane
ML_FLEET_E2E=1 go test -tags e2e ./e2e/ml_fleet/ -run TestMLFleet -update
for n in get_ml_fleet get_ml_node get_ml_device get_ml_stack; do
  cp e2e/ml_fleet/testdata/recorded/$n.json \
    ../reliant/frontends/web/src/components/Forge/Ml/Fleet/__tests__/recorded/
done
```

To copy from the remote branch instead of a local checkout, use
`git show FETCH_HEAD:e2e/ml_fleet/testdata/recorded/<name>.json`.

The test builds the stale cluster, zero-GPU cluster and empty cases inline,
as variants of these files.

How "not collected" appears on the wire:

- A headline double sends `"NaN"`.
- An absent double means 0, a real zero. An absent `usd_*` field therefore
  means no price sheet.
- An `MlFieldValue` carries `collection: ML_COLLECTION_NOT_COLLECTED`.
