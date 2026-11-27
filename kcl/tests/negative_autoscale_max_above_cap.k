"""
Negative test — autoscale.maxReplicas above the platform cap.

An autoscaled workload scales to at most 10 replicas
(v1alpha1.MaxAutoscaleReplicas), so one workload's load cannot claim an
unbounded share of the nodes it shares.
"""

# expect: Autoscale.maxReplicas must be at most 10

import forge

_bad = forge.Autoscale {minReplicas = 2, maxReplicas = 20}
