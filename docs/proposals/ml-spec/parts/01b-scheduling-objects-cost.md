# 01b — Scheduling, pod start, orchestrator objects and cost

Scope: everything between "a user submitted a run" and "the GPUs are doing useful work". That covers how a run's state, attempts and per-replica status come out of orchestrator CRs, and how money and goodput are counted.
Confidence: (V) = checked against upstream source this session (URLs in Sources). (K) = from knowledge, not re-checked.

The metric tables use these columns: Name | Source | Type & unit | Granularity / labels | Default on? | Why it matters | Used by view | When absent.

---

## 1. Kueue

**Where the metrics come from.** The kueue-controller-manager serves them on `/metrics` under the subsystem `kueue_`, so every name below carries the `kueue_` prefix (V). They are emitted by default, but the Helm chart's `enablePrometheus` defaults to false (K). So the **scrape** is often missing even when Kueue runs.

**Gated metrics.** The `local_queue_*` metrics need the `LocalQueueMetrics` feature gate, which is alpha and off by default (V for names and labels; K for the gate default).

**Common labels.** Almost every series has a `replica_role` label: leader or follower when Kueue runs in HA (V). It may also carry custom labels taken from CQ/LQ/Workload labels set in the config (`metrics.customLabels`, via `cl.LabelNames`) (V).

**API version.** The storage version on main is `kueue.x-k8s.io/v1beta2`. v1beta1 is still served (K).

**Detecting a missing scrape.** If Kueue's CRDs (`clusterqueues.kueue.x-k8s.io`) exist but there is no `kueue_build_info` series, raise `kueue-metrics-not-scraped`. The fix: set `enablePrometheus`, or add an OTel scrape of the controller metrics service (port 8443).

### 1.1 Admission and scheduling-cycle metrics

| Name                                                                                | Source                 | Type & unit     | Granularity / labels                              | Default on?                  | Why it matters                                                                          | Used by view            | When absent                  |
| ----------------------------------------------------------------------------------- | ---------------------- | --------------- | ------------------------------------------------- | ---------------------------- | --------------------------------------------------------------------------------------- | ----------------------- | ---------------------------- |
| `admission_attempts_total`                                                          | kueue controller (V)   | counter         | `result` (success / inadmissible)                 | yes                          | How often the scheduler cycles. A high inadmissible share means quota is starved        | Queues                  | `kueue-metrics-not-scraped`  |
| `admission_attempt_duration_seconds`                                                | (V)                    | histogram, s    | `result`                                          | yes                          | Kueue scheduler latency                                                                 | Queues                  | same                         |
| `admission_cycle_preemption_skips`                                                  | (V)                    | gauge           | `cluster_queue`                                   | yes                          | Workloads skipped in a cycle because their preemption targets overlap                   | Queues                  | same                         |
| `preemption_target_recomputations_total`                                            | (V, recent)            | counter         | `cluster_queue`, `result`                         | yes                          | Preemption churn                                                                        | Queues                  | same                         |
| `pending_workloads`                                                                 | (V)                    | gauge           | `cluster_queue`, `status`=`active`/`inadmissible` | yes                          | Queue depth. Inadmissible means it can never fit as configured                          | Queues, Run overview    | same                         |
| `pending_scheduling_hashes`                                                         | (V, recent)            | gauge           | `cluster_queue`, `status`                         | yes                          | Number of distinct pending workload shapes                                              | Queues                  | same                         |
| `unadmitted_workloads`                                                              | (V, recent)            | gauge           | `cluster_queue`, `reason`, `underlying_cause`     | yes                          | Why workloads wait (the QuotaReserved reasons in 1.4)                                   | Queues, Recommendations | same                         |
| `quota_reserved_workloads_total`                                                    | (V)                    | counter         | `cluster_queue`, `priority_class`                 | yes                          | Quota reservations (stage 1 of admission)                                               | Queues                  | same                         |
| `quota_reserved_wait_time_seconds`                                                  | (V)                    | histogram, s    | `cluster_queue`, `priority_class`                 | yes                          | Created → QuotaReserved: the **queue wait**                                             | Queues, Pod start       | same                         |
| `admitted_workloads_total`                                                          | (V)                    | counter         | `cluster_queue`, `priority_class`                 | yes                          | Admissions                                                                              | Queues                  | same                         |
| `admission_wait_time_seconds`                                                       | (V)                    | histogram, s    | `cluster_queue`, `priority_class`                 | yes                          | Created → Admitted, admission checks included                                           | Queues, Pod start       | same                         |
| `admission_checks_wait_time_seconds`                                                | (V)                    | histogram, s    | same                                              | yes                          | QuotaReserved → Admitted: time in AdmissionChecks (ProvisioningRequest/DWS, MultiKueue) | Queues, Pod start       | same                         |
| `ready_wait_time_seconds`                                                           | (V)                    | histogram, s    | same                                              | needs `waitForPodsReady` (K) | Created → PodsReady: end to end, "time until every pod runs"                            | Pod start               | `kueue-waitforpodsready-off` |
| `admitted_until_ready_wait_time_seconds`                                            | (V)                    | histogram, s    | same                                              | needs `waitForPodsReady`     | Admitted → PodsReady: pod scheduling, image pull and init                               | Pod start               | same                         |
| `workload_creation_latency_seconds`                                                 | (V, recent)            | histogram, s    | `job_kind`                                        | yes                          | Job created → Workload created (Kueue's own lag)                                        | Pod start               | —                            |
| `pod_scheduling_gate_removal_seconds`                                               | (V, recent)            | histogram, s    | `name`, `cluster_queue`, `is_group`               | yes                          | Admission → scheduling gates removed (TAS, pod groups)                                  | Pod start               | —                            |
| `workload_recovery_wait_time_seconds`                                               | (V, recent)            | histogram, s    | `cluster_queue`, `priority_class`                 | yes                          | Evicted → admitted again (recovery time)                                                | Run events, Queues      | —                            |
| `execution_time_seconds`                                                            | (V, recent)            | histogram, s    | same                                              | yes                          | Admitted → Finished                                                                     | Queues, Cost            | —                            |
| `finished_workloads` / `finished_workloads_total`                                   | (V)                    | gauge / counter | `cluster_queue` (+`priority_class`)               | yes                          | Finished workloads                                                                      | Queues                  | —                            |
| `workloads_dispatched_total`, `workloads_admitted_total`, `workloads_evicted_total` | MultiKueue (V, recent) | counter         | `cluster_queue`, `cluster` (+`reason`)            | only with MultiKueue         | Dispatch per worker cluster                                                             | Queues                  | —                            |

### 1.2 Eviction and preemption

| Name                                  | Source      | Type & unit  | Labels                                                                                                                                                                            | Default on?      | Why it matters                                                                 | Used by view             | When absent |
| ------------------------------------- | ----------- | ------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------- | ------------------------------------------------------------------------------ | ------------------------ | ----------- |
| `evicted_workloads_total`             | (V)         | counter      | `cluster_queue`, `reason` (Preempted, PodsReadyTimeout, AdmissionCheck, ClusterQueueStopped, LocalQueueStopped, NodeFailures, Deactivated…), `underlying_cause`, `priority_class` | yes              | Runs lost and GPU-hours wasted, by cause                                       | Queues, Run events, Cost | —           |
| `evicted_workloads_once_total`        | (V)         | counter      | same                                                                                                                                                                              | yes              | Distinct workloads evicted at least once, so repeat evictions don't inflate it | Queues                   | —           |
| `pods_ready_to_evicted_time_seconds`  | (V)         | histogram, s | `cluster_queue`, `reason`, `underlying_cause`                                                                                                                                     | yes              | How long a workload ran before eviction: an **upper bound on wasted progress** | Cost, Run events         | —           |
| `workload_eviction_latency_seconds`   | (V, recent) | histogram, s | `cluster_queue`, `reason`                                                                                                                                                         | yes              | Eviction decided → pods gone                                                   | Queues                   | —           |
| `preempted_workloads_total`           | (V)         | counter      | `preempting_cluster_queue`, `reason` (InClusterQueue, InCohortReclamation, InCohortFairSharing, InCohortReclaimWhileBorrowing)                                                    | yes              | Who preempts whom                                                              | Queues                   | —           |
| `partially_preempted_workloads_total` | (V, recent) | counter      | same                                                                                                                                                                              | yes              | Elastic or partial preemption                                                  | Queues                   | —           |
| `replaced_workload_slices_total`      | (V, recent) | counter      | `cluster_queue`                                                                                                                                                                   | ElasticJobs only | Elastic slice replacement                                                      | Queues                   | —           |

### 1.3 Quota, usage, fair share, cohorts, info

| Name                                                                                                                                                                                                                                                                           | Source      | Type & unit                  | Labels                                                       | Default on?                            | Why it matters                                               | Used by view            | When absent                    |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ----------- | ---------------------------- | ------------------------------------------------------------ | -------------------------------------- | ------------------------------------------------------------ | ----------------------- | ------------------------------ |
| `reserving_active_workloads`                                                                                                                                                                                                                                                   | (V)         | gauge                        | `cluster_queue`                                              | yes                                    | Workloads that hold quota                                    | Queues                  | —                              |
| `admitted_active_workloads`                                                                                                                                                                                                                                                    | (V)         | gauge                        | `cluster_queue`                                              | yes                                    | Workloads running                                            | Queues, Fleet           | —                              |
| `cluster_queue_resource_reservation`                                                                                                                                                                                                                                           | (V)         | gauge, resource units (GPUs) | `cohort`, `cluster_queue`, `flavor`, `resource`              | yes                                    | Quota reserved, including workloads not yet admitted         | Queues, Cost            | —                              |
| `cluster_queue_resource_usage`                                                                                                                                                                                                                                                 | (V)         | gauge                        | same                                                         | yes                                    | Quota used by admitted workloads                             | Queues, Cost            | —                              |
| `cluster_queue_resource_pending`                                                                                                                                                                                                                                               | (V, recent) | gauge                        | `cluster_queue`, `resource`                                  | yes                                    | GPUs requested but not granted: the capacity-planning number | Queues, Recommendations | —                              |
| `cluster_queue_nominal_quota`, `_borrowing_limit`, `_lending_limit`                                                                                                                                                                                                            | (V)         | gauge                        | `cohort`, `cluster_queue`, `flavor`, `resource`              | yes                                    | Guaranteed quota and the borrow/lend caps                    | Queues                  | —                              |
| `cluster_queue_weighted_share` / `cohort_weighted_share`                                                                                                                                                                                                                       | (V)         | gauge, dimensionless         | `cluster_queue`, `cohort`                                    | yes (meaningful only with fairSharing) | Where a queue or cohort stands in fair sharing               | Queues                  | —                              |
| `cohort_subtree_quota`, `cohort_subtree_resource_reservations`                                                                                                                                                                                                                 | (V)         | gauge                        | `cohort`, `flavor`, `resource`                               | yes                                    | Hierarchical cohort capacity against use                     | Queues                  | —                              |
| `cohort_subtree_admitted_workloads_total`, `cohort_subtree_admitted_active_workloads`                                                                                                                                                                                          | (V)         | counter/gauge                | `cohort`                                                     | yes                                    | Cohort throughput                                            | Queues                  | —                              |
| `cluster_queue_status`                                                                                                                                                                                                                                                         | (V)         | gauge 0/1                    | `cluster_queue`, `status` (pending/active/terminating)       | yes                                    | A stopped CQ queues everything silently                      | Queues, Recommendations | `kueue-cq-inactive`            |
| `cluster_queue_info`, `cohort_info`                                                                                                                                                                                                                                            | (V, recent) | info                         | `parent_cohort`, `root_cohort`, `dynamic_quota_orchestrator` | yes                                    | The queue/cohort tree                                        | Queues                  | —                              |
| `local_queue_*`: twins of the CQ metrics (pending, reserving/admitted active, quota_reserved/admitted/evicted/finished totals, wait histograms, resource usage/reservation, status, unadmitted, admission_fair_sharing_usage, execution_time, creation latency, recovery wait) | (V)         | as the CQ twins              | `name`, `namespace`                                          | **no**: `LocalQueueMetrics` gate       | Per-team (namespace) view without per-workload cardinality   | Queues                  | `kueue-localqueue-metrics-off` |
| `build_info`, `feature_enabled`                                                                                                                                                                                                                                                | (V)         | info                         | `git_version`…; `name`, `stage`                              | yes                                    | Version and gates, for detecting what this Kueue can do      | Fleet                   | —                              |

### 1.4 The Workload object (`workloads.kueue.x-k8s.io`): per-run truth

Histograms carry no run identity, so per-run timings must come from the Workload's conditions.

| Field / condition                                                                                                                                                                       | Meaning                                                  | Reasons (V unless noted)                                                                                                                                                                                                                                                                                                                                                    | View                 |
| --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------- |
| `QuotaReserved`                                                                                                                                                                         | Quota held in a CQ                                       | When False: `NoMatchingFlavor`, `WaitingForQuota`, `ExceedsMaxQuota`, `UnrecordableResourceUsage`, `TopologyPlacementFailed`, `WaitingForPreemptedWorkloads`, `Misconfigured`, `AssignmentError`, `Suspended`, `PendingEvaluation`, `WaitingForPodsReady`, `DRAResourcesUnresolved`, `SchedulingSimulationFailed`. `message` names the flavor or resource that is short (K) | Run overview, Queues |
| `Admitted`                                                                                                                                                                              | Every admission check is Ready, so pods may start        | When False: `NoReservation`, `UnsatisfiedAdmissionChecks`, `PendingDelayedTopologyRequests`                                                                                                                                                                                                                                                                                 | Pod start            |
| `PodsReady`                                                                                                                                                                             | Every pod is Ready (only with `waitForPodsReady`)        | Reasons such as `WaitForStart`, `WaitForRecovery` (K)                                                                                                                                                                                                                                                                                                                       | Pod start            |
| `Evicted`                                                                                                                                                                               | Lost its admission                                       | `Preempted`, `PodsReadyTimeout`, `AdmissionCheck`, `ClusterQueueStopped`, `LocalQueueStopped`, `NodeFailures`, `EvictedOnManagerCluster` (MultiKueue), `Deactivated`; `InactiveWorkload` is the old name (K)                                                                                                                                                                | Run events           |
| `Preempted`                                                                                                                                                                             | Why it was preempted                                     | `InClusterQueue`, `InCohortReclamation`, `InCohortFairSharing`, `InCohortReclaimWhileBorrowing` (K)                                                                                                                                                                                                                                                                         | Run events           |
| `Requeued`                                                                                                                                                                              | Back in the queue after an eviction                      | —                                                                                                                                                                                                                                                                                                                                                                           | Run events           |
| `DeactivationTarget`                                                                                                                                                                    | About to be deactivated (e.g. the requeue limit was hit) | —                                                                                                                                                                                                                                                                                                                                                                           | Run events           |
| `Finished`                                                                                                                                                                              | Done                                                     | `Succeeded`, `Failed`, `OutOfSync`, `OwnerNotFound`                                                                                                                                                                                                                                                                                                                         | Run overview         |
| `spec.active`, `spec.priority`, `spec.priorityClassName`, `spec.queueName`                                                                                                              | Deactivated flag, priority, LocalQueue                   | (K)                                                                                                                                                                                                                                                                                                                                                                         | Queues               |
| `spec.podSets[]{name,count,minCount,template,topologyRequest}`                                                                                                                          | The shape requested                                      | (K)                                                                                                                                                                                                                                                                                                                                                                         | Run overview         |
| `status.admission{clusterQueue, podSetAssignments[{name, flavors, resourceUsage, count, topologyAssignment{levels, domains}, delayedTopologyRequest}]}`                                 | What it was given, and where (TAS)                       | (K)                                                                                                                                                                                                                                                                                                                                                                         | Run system/hardware  |
| `status.requeueState{count, requeueAt}`                                                                                                                                                 | Requeue attempts and the backoff deadline                | (K)                                                                                                                                                                                                                                                                                                                                                                         | Run events           |
| `status.resourceRequests[]`                                                                                                                                                             | Effective requests per podset, after limit ranges        | (K)                                                                                                                                                                                                                                                                                                                                                                         | Cost                 |
| `status.reclaimablePods[]`                                                                                                                                                              | Pods that finished early, so their quota is released     | (K)                                                                                                                                                                                                                                                                                                                                                                         | Queues               |
| `status.admissionChecks[]{name, state Pending/Ready/Retry/Rejected, message, podSetUpdates}`                                                                                            | State of ProvisioningRequest / MultiKueue checks         | (K)                                                                                                                                                                                                                                                                                                                                                                         | Pod start            |
| `status.accumulatedPastExexcutionTimeSeconds` (spelled this way upstream)                                                                                                               | Run time summed across evictions                         | (K)                                                                                                                                                                                                                                                                                                                                                                         | Cost                 |
| `status.schedulingStats.evictions[]{reason, underlyingCause, count}`                                                                                                                    | Eviction history                                         | (K)                                                                                                                                                                                                                                                                                                                                                                         | Run events           |
| `status.clusterName`                                                                                                                                                                    | MultiKueue placement                                     | (K)                                                                                                                                                                                                                                                                                                                                                                         | Run overview         |
| Labels: `kueue.x-k8s.io/queue-name` and `kueue.x-k8s.io/priority-class` on the job; `kueue.x-k8s.io/podset` on pods; the Workload is named `<kind>-<job>-<hash>` and owner-refs the job | Correlation                                              | (K)                                                                                                                                                                                                                                                                                                                                                                         | all                  |

**Kueue TAS** (K). A ResourceFlavor's `topologyName` points to a `Topology` CR whose `levels[].nodeLabel` lists node labels, e.g. `cloud.google.com/gce-topology-block` and `kubernetes.io/hostname`. A pod template asks for placement with annotations: `kueue.x-k8s.io/podset-required-topology`, `podset-preferred-topology`, `podset-unconstrained-topology`, or `podset-slice-required-topology` plus `podset-slice-size`. Pods are held by the `kueue.x-k8s.io/topology` scheduling gate until placed. A placement failure shows as `TopologyPlacementFailed`.

**MultiKueue** (K). An AdmissionCheck with `controllerName: kueue.x-k8s.io/multikueue` points to a `MultiKueueConfig`, which lists `MultiKueueCluster`s. Each cluster reports its own `Active` condition.

**ProvisioningRequest / DWS** (K). An AdmissionCheck with `controllerName: kueue.x-k8s.io/provisioning-request` uses a `ProvisioningRequestConfig` whose class is `queued-provisioning.gke.io` (DWS) or `check-capacity.autoscaling.x-k8s.io`. The ProvisioningRequest's conditions are `Accepted`, `Provisioned`, `Failed`, `BookingExpired` and `CapacityRevoked`. Admission-check wait therefore equals the DWS wait.

---

## 2. kube-scheduler and alternative schedulers

kube-scheduler serves `/metrics` on port 10259 over HTTPS, with auth. Managed control planes usually hide it. Exceptions: GKE with control-plane metrics enabled, and EKS through its `metrics.eks.amazonaws.com` proxy (K). **Events are therefore the primary source.**

| Name                                                                                                  | Source                                                 | Type & unit         | Labels                                     | Default on? | Why it matters                                                                | Used by view               | When absent                                                    |
| ----------------------------------------------------------------------------------------------------- | ------------------------------------------------------ | ------------------- | ------------------------------------------ | ----------- | ----------------------------------------------------------------------------- | -------------------------- | -------------------------------------------------------------- |
| `scheduler_pending_pods`                                                                              | (K)                                                    | gauge               | `queue`=active/backoff/unschedulable/gated | yes         | Separates unschedulable, backoff and gated pods (Kueue gates show as `gated`) | Queues, Pod start          | `scheduler-metrics-unavailable` (classify from events instead) |
| `scheduler_unschedulable_pods`                                                                        | (K)                                                    | gauge               | `plugin`, `profile`                        | yes         | Which plugin blocks pods (NodeResourcesFit, TaintToleration…)                 | Pod start, Recommendations | same                                                           |
| `scheduler_schedule_attempts_total`                                                                   | (K)                                                    | counter             | `result`=scheduled/unschedulable/error     | yes         | Outcome of each attempt                                                       | Queues                     | same                                                           |
| `scheduler_scheduling_attempt_duration_seconds`                                                       | (K)                                                    | histogram           | `result`, `profile`                        | yes         | Latency of each attempt                                                       | Queues                     | same                                                           |
| `scheduler_pod_scheduling_sli_duration_seconds`                                                       | (K, 1.27+, replaces `pod_scheduling_duration_seconds`) | histogram           | `attempts`                                 | yes         | Queue entry → bound, time spent gated excluded                                | Pod start                  | same                                                           |
| `scheduler_pod_scheduling_attempts`                                                                   | (K)                                                    | histogram           | —                                          | yes         | Attempts needed per pod                                                       | Pod start                  | same                                                           |
| `scheduler_preemption_attempts_total`, `scheduler_preemption_victims`                                 | (K)                                                    | counter / histogram | —                                          | yes         | Pressure from priority preemption                                             | Queues                     | same                                                           |
| `scheduler_framework_extension_point_duration_seconds`, `scheduler_plugin_execution_duration_seconds` | (K)                                                    | histogram           | `extension_point`, `plugin`, `status`      | yes         | Slow plugins (DRA, volume binding)                                            | —                          | —                                                              |

### 2.1 Classifying `FailedScheduling` events (K)

The message looks like `0/N nodes are available: <n> <reason>, <n> <reason>. preemption: …`. Split it on commas and classify each `<n> <reason>` clause.

| Substring                                                                                                                                          | Class                     | Recommendation                                       |
| -------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------- | ---------------------------------------------------- |
| `Insufficient nvidia.com/gpu` (also `amd.com/gpu`, `nvidia.com/mig-*`)                                                                             | GPU capacity              | `gpu-capacity-short` (scale the pool or raise quota) |
| `Insufficient cpu/memory/ephemeral-storage/hugepages-*`                                                                                            | other capacity            | `non-gpu-request-too-large`                          |
| `untolerated taint {nvidia.com/gpu…}`                                                                                                              | missing GPU toleration    | `missing-gpu-toleration`                             |
| `untolerated taint {node.kubernetes.io/not-ready / unreachable}`, `{ToBeDeletedByClusterAutoscaler}`                                               | node health or scale-down | —                                                    |
| `didn't match Pod's node affinity/selector`                                                                                                        | affinity                  | `selector-matches-no-node`                           |
| `didn't match pod topology spread constraints`                                                                                                     | topology spread           | —                                                    |
| `didn't match pod affinity rules` / `anti-affinity`                                                                                                | inter-pod affinity        | —                                                    |
| `volume node affinity conflict`, `unbound immediate PersistentVolumeClaims`, `didn't find available persistent volumes`, `exceed max volume count` | volume                    | `pvc-zone-mismatch`                                  |
| `node(s) were unschedulable`                                                                                                                       | node cordoned             | —                                                    |
| `didn't have free ports`                                                                                                                           | hostPort conflict         | —                                                    |
| `cannot allocate all claims`                                                                                                                       | DRA                       | `dra-claim-unallocatable`                            |
| `No preemption victims found` / `Preemption is not helpful`                                                                                        | preemption failed         | —                                                    |

### 2.2 Alternative schedulers (K)

- **Volcano.** A `PodGroup` (`scheduling.volcano.sh/v1beta1`) has `minMember`, `minResources` and `queue`. Its `status.phase` is Pending, Inqueue, Running, Unknown or Completed. Its conditions are `Unschedulable` (reasons `NotEnoughResources`, `NotEnoughPods`) and `Scheduled`. A `Queue` has a state (Open/Closed) and running/pending/inqueue counts. Metrics carry the `volcano_` prefix, e.g. `e2e_scheduling_latency_milliseconds` and `unschedule_task_count`.
- **KAI scheduler** (NVIDIA's open-sourced Run:ai scheduler). A `PodGroup` with `minMember` and `queue`. `Queue` CRs form a hierarchy with quota, limit and over-quota weight. Pods are labelled `kai.scheduler/queue`. GPU sharing uses a `gpu-fraction` annotation. Metric names are low confidence.
- **Run:ai.** The same model: projects map to queues, and metrics are `runai_*` (`runai_allocated_gpus`…).
- **scheduler-plugins coscheduling.** A `PodGroup` (`scheduling.x-k8s.io/v1alpha1`), selected by the label `scheduling.x-k8s.io/pod-group`.
- **Gang semantics.** A run counts as scheduled only once minMember pods are bound. A run that stays partly bound is a deadlock signal.

### 2.3 Cluster-autoscaler, Karpenter, image distribution

| Name                                                                                                                                                                                                               | Source                     | Type & unit                | Labels                                                            | Default on?                    | Why it matters                                                        | Used by view     | When absent                                          |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------- | -------------------------- | ----------------------------------------------------------------- | ------------------------------ | --------------------------------------------------------------------- | ---------------- | ---------------------------------------------------- |
| Pod events `TriggeredScaleUp` ("pod triggered scale-up: [{ng 2->4 (max: 8)}]"), `NotTriggerScaleUp` ("pod didn't trigger scale-up: 2 max node group size reached, 1 Insufficient nvidia.com/gpu"), `FailedScaleUp` | cluster-autoscaler (K)     | event                      | per pod                                                           | yes                            | Is the pod waiting for a node, or will it never get one?              | Pod start        | —                                                    |
| Node events `ScaleDown`, `ScaleDownFailed`; ConfigMap `cluster-autoscaler-status`                                                                                                                                  | CA (K)                     | event / status             | per node                                                          | yes                            | Node churn                                                            | Fleet            | —                                                    |
| `cluster_autoscaler_unschedulable_pods_count`                                                                                                                                                                      | CA (K)                     | gauge                      | `type`                                                            | yes (self-managed only)        | Pods CA cannot place                                                  | Queues           | `ca-metrics-unavailable`                             |
| `cluster_autoscaler_scaled_up_nodes_total`, `_scaled_down_nodes_total`                                                                                                                                             | CA (K)                     | counter                    | `reason`                                                          | yes                            | Scaling activity                                                      | Fleet            | —                                                    |
| `cluster_autoscaler_failed_scale_ups_total`                                                                                                                                                                        | CA (K)                     | counter                    | `reason`                                                          | yes                            | Stockouts and cloud errors                                            | Recommendations  | —                                                    |
| `cluster_autoscaler_node_group_min_count/max_count/target_count`                                                                                                                                                   | CA (K)                     | gauge                      | `node_group`                                                      | `--emit-per-nodegroup-metrics` | Headroom left before the group's max                                  | Fleet            | —                                                    |
| `cluster_autoscaler_nodes_count`                                                                                                                                                                                   | CA (K)                     | gauge                      | `state` (ready, unready, notStarted, longUnregistered)            | yes                            | Nodes stuck while booting                                             | Fleet            | —                                                    |
| Karpenter `NodeClaim` (`karpenter.sh/v1`) conditions `Launched`, `Registered`, `Initialized`, `Ready`, `Drifted`, `Consolidatable`, `Disrupted`; status `providerID`, `nodeName`, `capacity`, `allocatable`        | Karpenter (K)              | conditions with timestamps | per nodeclaim                                                     | yes                            | Exact node boot phases (Initialized = the GPU device plugin is ready) | Pod start, Fleet | —                                                    |
| `karpenter_nodeclaims_created_total`, `_terminated_total`, `_disrupted_total`                                                                                                                                      | (K)                        | counter                    | `nodepool`, `reason`, `capacity_type`                             | yes                            | Churn by reason (consolidation, drift, interruption)                  | Fleet            | —                                                    |
| `karpenter_pods_startup_duration_seconds`                                                                                                                                                                          | (K)                        | summary, s                 | —                                                                 | yes                            | Pod created → running                                                 | Pod start        | —                                                    |
| `karpenter_pods_state`                                                                                                                                                                                             | (K)                        | gauge                      | `name`, `namespace`, `node`, `phase`, `capacity_type`, `nodepool` | yes                            | Spot or on-demand placement per pod, for cost                         | Cost             | —                                                    |
| `karpenter_nodepools_usage`, `_limit`, `karpenter_nodes_allocatable`                                                                                                                                               | (K)                        | gauge                      | `nodepool`, `resource_type`                                       | yes                            | Nodepool headroom                                                     | Fleet            | —                                                    |
| `karpenter_interruption_received_messages_total`                                                                                                                                                                   | Karpenter AWS (K)          | counter                    | `message_type` (spot_interrupted, scheduled_change, rebalance…)   | needs the SQS queue            | Spot reclaim notices, so an eviction can be expected                  | Run events, Cost | `karpenter-interruption-queue-off`                   |
| `karpenter_cloudprovider_errors_total`                                                                                                                                                                             | (K)                        | counter                    | `error` (InsufficientCapacityError…)                              | yes                            | GPU stockouts                                                         | Recommendations  | —                                                    |
| Spegel `spegel_mirror_requests_total{registry, cache, source}`, `spegel_resolve_duration_seconds`; Dragonfly `dragonfly_dfdaemon_*`                                                                                | P2P image distribution (K) | counter/histogram          | per node                                                          | when installed                 | Image cache hit rate                                                  | Pod start        | `image-p2p-absent` (suggest on large-image clusters) |

**GKE** (K):

- DWS through ProvisioningRequest (see 1.4).
- Image streaming (`--enable-image-streaming`): `Pulled` times drop to near zero, and the lazy pull happens in the background.
- Node auto-provisioning.

---

## 3. Pod start timeline

### 3.1 Pod conditions (K, from the k8s pod-lifecycle docs)

| Condition                   | Set when                                                    | Notes                                                                                                                                                                   |
| --------------------------- | ----------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `PodScheduled`              | The pod is bound to a node                                  | When False, reason is `Unschedulable` (message = FailedScheduling text) or `SchedulingGated` (Kueue/TAS gates)                                                          |
| `PodReadyToStartContainers` | Sandbox and network are ready (1.29+ beta)                  | Called `PodHasNetwork` in 1.25–1.27                                                                                                                                     |
| `Initialized`               | Init containers are done                                    |                                                                                                                                                                         |
| `ContainersReady` / `Ready` | Containers are ready; `Ready` also needs the readinessGates |                                                                                                                                                                         |
| `DisruptionTarget`          | The pod is about to be killed by a disruption               | Reasons `PreemptionByScheduler`, `DeletionByTaintManager`, `EvictionByEvictionAPI`, `DeletionByPodGC`, `TerminationByKubelet`. This is what podFailurePolicy matches on |

### 3.2 Container states, reasons, exit codes (K)

- **waiting:**
  - `ContainerCreating`, `PodInitializing`.
  - Image problems: `ErrImagePull`, `ImagePullBackOff`, `InvalidImageName`.
  - Config problems: `CreateContainerConfigError` (a missing secret or configmap), `CreateContainerError`, `RunContainerError`.
  - `CrashLoopBackOff`.
- **terminated:** `OOMKilled`, `Error`, `Completed`, `ContainerCannotRun`, `DeadlineExceeded`, `StartError`.
- **Exit codes:**

  | Code      | Meaning                                          |
  | --------- | ------------------------------------------------ |
  | 0         | success                                          |
  | 1         | application error                                |
  | 126 / 127 | not executable / not found                       |
  | 134       | SIGABRT (CUDA/NCCL asserts)                      |
  | **137**   | SIGKILL: OOM, or killed after the grace period   |
  | **139**   | SIGSEGV                                          |
  | **143**   | SIGTERM: graceful stop on preemption or eviction |

- `lastState.terminated` and `restartCount` explain restarts.
- Pod-level `status.reason`:
  - `Evicted` (node pressure), `NodeLost`.
  - `UnexpectedAdmissionError`: the device plugin's Allocate failed, a common GPU fault.
  - `OutOfnvidia.com/gpu`.

### 3.3 Events (K)

| Reason                                                                                                                                        | Use                                                                                                                                                                                                                                           |
| --------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `Scheduled`                                                                                                                                   | "Successfully assigned ns/pod to node": the bind time                                                                                                                                                                                         |
| `Pulling` / `Pulled`                                                                                                                          | `Successfully pulled image "x" in 1m2.3s (1m10s including waiting). Image size: N bytes.` (1.28+ added the wait and the size), or `Container image "x" already present on machine`. Parse the pull seconds, wait seconds, bytes and cache hit |
| `Created`, `Started`, `Killing`, `BackOff`, `Unhealthy`                                                                                       | Container lifecycle                                                                                                                                                                                                                           |
| `FailedMount`, `FailedAttachVolume`                                                                                                           | Storage is stuck (multi-attach, CSI timeout)                                                                                                                                                                                                  |
| `FailedCreatePodSandBox`                                                                                                                      | CNI or runtime failure (e.g. the nvidia runtime class is missing)                                                                                                                                                                             |
| `UnexpectedAdmissionError`                                                                                                                    | GPU allocation failed                                                                                                                                                                                                                         |
| `Preempted`, `Evicted`, `TaintManagerEviction`                                                                                                | Disruption                                                                                                                                                                                                                                    |
| `NodeNotReady`, `Rebooted`, pressure conditions                                                                                               | Node health                                                                                                                                                                                                                                   |
| Kueue job events `Suspended`, `Resumed`, `Started`, `Stopped`; Workload events `QuotaReserved`, `Admitted`, `Pending`, `Evicted`, `Preempted` | Queue stages                                                                                                                                                                                                                                  |
| `TriggeredScaleUp`, `Nominated` ("Pod should schedule on nodeclaim X"), `Launched`, `Registered`, `DisruptionBlocked`                         | Node provisioning                                                                                                                                                                                                                             |

Events are kept about 1h by default (`--event-ttl`), so we must stream them (the k8s events receiver) rather than poll later. If we don't: `events-not-collected`.

### 3.4 Stages and how to compute them

For a gang, take the **max** across pods: the run starts when its slowest rank starts.

| Stage                                 | From → To                                                                                   | Source              |
| ------------------------------------- | ------------------------------------------------------------------------------------------- | ------------------- |
| S0 submit → workload                  | job created → Workload created                                                              | objects             |
| S1 queue wait                         | Workload created → `QuotaReserved=True`                                                     | Workload            |
| S2 admission checks (DWS, MultiKueue) | QuotaReserved → `Admitted=True`                                                             | Workload            |
| S3 pod creation                       | Admitted (job unsuspended) → pod created                                                    | objects             |
| S4 gated                              | pod created → `SchedulingGated` cleared                                                     | PodScheduled reason |
| S5 scheduling                         | gate cleared → `PodScheduled=True`                                                          | condition           |
| S5a node provisioning (inside S5)     | `TriggeredScaleUp`/`Nominated` → node Ready (NodeClaim Launched → Registered → Initialized) | CA/Karpenter        |
| S6 sandbox                            | PodScheduled → `PodReadyToStartContainers`                                                  | condition           |
| S7 image pull                         | `Pulling` → `Pulled`, summed across containers                                              | events              |
| S8 init                               | sandbox ready → `Initialized`                                                               | condition           |
| S9 start → ready                      | `Initialized` → `Ready`                                                                     | condition           |
| S10 app warm-up                       | Ready → first training step, or model loaded                                                | app telemetry       |
| Time to first step                    | job created → first step of the slowest rank                                                | composite           |

---

## 4. Orchestrator objects

### 4.1 batch/v1 Job (K, from the k8s Job docs)

**Spec fields:**

- `completionMode: Indexed`, `completions`, `parallelism`.
- `backoffLimit`; `backoffLimitPerIndex` with `maxFailedIndexes` (GA 1.33).
- `podFailurePolicy.rules[{action FailJob|FailIndex|Ignore|Count, onExitCodes, onPodConditions[{type: DisruptionTarget}]}]`.
- `successPolicy.rules[{succeededIndexes, succeededCount}]`.
- `podReplacementPolicy`, `suspend`, `managedBy`, `activeDeadlineSeconds`.

**Status fields:** `active`, `ready`, `succeeded`, `failed`, `terminating`, `completedIndexes`, `failedIndexes`, `startTime`, `completionTime`.

**Conditions:**

- `Suspended`.
- `FailureTarget` and then `Failed`, with reasons `BackoffLimitExceeded`, `DeadlineExceeded`, `PodFailurePolicy`, `MaxFailedIndexesExceeded`, `FailedIndexes`.
- `SuccessCriteriaMet` and then `Complete`, with reasons `SuccessPolicy` or `CompletionsReached`.

**Correlation:**

- Label/annotation `batch.kubernetes.io/job-completion-index`, also exposed as env `JOB_COMPLETION_INDEX`.
- Label `batch.kubernetes.io/job-name`.
- Retries per index = failed pods grouped by that index.

### 4.2 JobSet (`jobset.x-k8s.io/v1alpha2`) (V for fields and labels)

**Status fields:**

- `conditions`, typed `StartupPolicyInProgress`/`StartupPolicyCompleted`, `Completed`, `Failed`, `Suspended`.
- `restarts`, and `restartsCountTowardsMax`.
- `executionAttempts` and `terminalState`.
- `startTime`.
- `replicatedJobsStatus[]{name, ready, succeeded, failed, active, suspended, jobRestarts[], jobRestartsCountTowardsMax[]}`.
- `previousInPlaceRestartAttempt` and `currentInPlaceRestartAttempt`.

**Spec fields:**

- `failurePolicy{maxRestarts, restartStrategy Recreate|BlockingRecreate, rules[{name, action FailJobSet|RestartJobSet|RestartJobSetAndIgnoreMaxRestarts, onJobFailureReasons, onJobFailureMessagePatterns, targetReplicatedJobs}]}` (V for the rule fields; K for the enum values).
- `successPolicy{operator All|Any, targetReplicatedJobs}`.
- `startupPolicy{startupPolicyOrder AnyOrder|InOrder}`.
- `replicatedJobs[]{name, groupName, replicas, template, dependsOn[{name, status}]}`.
- `network{enableDNSHostnames, subdomain, publishNotReadyAddresses}`.
- `coordinator`, `suspend`, `managedBy`, `ttlSecondsAfterFinished`, `activeDeadlineSeconds`, `volumeClaimPolicies`.

**Labels (V):**

- `jobset.sigs.k8s.io/jobset-name`, `jobset-uid`.
- `replicatedjob-name`, `replicatedjob-replicas`.
- `job-index`, `job-global-index`, `global-replicas`, `job-key`.
- `coordinator`.

The pod annotation `jobset.sigs.k8s.io/restart-attempt` carries the attempt number (K).

**Global rank** = `job-global-index × pods-per-job + JOB_COMPLETION_INDEX`.

### 4.3 Kubeflow Trainer v2 (`trainer.kubeflow.org/v1alpha1`) (K)

**TrainJob spec:**

- `runtimeRef{name, kind ClusterTrainingRuntime|TrainingRuntime}`.
- `trainer{image, command, numNodes, numProcPerNode, resourcesPerNode, env}`.
- `initializer{dataset, model}`.
- `podTemplateOverrides`, `suspend`, `managedBy`.

**TrainJob status:**

- Conditions: `Suspended`, `Complete`, and `Failed`, which carries JobSet reasons.
- `jobsStatus[]{name, ready, succeeded, failed, active, suspended}`, one entry per replicated job: `dataset-initializer`, `model-initializer`, `node`, `launcher`.

**How it runs:**

- A TrainingRuntime embeds a JobSet template plus `mlPolicy{numNodes, torch{numProcPerNode}, mpi}` and `podGroupPolicy{coscheduling|volcano}`.
- Each TrainJob renders to a JobSet, so the JobSet's restarts are the TrainJob's attempts.

**Injected values:**

- Env from the torch plugin: `PET_NNODES`, `PET_NPROC_PER_NODE`, `PET_NODE_RANK`, `PET_MASTER_ADDR`, `PET_MASTER_PORT`.
- Labels: `trainer.kubeflow.org/trainjob-name`, and the step label (`trainjob-ancestor-step`). The step label is a natural **phase** boundary: initializer, then trainer.

### 4.4 Legacy Training Operator v1 (`kubeflow.org/v1` PyTorchJob/TFJob/XGBoostJob/PaddleJob/JAXJob; MPIJob `v2beta1`) (K)

**Status:**

- Conditions: `Created`, `Running`, `Restarting`, `Succeeded`, `Failed`, `Suspended`, with reasons like `PyTorchJobRestarting`.
- `replicaStatuses{Master|Worker|Launcher: {active, succeeded, failed, labelSelector}}`.
- `startTime`, `completionTime`.

**Spec:**

- `runPolicy{cleanPodPolicy, backoffLimit, activeDeadlineSeconds, schedulingPolicy{minAvailable, queue, priorityClass}, suspend}`.
- `elasticPolicy{minReplicas, maxReplicas, maxRestarts, rdzvBackend}`.
- Per replica, `restartPolicy`: Always, OnFailure, Never or ExitCode.

**Labels:** `training.kubeflow.org/job-name`, `replica-type`, `replica-index`, `job-role=master`.

**Env:** `MASTER_ADDR`, `MASTER_PORT`, `WORLD_SIZE`, `RANK`, and the elastic `PET_*` variables.

**Global rank:** the master is 0; worker i is i+1.

### 4.5 KubeRay (`ray.io/v1`) (K)

**RayJob status:**

- `jobStatus`: PENDING, RUNNING, STOPPED, SUCCEEDED or FAILED.
- `jobDeploymentStatus`: Initializing, Running, Complete, Failed, Suspending, Suspended, Retrying, Waiting or ValidationFailed.
- `reason`: `SubmissionFailed`, `DeadlineExceeded`, `AppFailed`, `ValidationFailed`…
- `message`, `jobId`, `rayClusterName`, `dashboardURL`, `startTime`, `endTime`.
- `failed`/`succeeded` counts: retries under `backoffLimit`.

**RayCluster status:**

- Conditions: `RayClusterProvisioned`, `HeadPodReady`, `RayClusterReplicaFailure`, `RayClusterSuspended`.
- `readyWorkerReplicas`, `availableWorkerReplicas`, `desiredWorkerReplicas`, `desiredGPU`.

**Labels:** `ray.io/cluster`, `ray.io/node-type`, `ray.io/group`. Ray ranks are logical, so a rank comes from app telemetry, not from a pod index.

**Operator metrics:** `kuberay_cluster_provisioned_duration_seconds`, `kuberay_job_execution_duration_seconds{job_deployment_status, retry_count}`.

### 4.6 Volcano Job (`batch.volcano.sh/v1alpha1`) (K)

**Status:**

- `state.phase`: Pending, Aborting, Aborted, Running, Restarting, Completing, Completed, Terminating, Terminated or Failed.
- `state.reason`.
- `minAvailable`, plus counts `pending`, `running`, `succeeded`, `failed`.
- `retryCount`, `runningDuration`.
- `taskStatusCount`.

**Policies:** `{event PodEvicted|PodFailed, action RestartJob|AbortJob|CompleteJob}`.

**Labels and env:** label `volcano.sh/job-name`; env `VC_TASK_INDEX`.

### 4.7 LeaderWorkerSet (`leaderworkerset.x-k8s.io/v1`) (K)

**Status:** conditions `Available`, `Progressing`, `UpdateInProgress`; `readyReplicas`, `updatedReplicas`.

**Spec:** `leaderWorkerTemplate{size, restartPolicy RecreateGroupOnPodRestart|None}`, `startupPolicy`.

**Labels:** `leaderworkerset.sigs.k8s.io/name`, `group-index`, `worker-index` (0 is the leader).

**Env:** `LWS_LEADER_ADDRESS`, `LWS_GROUP_SIZE`, `LWS_WORKER_INDEX`.

**What it means:** a group restart is an outage of one multi-node inference replica.

### 4.8 KServe (`serving.kserve.io`) (K)

**InferenceService conditions:** `Ready`, `PredictorReady`, `TransformerReady`, `IngressReady`, `LatestDeploymentReady`, `RoutesReady`.

**InferenceService status fields:**

- `url`.
- `components.predictor{latestReadyRevision, latestCreatedRevision, traffic[]}`.
- `modelStatus`, which holds:
  - `states{activeModelState, targetModelState}`: Pending, Standby, Loading, Loaded or FailedToLoad.
  - `transitionStatus`: UpToDate, InProgress, BlockedByFailedLoad or InvalidSpec.
  - `lastFailureInfo{reason, message, exitCode}`, where reason is ModelLoadFailed, RuntimeUnhealthy, NoSupportingRuntime…
  - `copies{failedCopies, totalCopies}`.
- `deploymentMode`.

**Related objects:**

- `ServingRuntime`: `supportedModelFormats`, `multiModel`, `protocolVersions`.
- `LLMInferenceService` (v1alpha1): llm-d style serving with disaggregated prefill/decode.
- `LocalModelCache` and `LocalModelNodeGroup`: status `copies{total, available, failed}`, and per-node state NodeDownloadPending, Downloading, Downloaded or DownloadError.

**Labels:** `serving.kserve.io/inferenceservice`, `component`.

### 4.9 Gateway API Inference Extension (K)

**InferencePool** (`inference.networking.k8s.io/v1`):

- Spec: `selector`, `targetPorts`, `endpointPickerRef`.
- Status: `parents[].conditions`, typed `Accepted` and `ResolvedRefs`.

**InferenceModel** (`v1alpha2`; renamed **InferenceObjective** in later releases):

- `modelName`, `criticality`/`priority`, `targetModels[{name, weight}]`, `poolRef`.

**EPP metrics:**

- `inference_model_request_total`, `inference_model_request_duration_seconds`, `inference_model_input_tokens`.
- `inference_pool_average_kv_cache_utilization`, `inference_pool_average_queue_size`, `inference_pool_ready_pods`.

### 4.10 Joining ranks to pods

| Orchestrator | Run key                            | Replica group                                         | Index                                           | Attempt                                                   |
| ------------ | ---------------------------------- | ----------------------------------------------------- | ----------------------------------------------- | --------------------------------------------------------- |
| Indexed Job  | job uid                            | —                                                     | `job-completion-index` / `JOB_COMPLETION_INDEX` | failed pods per index                                     |
| JobSet       | `jobset-uid`                       | `replicatedjob-name`, `job-index`, `job-global-index` | `JOB_COMPLETION_INDEX`                          | `status.restarts`, annotation `restart-attempt`           |
| Trainer v2   | `trainjob-name`                    | step label                                            | `PET_NODE_RANK`                                 | through the JobSet                                        |
| PyTorchJob   | `training.kubeflow.org/job-name`   | `replica-type`                                        | `replica-index`, `RANK`                         | count of `Restarting` transitions, container restartCount |
| RayJob       | `ray.io/cluster`                   | `ray.io/group`                                        | app-level                                       | `status.failed`                                           |
| LWS          | `leaderworkerset.sigs.k8s.io/name` | `group-index`                                         | `worker-index`                                  | group recreate events                                     |
| Kueue        | Workload (owner chain)             | `kueue.x-k8s.io/podset`                               | —                                               | `requeueState.count`, eviction count                      |

torchrun sets `RANK`, `LOCAL_RANK`, `WORLD_SIZE`, `GROUP_RANK`, `TORCHELASTIC_RESTART_COUNT`, `TORCHELASTIC_RUN_ID`, `MASTER_ADDR` (K). Record them as OTel resource attributes (`ml.rank`, `ml.local_rank`, `ml.attempt`). GPU telemetry then joins to pods through DCGM exporter's `pod`/`namespace`/`container` labels.

---

## 5. Cost and goodput

### 5.1 GPU-hour accounting

| Quantity                 | Formula                                                                                       | Source                                                                                               |
| ------------------------ | --------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| Allocated GPU-h          | Σ `requests[nvidia.com/gpu]` × Δt while the pod is bound and not terminated                   | kube-state-metrics `kube_pod_container_resource_requests{resource="nvidia_com_gpu"}` + pod phase (K) |
| MIG                      | fraction = slices ÷ 7 (A100/H100), or a price per profile                                     | device plugin resource name                                                                          |
| Time-slicing / MPS       | fraction = 1 ÷ replicas                                                                       | device plugin config                                                                                 |
| Unallocated node GPU-h   | (allocatable − allocated) × Δt                                                                | `kube_node_status_allocatable`                                                                       |
| Utilised GPU-h           | allocated GPU-h × mean `DCGM_FI_PROF_GR_ENGINE_ACTIVE` (fallback: `DCGM_FI_DEV_GPU_UTIL`/100) | DCGM                                                                                                 |
| Idle while allocated     | allocated GPU-h where GR_ENGINE_ACTIVE stays < 5% for ≥ N min                                 | `gpu-allocated-idle`                                                                                 |
| Held by queue but unused | ∫ (`cluster_queue_resource_reservation` − `_usage`)                                           | Kueue                                                                                                |

### 5.2 Price models

**Capacity types the customer can price:**

- `on_demand`.
- `spot`, with observed reclaims.
- `reserved`/`committed`: the commitment is amortised per hour, so idle GPU time is pure waste.
- `owned`: capex ÷ depreciation hours, plus power and colo.

**Fields each price carries:**

- `gpu_product`, from the node label `nvidia.com/gpu.product` or the DCGM model name.
- `capacity_type`, from `karpenter.sh/capacity-type`, `cloud.google.com/gke-spot` or `eks.amazonaws.com/capacityType`.
- `$ per GPU-hour`, plus an optional `$ per node-hour`.
- Currency, and an `effective_from` date. Prices are versioned so that recomputing history stays stable.

| Derived                   | Formula                                                                              |
| ------------------------- | ------------------------------------------------------------------------------------ |
| $ per run                 | Σ_pods allocated GPU-h × price(product, capacity_type, t), plus the CPU/memory share |
| $ per phase               | the same sum, windowed by phase spans (init, pretrain, train, eval)                  |
| $ per 1M training tokens  | run $ ÷ tokens × 1e6                                                                 |
| $ per 1M inference tokens | service GPU $ ÷ (`vllm:prompt_tokens_total` + `vllm:generation_tokens_total`) × 1e6  |
| $ wasted                  | $ of each badput category (5.4)                                                      |

### 5.3 OpenCost / Kubecost (K)

**How it allocates:**

- Node cost goes to containers by max(request, usage) for each resource.
- GPU is allocated by request: GPU-hours = request × hours.
- The GPU price comes from cloud pricing or from custom pricing.
- Node cost that nothing claims is reported as idle, under `__idle__`.

**Metrics:** `node_gpu_hourly_cost`, `node_gpu_count`, `container_gpu_allocation`, `node_total_hourly_cost`, `kubecost_node_is_spot`.

**What we do:** if these metrics exist, adopt them (`opencost-detected`). If not, compute the same numbers ourselves.

### 5.4 Goodput (Google's ML Goodput model; K, low-to-medium confidence on the library's enum names)

The model has three factors:

- **Scheduling goodput** = time all required resources were available ÷ time the job wanted them.
- **Runtime goodput** = productive step time that is kept ÷ time the resources were held.
  - Badput: initialisation (pod start, framework init, compile, dataloader warm-up), blocking checkpoint save, checkpoint restore, **wasted progress** since the last good checkpoint before a failure, data-loading stalls, hangs until the watchdog fires, and unproductive restarts.
- **Program goodput** = MFU = (model FLOPs per step × steps/s) ÷ (peak FLOPs × GPUs).

**Overall goodput** ≈ scheduling × runtime × program.

The `ml-goodput-measurement` library's badput types: `TPU_INITIALIZATION`, `TRAINING_PREP`, `PROGRAM_STARTUP`, `DATA_LOADING_SYNC`, `DATA_LOADING_ASYNC`, `UNPRODUCTIVE_CHECKPOINT_SAVE_TIME`, `UNPRODUCTIVE_CHECKPOINT_RESTORE_TIME`, `WASTED_PROGRESS_FROM_DISRUPTION`, `CUSTOM_BADPUT_EVENTS`, `OTHER`.

**Computing it from our signals:**

- **queue badput** = Σ (S1 + S2) + the gaps between eviction and re-admission.
- **startup badput**, per attempt = from admission until the last rank's first step (S3–S10).
- **checkpoint badput** = Σ the blocking spans of checkpoint saves.
- **wasted progress**, per failure = failure time − last successful checkpoint time. Without app telemetry, bound it by `pods_ready_to_evicted_time_seconds`.
- **data stall** = step time where the dataloader wait exceeds a threshold.
- **hang** = time from the last step's progress until the kill.
- **runtime goodput** = productive step time ÷ Σ the allocated time of every attempt.
- **$ goodput** = productive GPU-h ÷ allocated GPU-h.

---

## Derived metrics

- **Queue wait, per run** = QuotaReserved transition time − Workload creation time.
- **Admission-check wait** = Admitted − QuotaReserved.
- **Time to all-ready** = PodsReady − Admitted (fallback: the latest pod Ready).
- **Image pull share** = Σ Pulled seconds ÷ startup time.
- **Image cache hit ratio** = "already present" events ÷ all pull outcomes.
- **Queue pressure** = `cluster_queue_resource_pending` ÷ `cluster_queue_nominal_quota`, per GPU flavor.
- **Borrowing ratio** = max(0, usage − nominal) ÷ nominal.
- **Preemption cost** = Σ `pods_ready_to_evicted_time_seconds` × GPUs × price.
- **Restart rate** = JobSet `restarts` ÷ run hours.
- **MTBF** = productive time ÷ failures.
- **Node provisioning time** = NodeClaim Initialized − NodeClaim created.
- **Allocation efficiency** = allocated GPU-h ÷ allocatable GPU-h.
- **Utilisation efficiency** = utilised GPU-h ÷ allocated GPU-h.

## Gotchas

- **Kueue histograms are per queue, not per run.** Per-run stage times come from Workload conditions and pod events.
- **Condition timestamps are overwritten.** Each re-admission overwrites `lastTransitionTime`, so watch transitions as they happen and store them as events, or earlier attempts are lost.
- **Events expire (~1h) and are deduplicated.** Use `count` and `series` to keep the first and last time.
- **kube-scheduler metrics are usually unavailable** on managed clusters.
- **Cardinality.** `local_queue_*` is off by default. Kueue custom labels can explode cardinality: never label by workload.
- **Gangs.** One pod stuck in ImagePullBackOff leaves every allocated GPU idle. Count that as startup badput for the whole gang.
- **MIG and time-slicing.** An `nvidia.com/gpu` request is not a physical GPU. Price by profile, and for MIG use DCGM's per-instance `GPU_I_ID` series.
- **Two kinds of idle.** Pending pods hold Kueue quota but cost nothing. Empty nodes cost money but hold no pods: attribute them to `__idle__` per node pool.
- **Spot reclaim and Kueue preemption look alike.** Both end in exit 143/137 with `DisruptionTarget`. Tell them apart by the DisruptionTarget reason and by the Kueue `Evicted` reason.
- **JobSet restart counters.** `restartsCountTowardsMax` excludes the restarts made by `RestartJobSetAndIgnoreMaxRestarts` rules (e.g. on preemption). Read both counters to separate fault restarts from policy restarts.
- **Typo in a Kueue field name.** `accumulatedPastExexcutionTimeSeconds` is spelled that way upstream: match it exactly.
- **Kueue metrics change across versions.** They moved a lot between 0.9 and 0.14+. Detect the version from `kueue_build_info` and degrade gracefully.
- **Prices must be versioned.** Pricing old runs at today's price is wrong.

## Sources

- (V) https://raw.githubusercontent.com/kubernetes-sigs/kueue/main/pkg/metrics/metrics.go
- (V) https://raw.githubusercontent.com/kubernetes-sigs/kueue/main/apis/kueue/v1beta2/workload_types.go
- (V) https://raw.githubusercontent.com/kubernetes-sigs/jobset/main/api/jobset/v1alpha2/jobset_types.go
- (K) https://kueue.sigs.k8s.io/docs/reference/metrics/
- (K) https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/ , https://kubernetes.io/docs/concepts/workloads/pods/disruptions/ , https://kubernetes.io/docs/concepts/workloads/controllers/job/
- (K) https://kubernetes.io/docs/reference/instrumentation/metrics/
- (K) https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/proposals/metrics.md , https://karpenter.sh/docs/reference/metrics/
- (K) https://www.kubeflow.org/docs/components/trainer/ , https://docs.ray.io/en/latest/cluster/kubernetes/ , https://lws.sigs.k8s.io/ , https://kserve.github.io/website/ , https://gateway-api-inference-extension.sigs.k8s.io/
- (K) https://www.opencost.io/docs/ , https://cloud.google.com/blog/products/ai-machine-learning/goodput-metric-as-measure-of-ml-productivity , https://github.com/AI-Hypercomputer/ml-goodput-measurement
