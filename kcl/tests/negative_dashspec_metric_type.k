"""
Negative test — a series metricType outside dashspec's MetricType* constants
(read through the //dashspec:enum directive on Series.MetricType) is refused.
"""

# expect: Series.metricType must be one of
# expect: "exp_histogram"

import forge.dashspec as ds

_s = ds.quantile("p95", "demo.request.duration", 0.95, metric_type = "exponential")
