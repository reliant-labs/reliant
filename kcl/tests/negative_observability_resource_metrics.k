"""
Negative test — ObservabilitySignals.resource_metrics, the env's default, is
one of the modes forge/pkg/observe knows (auto, all, process, host, off).
"""

# expect: ObservabilitySignals.resource_metrics must be one of

import forge

signals = forge.ObservabilitySignals {resource_metrics = "cpu"}
