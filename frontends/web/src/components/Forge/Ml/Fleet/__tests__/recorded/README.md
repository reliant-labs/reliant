# Recorded ML fleet responses

`realData.render.test.tsx` serves these files (protojson of
`controlplane.v1.MlEarlyAccessService` responses) through a router transport
and renders Fleet, Node, GPU and Setup over them.

**They are hand-written stand-ins.** They follow the Phase B1 data contract
for what control-plane's B1 handlers (`GetMlFleet`, `GetMlNode`,
`GetMlDevice`, `GetMlStack`) answer for the k3d cluster `ml-fleet-e2e`: two k3d
agents (`k3d-ml-fleet-e2e-agent-0/1`) and two KWOK nodes (`kwok-gpu-0/1`), each
with 8 × `NVIDIA-A100-SXM4-80GB` from fake-gpu-operator 0.2.1. Agent 0 is
unhealthy (a `GpuXidError` condition). One GPU on agent 1 runs at about 70%
utilisation for pod `default/gpu-burn`. Only `DCGM_FI_DEV_GPU_UTIL`, `FB_USED`
and `FB_FREE` are collected, so power, temperature, clocks, Xid, ECC, SM active
and every cost field are not collected, and workloads, NVLink, InfiniBand,
host, diagnostics and incarnations are empty.

They will be replaced by the files control-plane's e2e records
(`control-plane e2e/ml_fleet/testdata/recorded/`, written with `-update`) once
that lands. Copy those files over these and keep the names the same:
`get_ml_fleet.json`, `get_ml_node.json`, `get_ml_device.json` and
`get_ml_stack.json`.

"Not collected" on the wire: a headline double the contract documents as
"NaN when not collected" is sent as `"NaN"`. An absent double is 0, a real zero.
An `MlFieldValue` uses `collection: ML_COLLECTION_NOT_COLLECTED`.
