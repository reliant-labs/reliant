"""
Shorthand for the series a metrics dashboard declares most, over the generated
schemas in dashspec_gen.k. Hand-written, and signal-generic: each helper only
fills in the fields an OpenTelemetry metric of that kind always takes. None of
them knows a metric name; the caller names it.

    import forge.dashspec as ds

    ds.Panel {
        id = "rps", title = "Requests / s", kind = "line", signal = "metrics"
        series = [ds.rate("rps", "http.server.request.count")]
    }

Every helper returns a schema instance, so the result is checked like a
hand-written one, and a field the helper does not take is still set on it with
a `|` merge: `ds.rate("rps", "m") | {thresholds = ds.Thresholds {good = 1, poor = 5}}`.
"""

# rate is the per-second rate of a monotonic sum (a counter).
rate = lambda label: str, metric: str, where: [Predicate] = [] -> Series {
    Series {
        label = label
        agg = "rate"
        field = metric
        metricType = "sum"
        where = where if where else Undefined
    }
}

# quantile is the q quantile (0 < q <= 1) of a distribution metric: a
# histogram by default, or an exp_histogram or summary.
quantile = lambda label: str, metric: str, q: float, metric_type: str = "histogram", where: [Predicate] = [] -> Series {
    Series {
        label = label
        agg = "quantile"
        quantile = q
        field = metric
        metricType = metric_type
        where = where if where else Undefined
    }
}

# gauge is a gauge's value: its last by default, or avg, min, max or sum.
gauge = lambda label: str, metric: str, agg: str = "last", where: [Predicate] = [] -> Series {
    Series {
        label = label
        agg = agg
        field = metric
        metricType = "gauge"
        where = where if where else Undefined
    }
}

# ratio is a line panel plotting num / den: an error rate is the failed
# requests over all of them. Both are series of the panel's signal (metrics by
# default); the panel's unit defaults to percent, a fraction in [0, 1].
ratio = lambda id: str, title: str, num: Series, den: Series, signal: str = "metrics", unit: str = "percent" -> Panel {
    Panel {
        id = id
        title = title
        kind = "line"
        signal = signal
        series = [num, den]
        ratio = True
        unit = unit
    }
}
