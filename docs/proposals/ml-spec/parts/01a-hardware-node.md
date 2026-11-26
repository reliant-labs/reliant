# 01a — Hardware and node metrics (GPU, NPD, node/kubelet, other vendors)

<!--
  prettier-ignore: prettier 3.5 reads the unquoted metric names in this file
  (node_edac_*, num_retracted_*, …) as emphasis and rewrites them, and joins
  adjacent code spans, so this file is left as researched.
-->

<!-- prettier-ignore-start -->

Confidence: (V) = checked this session against a source in the list at the bottom; (K) = from knowledge, not checked this session.

## 0. A naming change that matters first (V)

dcgm-exporter `main` (late 2026) ships **renamed, unit-suffixed Prometheus names** in `etc/default-counters.csv`: `DCGM_FI_DEV_GPU_UTIL_RATIO` (0–1), `DCGM_FI_DEV_BOARD_POWER_WATTS`, `DCGM_FI_DEV_GPU_ENERGY_JOULES_TOTAL`, `DCGM_FI_DEV_FB_USED_BYTES`, `DCGM_FI_PROF_TENSOR_UTIL_RATIO`, `*_THROTTLE_SECONDS_TOTAL`, `DCGM_FI_DEV_ROW_REMAP_*_TOTAL`, plus exporter-own `DCGM_EXP_*` series. Older releases (the 3.3/4.x line most clusters run) emit the **legacy DCGM field names** with raw units: `DCGM_FI_DEV_GPU_UTIL` (%, 0–100), `DCGM_FI_DEV_POWER_USAGE` (W), `DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION` (mJ), `DCGM_FI_DEV_FB_USED` (MiB), `DCGM_FI_PROF_PIPE_TENSOR_ACTIVE` (0–1). **The ingest layer has to map both naming schemes onto one neutral field set, and normalise units** (% to ratio, MiB to bytes, mJ to J, µs to s). In the tables below the canonical DCGM field ID name comes first, and the new exporter name follows it where one exists.

Other renames (K):
- DCGM 3.3 / driver R535 renamed "clock throttle reasons" to **"clock event reasons"**. `DCGM_FI_DEV_CLOCK_THROTTLE_REASONS` became `DCGM_FI_DEV_CLOCKS_EVENT_REASONS`, and the field ID (112) is unchanged.
- DCGM 3 can read profiling (DCP) fields per MIG GPU instance.
- DCGM 4 split NVLink error counters by generation (pre-Hopper FLIT/DATA CRC, Hopper data-link CRC, Blackwell recovery-successful/failed/event) (V, from the CSV comments). It also added the C2C, fabric health and IMEX health watch fields.

## 1. DCGM / dcgm-exporter field set

Defaults: "D" = enabled in `default-counters.csv`; "P" = enabled in `dcp-metrics-included.csv` (the Helm and GPU Operator default); "off" = commented out in both, so it needs a custom collectors CSV.

Common "when absent" ids:
- `dcgm-exporter-missing`: install it through the GPU Operator or the Helm chart.
- `dcgm-profiling-off`: switch to the dcp CSV. Profiling is unavailable on consumer or vGPU parts, and when Nsight or CUPTI holds the profiler.
- `dcgm-field-off-<group>`: add the field to the collectors CSV.

Labels for every row are the standard set in §1.12 unless a row says otherwise.

### 1.1 Utilisation (sampled, coarse)
| Name | Source | Type & unit | Granularity | Default | Why it matters | Used by view | When absent |
|---|---|---|---|---|---|---|---|
| DCGM_FI_DEV_GPU_UTIL (203) / `_GPU_UTIL_RATIO` (V) | dcgm-exporter | gauge, % legacy / ratio new | per GPU (not per MIG) | D | "Any kernel running?" Treat it as a time-active proxy, **not** compute saturation | Fleet, GPU detail, Run system/hardware | dcgm-exporter-missing |
| DCGM_FI_DEV_MEM_COPY_UTIL (204) / `_MEMORY_UTIL_RATIO` (V) | | gauge | per GPU | D | Memory-controller busy time | GPU detail | — |
| DCGM_FI_DEV_ENC_UTIL (206) / DEC_UTIL (207) (V) | | gauge | per GPU | D | Video encode/decode load | GPU detail | — |
| DCGM_FI_DEV_GPU_PSTATE (190) (V) | | gauge 0–15 | per GPU | off | Shows a GPU stuck in a low P-state | GPU detail | dcgm-field-off-clocks |

### 1.2 Profiling (DCP; these are the "true" utilisation)
| Name | Type & unit | Granularity | Default | Why it matters | View | Absent |
|---|---|---|---|---|---|---|
| PROF_GR_ENGINE_ACTIVE (1001) / `_GR_ENGINE_UTIL_RATIO` (V) | gauge ratio | per GPU, per MIG GI | P | Engine active time; on MIG, the right replacement for GPU_UTIL | Fleet, Run system/hardware | dcgm-profiling-off |
| PROF_SM_ACTIVE (1002) / `_SM_UTIL_RATIO` (V) | gauge ratio | per GPU/GI | P (off in the new main CSV) | Share of cycles in which an SM has at least one warp | Run system/hardware, GPU detail | dcgm-profiling-off |
| PROF_SM_OCCUPANCY (1003) (V) | gauge ratio | per GPU/GI | off | Resident warps / max; latency hiding | Run profiles | — |
| PROF_PIPE_TENSOR_ACTIVE (1004) / `_TENSOR_UTIL_RATIO` (V) | gauge ratio | per GPU/GI | P | Tensor-core use; the best single proxy for training MFU | Run system/hardware, Recommendations | dcgm-profiling-off |
| PROF_DRAM_ACTIVE (1005) / `_DRAM_UTIL_RATIO` (V) | gauge ratio | per GPU/GI | P | HBM bandwidth bound (decode-heavy inference) | Inference service | — |
| PROF_PIPE_FP64/FP32/FP16_ACTIVE (1006–1008) (V) | gauge ratio | per GPU/GI | off | Precision mix; FP32 high while tensor is low means a missed AMP/BF16 opportunity | Recommendations | dcgm-field-off-prof |
| PROF_PCIE_TX/RX_BYTES (1009/1010) (V) | gauge B/s (headers included) | per GPU | P | Host↔device traffic: dataloader or offload bottleneck | Run system/hardware | — |
| PROF_NVLINK_TX/RX_BYTES (1011/1012) (V) | gauge B/s | per GPU | off | Collective traffic inside a node | Run system/hardware | dcgm-field-off-nvlink |
| PROF_PIPE_TENSOR_IMMA/HMMA/DFMA_ACTIVE (K) | gauge ratio | per GPU | off | INT8 vs FP16/BF16 vs FP64 tensor (quantised inference check) | Inference service | — |
| PROF_PIPE_INT_ACTIVE (K) | gauge ratio | per GPU | off | Integer pipe | Run profiles | — |
| PROF_NVDECn/NVJPGn/NVOFA0_ACTIVE (Hopper+) (K) | gauge ratio | per engine | off | Media/optical-flow engines | GPU detail | — |
| `*_CYCLES_ACTIVE_TOTAL` (SM, MMA, HMMA, IMMA, DMMA, DFMA, INT, FP16/32/64), `SM_CYCLES_ELAPSED_TOTAL`, `PCIE_*_BYTES_TOTAL` (V, exporter main) | counter cycles/bytes | per GPU | off | **Counters** let any window be computed exactly with rate(), which beats a sampled ratio | Run metrics | — |

Profiling note (K): only a few counter groups can be read at once on a given architecture, so asking for too many makes DCGM multiplex them (lower fidelity). Sampling is typically 1 Hz.

### 1.3 Memory
| Name | Type & unit | Gran. | Default | Why | View | Absent |
|---|---|---|---|---|---|---|
| FB_TOTAL/FREE/USED/RESERVED (250–253) / `_FB_CAPACITY/FREE/USED/RESERVED_BYTES` (V) | gauge MiB / bytes | per GPU, per MIG GI | D | OOM headroom, KV-cache sizing, memory leaked between jobs | GPU detail, Inference service | — |
| FB_USED_PERCENT (254) (K) | gauge | per GPU | off | | | — |
| BAR1_TOTAL/USED/FREE (90–92) (V) | gauge | per GPU | off | BAR1 exhaustion breaks GPUDirect RDMA / P2P | Node detail | dcgm-field-off-bar1 |

### 1.4 Power and energy
| Name | Type & unit | Default | Why | View | Absent |
|---|---|---|---|---|---|
| POWER_USAGE (155) / `_BOARD_POWER_WATTS` (V) | gauge W (averaged) | D | Load and cost | Fleet, Cost | — |
| POWER_USAGE_INSTANT (157) (K) | gauge W | off | Spikes that trip PSU/rack limits | GPU detail | — |
| TOTAL_ENERGY_CONSUMPTION (156) / `_GPU_ENERGY_JOULES_TOTAL` (V) | counter mJ legacy / J new; resets on driver reload | D | kWh per run / per token | Cost, Run overview | — |
| POWER_MGMT_LIMIT (160) / `_BOARD_POWER_LIMIT_REQUESTED_WATTS` (V) | gauge W | off | Configured cap | GPU detail | dcgm-field-off-power |
| ENFORCED_POWER_LIMIT (164) / `_BOARD_POWER_LIMIT_ENFORCED_WATTS` (V) | gauge W | off | Effective limit after all limiters | GPU detail | — |
| POWER_MGMT_LIMIT_MIN/MAX/DEF (K) | gauge W | off | Headroom denominator | GPU detail | — |

### 1.5 Thermals
| Name | Type & unit | Default | Why | View |
|---|---|---|---|---|
| GPU_TEMP (150) (V) | gauge °C | D | Hot GPU → thermal clock events | Fleet, GPU detail |
| MEMORY_TEMP (140) (V) | gauge °C (HBM) | D | HBM overheating; precedes ECC errors | GPU detail |
| `_GPU_TEMP_MARGIN_CELSIUS` (GPU_TEMP_LIMIT) (V) | gauge °C | off | Distance to slowdown | GPU detail |
| SLOWDOWN_TEMP (158), SHUTDOWN_TEMP (159), GPU/MEM_MAX_OP_TEMP (V) | gauge °C | off | Threshold bands on charts | GPU detail |
| FAN_SPEED (191) / `_FAN_SPEED_RATIO` (V) | gauge | off | Fan failure (air-cooled PCIe parts; SXM reads N/A) | Node detail |

### 1.6 Clocks
| Name | Type | Default | Why | View |
|---|---|---|---|---|
| SM_CLOCK (100) / `_SM_CLOCK_HERTZ` (V) | gauge MHz / Hz | D | Effective speed | GPU detail |
| MEM_CLOCK (101) (V) | gauge | D | | GPU detail |
| VIDEO_CLOCK (102), APP_SM_CLOCK (110), APP_MEM_CLOCK (111), MAX_SM_CLOCK (113), MAX_MEM_CLOCK (114), MAX_VIDEO_CLOCK (115) (V) | gauge | off | SM_CLOCK / MAX_SM_CLOCK is the clock ratio; a straggler GPU shows a lower one | GPU detail, Run system/hardware |

### 1.7 Clock-event (throttle) reasons and violation counters
`DCGM_FI_DEV_CLOCKS_EVENT_REASONS` (112) is a gauge bitmask, off by default. Bits (K; NVML `nvmlClocksEventReason*`):

| Bit | Name | Meaning | Severity |
|---|---|---|---|
| 0x1 | GpuIdle | No work; clocks dropped on purpose | benign |
| 0x2 | ApplicationsClocksSetting | App clocks set below max | config |
| 0x4 | SwPowerCap | Power above the software power limit | perf |
| 0x8 | HwSlowdown | HW slowdown (thermal or power brake) | **bad** |
| 0x10 | SyncBoost | Held back to match a sync-boost group | perf |
| 0x20 | SwThermalSlowdown | Driver thermal slowdown | perf/bad |
| 0x40 | HwThermalSlowdown | HW thermal slowdown | **bad** (cooling) |
| 0x80 | HwPowerBrakeSlowdown | External power-brake signal (PSU/rack) | **bad** (facility) |
| 0x100 | DisplayClockSetting | Display clock | benign |

Violation counters (fields 240–245, µs; new names `*_THROTTLE_SECONDS_TOTAL`, all off; names V, field IDs K):
- POWER_VIOLATION
- THERMAL_VIOLATION
- SYNC_BOOST_VIOLATION
- BOARD_LIMIT_VIOLATION
- LOW_UTIL_VIOLATION
- RELIABILITY_VIOLATION

They count time spent below application clocks. Exporter main also adds `_GPU_APP_THROTTLE_SECONDS_TOTAL` (all limiters) and `_GPU_BASE_THROTTLE_SECONDS_TOTAL` (below base clock), plus `DCGM_EXP_CLOCK_EVENTS_COUNT` (gauge, events in the window) and `_TOTAL` (counter, edge-counted) (V). Views: GPU detail, Run system/hardware, Recommendations. Id: `gpu-throttle-telemetry-off`.

### 1.8 Reliability: ECC, retired pages, row remapping, Xid
| Name | Type & unit | Default | Why | View | Absent |
|---|---|---|---|---|---|
| ECC_SBE_VOL_TOTAL (310), ECC_DBE_VOL_TOTAL (311), ECC_SBE_AGG_TOTAL (312), ECC_DBE_AGG_TOTAL (313) (V) | counter; VOL resets on driver reload, AGG is lifetime (InfoROM) | off | A DBE is uncorrectable, so the job dies; an SBE rate is a leading indicator | GPU detail, Fleet | gpu-ecc-telemetry-off |
| Per-location ECC (L1, L2, DEV, REG, TEX, SRM, SHM, CBU; VOL and AGG) (K) | counter | off | Shows where errors occur (SRAM vs HBM) | GPU detail | — |
| RETIRED_SBE (390), RETIRED_DBE (391), RETIRED_PENDING (392) / `_PAGE_RETIRED_*` (V) | counter / gauge 0-1 | off | **Pre-Ampere** page retirement; pending=1 means it needs a reset | GPU detail | — |
| UNCORRECTABLE_REMAPPED_ROWS (393), CORRECTABLE_REMAPPED_ROWS (394), ROW_REMAP_FAILURE (395), ROW_REMAP_PENDING (396); new names `ROW_REMAP_*_TOTAL`, `_FAILED` (V) | counter / gauge | P | **Ampere+** replaces page retirement. Pending → reset; failure → RMA | Fleet, GPU detail | gpu-rowremap-telemetry-off |
| XID_ERRORS (230) (V) | gauge, **value of the LAST Xid** (not a count) | D | See §2; use `DCGM_EXP_XID_ERRORS_TOTAL`/`_COUNT` (V) or kernel-log events for counts | Fleet, Node detail, Run events | xid-count-off |

### 1.9 PCIe
| Name | Type | Default | Why | View |
|---|---|---|---|---|
| PCIE_REPLAY_COUNTER (202) / `_PCIE_REPLAY_TOTAL` (V) | counter | D | Link signal integrity (riser, slot) | GPU detail |
| PCIE_LINK_GEN, PCIE_LINK_WIDTH (V); MAX_LINK_GEN/WIDTH (K) | gauge | off | **Link downtrained** (x8 instead of x16, Gen3 instead of Gen5) quietly halves H2D bandwidth | Node detail, Recommendations (`pcie-downtrained`) |
| `_PCIE_CORRECTABLE_ERROR_TOTAL` (V); PCIe AER uncorrectable (host dmesg) (K) | counter | off | | GPU detail |

### 1.10 NVLink, NVSwitch and fabric
| Name | Type | Default | Why | View |
|---|---|---|---|---|
| NVLINK_BANDWIDTH_TOTAL (449) / `_NVLINK_THROUGHPUT_BYTES_PER_SECOND`, plus per link L0–L17 (V) | gauge B/s | D (total only) | Intra-node collective traffic | Run system/hardware |
| Pre-Hopper: CRC_FLIT, CRC_DATA, REPLAY, RECOVERY error totals, plus per link (V) | counter | off | Link degradation, which slows NCCL | GPU detail |
| Hopper: `_NVLINK_CRC_ERROR_TOTAL`, `_RECOVERY_TOTAL`, `_REPLAY_TOTAL` (V) | counter | off | | GPU detail |
| Blackwell: `_NVLINK_RECOVERY_SUCCESSFUL/FAILED/EVENT_TOTAL`, `_NVLINK_PPCNT_RECOVERY_LAST_GAP_SECONDS` (V) | counter/gauge | off | Recovery storms | GPU detail |
| NVLINK_LINK_STATUS (K) / `DCGM_EXP_P2P_STATUS` (V) | gauge per link | off | **NVLink down/inactive**: traffic falls back to PCIe | Node detail (`nvlink-inactive`) |
| `_C2C_MAX_BANDWIDTH_BYTES_PER_SECOND` (V), C2C link status (K) | gauge | off | GH200/GB200 | Node detail |
| `_FABRIC_MANAGER_STATUS`, `_FABRIC_HEALTH_SUMMARY`, `DCGM_EXP_SYSTEM_HEALTH_STATUS` (IMEX) (V) | gauge enum | off | Fabric Manager down means NVSwitch systems cannot run CUDA | Node detail (`fabricmanager-down`) |
| NVSWITCH_* fields: throughput TX/RX, fatal/non-fatal errors, temperature, power, link status, per-lane CRC/ECC (K) | gauge/counter, entity = switch | off | HGX baseboard health; SXid errors | Node detail |

### 1.11 Device info, processes, vGPU, health
- **Inventory/labels** (K): NAME, SERIAL, UUID, VBIOS_VERSION, DRIVER_VERSION, CUDA_DRIVER_VERSION, PERSISTENCE_MODE, COMPUTE_MODE, MIG_MODE, BRAND, MINOR_NUMBER, PCI_BUSID, InfoROM versions, CPU/NUMA affinity. They feed the Node detail inventory and the `driver-skew` recommendation (mixed drivers across one job's nodes).
- **vGPU**: `DCGM_FI_DEV_VGPU_LICENSE_STATUS` is D (V); `_VGPU_FB_USED_BYTES` (V); per-vGPU utilisation (K).
- **Processes** (K): DCGM job stats (`dcgmi stats`) give per-PID energy, utilisation, memory and Xids. dcgm-exporter does not export PIDs; pod attribution comes from the kubelet PodResources API.
- **Health watches** (`dcgmi health -s`): PCIe, memory, InfoROM, thermal/power, NVLink, NVSwitch, driver, CPU (Grace) (K). The exporter surfaces them as `DCGM_EXP_GPU_HEALTH_STATUS` (V); the 0 pass / 10 warn / 20 fail values are (K).
- **Field policies**: `DCGM_EXP_FIELD_POLICY_*` (V).

### 1.12 Exporter labels (K unless marked)
- **Always**: `gpu` (index), `UUID`, `device` (nvidiaN), `modelName`, `Hostname`, `pci_bus_id`, and `DCGM_FI_DRIVER_VERSION` on newer versions.
- **With `KUBERNETES=true`** (PodResources socket): `pod`, `namespace`, `container`. Newer versions add opt-in pod labels and UID.
- **MIG**: `GPU_I_PROFILE` (e.g. `1g.10gb`) and `GPU_I_ID`. DCP fields are per GI; DEV fields such as GPU_UTIL belong to the parent GPU.
- **Xid series**: newer versions add `err_code`/`err_msg`.

### 1.13 DCGM diagnostics (on demand) (K)
`dcgmi diag -r` levels:
- **r1** (seconds): software and deployment checks.
- **r2** (~2 min): adds PCIe and memory checks plus a short stress.
- **r3** (~15–30 min): targeted power, targeted stress, memory bandwidth.
- **r4** (~1–2 h): adds memtest and pulse test.

EUD (Extended Utility Diagnostics) is a separate NVIDIA package for deeper, RMA-grade tests.

Product use:
- Run r1 as a node-admission check.
- Run r3 after a reset or on an RMA candidate, before the node rejoins the pool.
- Store each result as the event `gpu.diag.result{level,test,result}` (pass/warn/fail per GPU). Views: Node detail, Fleet. Id: `gpu-diag-never-run`.
- The GPU Operator can run diag as a Job; some clouds run it at node start.

## 2. NVIDIA Xid codes that matter

NVIDIA now publishes the authoritative catalog as a spreadsheet (V). It has tenant in-operation and investigatory actions per code, a decode sheet for 144–150 (IntrInfo bitmask and Error Status, V1/V2 encoding by driver), and a parsing policy that resolves combinations of Xids by priority. Where the guidance points to Xid 154, follow its recovery advice.

Product rule: ingest Xid **events** from kernel logs (`NVRM: Xid (PCI:...): <code>, pid=..., name=..., <msg>`), each carrying GPU, PCI address, PID and process. Do not rely only on DCGM's last-value gauge.

The 79 example and the 154 rule are (V); the rest of the table is (K). Verify every row against the spreadsheet before shipping the ruleset.

| Xid | Meaning | Severity | Action |
|---|---|---|---|
| 13 | Graphics engine exception (illegal address/instruction) | app | App bug; flag the GPU only if it repeats across apps |
| 31 | GPU memory page fault (MMU) | app | App bug (or driver) |
| 32 | Invalid or corrupted push buffer (PCIe/DMA) | hw-suspect | Check PCIe replays; drain if repeated |
| 43 | GPU stopped processing (channel killed) | app | Ignore; the GPU stays usable |
| 45 | Preemptive cleanup after another error or a kill | info | Correlate with the root Xid |
| 48 | Double-bit ECC error | hw | Restart app; reset GPU (remap); RMA if it recurs |
| 61 | Internal microcontroller breakpoint/warning | fw | Reset GPU; report if repeated |
| 62 | Internal microcontroller halt | fw/hw | Reset GPU; drain |
| 63 | ECC page retirement / row remap recorded | hw-info | Reset when convenient; watch the rate |
| 64 | ECC page retirement / row remap failure | hw | Drain; RMA |
| 68 | NVDEC/video processor exception | app/hw | Restart app; reset if repeated |
| 69 | Graphics engine class error | app | App bug |
| 74 | NVLink error | hw | Reset; drain if it recurs; check NVSwitch/cabling |
| 79 | GPU has fallen off the bus (V) | critical | Drain; power-cycle; RMA if repeated |
| 92 | High single-bit ECC rate | warn | Monitor; schedule diag |
| 94 | Contained ECC error (only affected processes killed) | hw | Restart app; reset at the next opportunity |
| 95 | Uncontained ECC error | hw | Reset now; drain |
| 109 | Context switch timeout | app/hw | Restart app; reset if repeated |
| 119 | GSP RPC timeout | fw | Reset GPU, often needs a reboot; drain if repeated |
| 120 | GSP error | fw | Same as 119 |
| 121 | C2C link error (Grace-Hopper) | hw | Reset; drain |
| 136 | Link/fabric training failure (K, unsure) | hw | Reset; check the fabric |
| 140 | Unrecovered ECC in on-chip memory | hw | Reset; RMA if repeated |
| 143 | GPU init / fabric failure (K, unsure) | hw | Reset; drain |
| 144–150 | NVLink5/NVSwitch error classes; severity comes from the decode sheet | varies | At least a reset; drain or RMA on fatal ones |
| 154 | Recovery-action advisory (none / reset / reboot / drain) (V) | meta | Follow the advised action |

Recommendation ids:
- `xid-app-fault`: 13, 31, 43, 69.
- `xid-reset-required`: 48, 62, 94, 95, 119, 120, and 154 advising a reset.
- `xid-drain-node`: 79, 64, repeated 95, fatal 74 or 144–150.
- `xid-rma-candidate`: repeated 48/64/79, or a row-remap failure.

## 3. node-problem-detector (NPD)

### 3.1 Default monitors, conditions and reasons
| Monitor | Condition (permanent) | Event reasons (temporary) |
|---|---|---|
| kernel-monitor.json (V) | **KernelDeadlock** (DockerHung / KernelHasNoDeadlock); **XfsShutdown**; **CperHardwareErrorFatal** | OOMKilling, TaskHung, UnregisterNetDevice, KernelOops, Ext4Error, Ext4Warning, IOError, MemoryReadError, CperHardwareErrorCorrected, CperHardwareErrorRecoverable |
| readonly-monitor.json (K) | **ReadonlyFilesystem** (FilesystemIsReadOnly) | — |
| docker-monitor.json (K) | **CorruptDockerOverlay2** | CorruptDockerImage |
| abrt-adaptor.json (K) | — | CCPPCrash, UncaughtException, XorgCrash, KernelOops |
| systemd-monitor.json (K) | — | KubeletStart, DockerStart, ContainerdStart |
| health-checker-kubelet/docker/containerd (custom plugin) (K) | **KubeletUnhealthy**, **ContainerRuntimeUnhealthy** | — |
| kernel/systemd log-counter plugins (K) | **FrequentUnregisterNetDevice**, **FrequentKubeletRestart**, **FrequentDockerRestart**, **FrequentContainerdRestart** | — |
| network-problem-monitor (K) | — | **ConntrackFull** |
| iptables-mode-monitor (K) | — | IPTablesVersionsMismatch |
| NTP / timesyncd monitor (K) | **NTPProblem** | — |

NPD's own metrics (K):
- `problem_counter{reason}`: counter, on the Prometheus exporter (default :20257).
- `problem_gauge{type,reason}`: gauge, 1 = condition true.
- `system-stats-monitor` host stats (disk IO, CPU load, memory, uptime) are off by default.

Conditions also land on `Node.status.conditions` and as Events. Views: Node detail, Fleet, Run events. Id: `npd-missing` (install NPD; GKE/AKS ship it; EKS has its node monitoring agent).

### 3.2 GPU and fabric custom checks deployed in practice (K)
| Check | Who | Signal |
|---|---|---|
| NVRM Xid log rule (regex → `NvidiaXid<code>`) | common NPD custom config | event or permanent `GPUProblem` |
| GKE GPU health and auto-repair | GKE | node conditions |
| EKS node monitoring agent: **AcceleratedHardwareReady** (NVIDIA Xid/DCGM, Neuron), StorageReady, NetworkingReady, KernelReady, ContainerRuntimeReady | EKS | conditions + node auto-repair |
| AKS GPU health / Xid detection, node auto-repair | AKS | conditions/events |
| GPU Operator validators (driver, toolkit, cuda, plugin), DCGM health | NVIDIA | validator pod status, `nvidia.com/gpu.deploy.*` |
| NVIDIA NVSentinel-style health monitor: health event → cordon/drain/remediate | NVIDIA | node conditions |
| CoreWeave/Lambda-style suites: IB port not ACTIVE, PCIe AER uncorrectable, Fabric Manager down, NVLink inactive, GPU count < expected, `nvidia-smi` hang, persistence off, power-cap mismatch, row-remap pending, diag r1 fail, HCA firmware skew | providers | e.g. `GpuCountMismatch`, `IBLinkDown`, `NvidiaSmiHang` |

Normalise all of these into `node.condition{type,status,reason,source}` plus `node.problem.event{reason,source}`, keyed to the GPU where possible. Ids: `gpu-count-mismatch`, `ib-link-down`, `nvidia-smi-hang`, `fabricmanager-down`, `nvlink-inactive`, `pcie-aer-uncorrectable`.

## 4. Node, kubelet, CRI and host (K)

### 4.1 node-exporter essentials
| Name | Type | Why | View |
|---|---|---|---|
| node_cpu_seconds_total{mode} | counter | Dataloader CPU saturation; iowait | Node detail, Run system/hardware |
| node_pressure_{cpu,memory,io}_{waiting,stalled}_seconds_total (PSI) | counter | Stall time, which says more than utilisation | Node detail |
| node_memory_MemAvailable_bytes, HugePages_*, node_vmstat_oom_kill | gauge/counter | Host OOM (pinned memory) | Node detail |
| node_disk_{read,written}_bytes_total, node_disk_io_time_seconds_total | counter | Checkpoint and dataset IO | Run checkpoints |
| node_filesystem_avail_bytes (root, /var/lib/containerd image fs, NVMe scratch) | gauge | DiskPressure, failed image pulls | Node detail, Pod start |
| node_network_{receive,transmit}_{bytes,errs,drop}_total | counter | Storage/data traffic over Ethernet | Node detail |
| node_infiniband_*: port_data_{transmitted,received}_bytes_total, link_downed_total, link_error_recovery_total, symbol_error_total, port_receive_errors_total, port_transmit_wait_total, excessive_buffer_overrun_errors_total, local_link_integrity_errors_total, state_id, physical_state_id, rate_bytes_per_second | counter/gauge | Inter-node fabric: NCCL slowdowns, link flaps, congestion (xmit_wait) | Node detail, Run system/hardware |
| RoCE/RDMA via network-operator / DOCA Telemetry Service: port_xmit_data, port_rcv_data, port_rcv_errors, link_downed, symbol_error, out_of_buffer, np_cnp_sent, rp_cnp_handled, PFC pause | counter | RoCE congestion control | Node detail |
| node_edac_* (host ECC), node_hwmon_temp_celsius, node_timex_offset_seconds | | Host RAM ECC; clock skew breaks traces and NCCL timeouts | Node detail |

### 4.2 kubelet, cAdvisor and CRI
| Name | Type | Why | View |
|---|---|---|---|
| kubelet_pod_start_sli_duration_seconds (excludes image pull) | histogram | Pod start SLI | Pod start |
| kubelet_pod_start_duration_seconds, kubelet_pod_start_total_duration_seconds (includes pull, newer) | histogram | End-to-end start including a 20 GB image | Pod start |
| kubelet_image_pull_duration_seconds{image_size_in_bytes} (~1.30+) | histogram | Pull cost by size bucket | Pod start (`image-pull-slow`) |
| kubelet_pod_worker_duration_seconds{operation} | histogram | Sync latency | Pod start |
| kubelet_runtime_operations_duration_seconds / _total / _errors_total{operation_type} | histogram/counter | CRI latency and errors | Node detail |
| kubelet_evictions{eviction_signal}, kubelet_preemptions | counter | Evictions under pressure | Node detail, Run events |
| kubelet_started_pods_errors_total, kubelet_started_containers_errors_total{code}, kubelet_running_pods | | | Pod start |
| cAdvisor: container_memory_working_set_bytes, container_oom_events_total, container_cpu_cfs_throttled_periods_total / _periods_total, container_cpu_usage_seconds_total, container_fs_*, container_network_* | | Pod OOM (not GPU OOM), dataloader CPU throttling | Run system/hardware |
| containerd: gRPC request durations, CRI image pull bytes/durations (2.x), snapshotter/unpack metrics | | Pull throughput, slow unpack | Pod start |

**Kubelet tracing** (KubeletTracing GA in 1.34 per the briefing; not verified):
- **Spans**: OTLP spans around the CRI calls (RunPodSandbox, CreateContainer, StartContainer, PullImage …) and the kubelet's syncPod work.
- **Propagation**: W3C trace context into containerd, which emits its own spans.
- **Use**: a schedule → sandbox → pull → start → ready waterfall in the Pod start view.
- **Id**: `kubelet-tracing-off`; fix by setting the `tracing:` endpoint and sampling rate in the KubeletConfiguration.

**Node conditions**: Ready, MemoryPressure, DiskPressure, PIDPressure, NetworkUnavailable, plus the NPD and cloud conditions in §3.

**Device plugin**: compare `capacity["nvidia.com/gpu"]` with `allocatable`; less allocatable means the plugin marked a GPU unhealthy (its Xid health check). MIG appears as `nvidia.com/mig-<profile>` resources; time-slicing advertises replicas. Allocated vs idle comes from PodResources. Id: `gpu-allocatable-lt-capacity`.

### 4.3 GPU Feature Discovery labels
- **GPU**: `nvidia.com/gpu.product`, `.memory` (MiB), `.count`, `.family`, `.machine`, `.replicas`, `.sharing-strategy`, `nvidia.com/gpu.compute.major/minor`, `nvidia.com/gpu.present`, `nvidia.com/gpu.clique` (NVLink domain, GB200).
- **CUDA**: `nvidia.com/cuda.driver.major/minor/rev` (newer `cuda.driver-version.*`) and `nvidia.com/cuda.runtime.major/minor` (newer `cuda.runtime-version.*`).
- **MIG**: `nvidia.com/mig.capable`, `mig.strategy` (none/single/mixed), `mig.config`, `mig.config.state` (pending/rebooting/success/failed; written by mig-manager), and per-profile `nvidia.com/mig-<profile>.count/.memory/.multiprocessors/.engines.*`.
- **Other**: `nvidia.com/gfd.timestamp`, `nvidia.com/gpu.deploy.*`, `nvidia.com/vgpu.present`, `nvidia.com/mps.capable`, `feature.node.kubernetes.io/pci-10de.present`.

**DRA ResourceSlice** (NVIDIA DRA driver `gpu.nvidia.com`):
- Device attributes: `uuid`, `productName`, `architecture`, `brand`, `driverVersion`, `cudaDriverVersion`, `cudaComputeCapability`, `index`, `minor`, `type` (gpu/mig), `pcieBusID`, `pcieRoot`.
- For MIG devices: `parentUUID`, `profile`, and capacity (memory, multiprocessors).
- ComputeDomain resources for IMEX.

Where DRA is in use, the Fleet inventory counts devices from these slices instead of from device-plugin capacity.

## 5. Other vendors → neutral fields (K)
| Neutral field | NVIDIA DCGM | AMD device-metrics-exporter | Intel Gaudi exporter | AWS Neuron (neuron-monitor) | Google TPU |
|---|---|---|---|---|---|
| accel.util.time_active | GPU_UTIL | gpu_gfx_activity | habanalabs_utilization | neuroncore_utilization_ratio | duty_cycle |
| accel.util.matrix_active | PROF_PIPE_TENSOR_ACTIVE | profiler MFMA counters | — | — | tensorcore_utilization |
| accel.mem.used/total | FB_USED/TOTAL | gpu_used_vram / gpu_total_vram | habanalabs_memory_used_bytes | neuron_runtime_memory_used_bytes | memory_used / memory_total |
| accel.mem.bw_active | PROF_DRAM_ACTIVE | gpu_umc_activity | — | — | memory_bandwidth_utilization |
| accel.power / energy | POWER_USAGE / TOTAL_ENERGY | gpu_power_usage / gpu_energy_consumed | habanalabs_power_mW | not exposed | — |
| accel.temp.core/mem | GPU_TEMP / MEMORY_TEMP | gpu_junction_temperature / gpu_memory_temperature | habanalabs_temperature_onchip | — | — |
| accel.ecc.corr/uncorr | ECC_SBE/DBE | gpu_ecc_correct/uncorrect_total (per block) | habanalabs ECC/PCIe errors | hardware ECC events | — |
| accel.link.bw/errors | NVLINK_* | gpu_xgmi_link_* | habanalabs_nic_port_status | collectives stats | ICI link metrics |
| accel.fatal_event | Xid | RAS events / health | habanalabs_device_health | hardware errors | node health condition |

## Derived metrics
- **True utilisation**: `PROF_SM_ACTIVE` (GR_ENGINE_ACTIVE on MIG).
  - Efficiency proxy: `PIPE_TENSOR_ACTIVE`.
  - MFU proxy: `tensor_active × sm_clock/max_sm_clock`.
  - `GPU_UTIL > 0.9 AND SM_ACTIVE < 0.3` raises `gpu-util-misleading` (launch-bound work or tiny kernels).
- **Power headroom** = `1 − POWER_USAGE/ENFORCED_POWER_LIMIT`. At or below 0.02 sustained, together with the SwPowerCap bit, the GPU is power-bound.
- **% time throttled by reason** = `rate(<REASON>_VIOLATION)/1e6` (µs per second becomes a fraction). With the new names it is `rate(*_THROTTLE_SECONDS_TOTAL)`. With only the bitmask: `avg_over_time(((reasons & bit) > 0)[w])`.
- **ECC rate**:
  - `rate(ECC_SBE_VOL_TOTAL[1h])`.
  - Any DBE increase is an event.
  - `increase(CORRECTABLE_REMAPPED_ROWS[7d])`.
- **NVLink error rate** = `Σ rate(CRC_FLIT + CRC_DATA + REPLAY + RECOVERY)` ÷ NVLink bytes (errors per TB), plus the share of links down.
- **Straggler** = `SM_CLOCK / median(SM_CLOCK over the run's GPUs) < 0.9`.
- **Idle-allocated GPU-hours** = Σ intervals where the GPU is allocated to a pod and `SM_ACTIVE < 0.05` (fallback GPU_UTIL < 5%). Show it by namespace in Cost and Recommendations (`idle-allocated-gpu`).
- **Energy per run / token** = Δ TOTAL_ENERGY over the run's GPUs and window ÷ tokens.
- **Bad-GPU score** (0–100, higher is worse). The proposed policy is drain at 60 or more and RMA candidate at 85 or more. The weights are a starting point to tune:

| Input (7-day window unless noted) | Points |
|---|---|
| Xid 79/64/95, fatal 144–150, or a row-remap failure | 60 each (total capped at 100) |
| Xid 48/94/62/119/120 | 15 each, cap 45 |
| Row-remap pending or retired-page pending | 25 |
| Uncorrectable remapped rows increased | 10 per row, cap 30 |
| SBE rate > 10/h (24 h) | 10 |
| NVLink error rate above the fleet p99, or a link down | 15 |
| PCIe downtrained, or replay rate above the fleet p99 | 10 |
| HW slowdown / HW thermal / power-brake time > 1% | 10 |
| Clock straggler in at least 2 runs | 10 |
| DCGM diag r3 fail (warn) | 40 (15) |
| Device plugin marked the GPU unhealthy | 30 |

## Gotchas
- **XID_ERRORS is last-value.** It keeps the last code until the next Xid, so never alert on `>0`; count with `DCGM_EXP_XID_ERRORS_TOTAL` or log events. A scrape can also miss a burst between samples.
- **GPU_UTIL measures time active**, not how hard the GPU works. One tiny kernel in a loop reads 100%.
- **MIG**: DEV_* fields (utilisation, power, temperature, ECC) belong to the parent GPU and repeat on every GI's series, so never sum them across GIs. DCP fields are per GI.
- **Time-slicing and MPS**: several pods share one GPU series, so attribution is ambiguous. Label it "shared".
- **Pod attribution needs `KUBERNETES=true` plus the PodResources mount.** A finished pod's series disappear, so join on time ranges.
- **Counter resets**: energy and VOL ECC counters reset on a driver reload or GPU reset. AGG counters persist.
- **Units differ between legacy and new names.** Normalise them at ingest.
- **Profiling contention**: Nsight or CUPTI in a pod blanks the DCP fields, which can look like an idle GPU.
- **Cardinality**: per-link NVLink series (18 links × 8 GPUs) and per-location ECC multiply series. Keep totals by default and fetch detail on demand.
- **NPD regexes drift** with driver and kernel message formats.
- **Clock skew** between nodes breaks cross-node trace ordering. Watch `node_timex_offset_seconds`.

## Sources
- https://raw.githubusercontent.com/NVIDIA/dcgm-exporter/main/etc/default-counters.csv (V)
- https://raw.githubusercontent.com/NVIDIA/dcgm-exporter/main/etc/dcp-metrics-included.csv (V)
- https://docs.nvidia.com/deploy/xid-errors/analyzing-xid-catalog.html (V)
- https://raw.githubusercontent.com/kubernetes/node-problem-detector/master/config/kernel-monitor.json (V)
- Not fetched this session (K): the DCGM field-ID docs, the rest of NPD's config directory, kubelet metric docs, the GFD README, the NVIDIA DRA driver, other vendors' exporters.

<!-- prettier-ignore-end -->
