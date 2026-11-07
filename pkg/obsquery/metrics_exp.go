package obsquery

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// Exponential histograms and summaries (DESIGN §3.2 "Histograms").
//
// An exponential histogram point holds, per sign, a dense run of counts
// starting at an offset; bucket i at scale s covers (2^(i·2^-s), 2^((i+1)·2^-s)]
// (the negative side mirrored). SQL reduces every series to its own minimum
// scale and takes the per-bucket increments, so the rows that leave
// ClickHouse are small maps keyed by bucket index; Go merges the series that
// disagree on scale, converts to (bounds, counts) and hands the result to
// HistogramQuantile, the path an explicit histogram takes.
//
// A summary keeps Count and Sum, which flow through the histogram counter
// paths (avg, rate, count, increase, sum), and the quantiles its exporter
// computed. A stored quantile cannot be merged across series, so it is read
// back only when exactly one series answers.

// hasCounts reports whether the kind's points carry Count and Sum.
func (t MetricType) hasCounts() bool {
	return t == MetricHistogram || t == MetricExpHistogram || t == MetricSummary
}

// tmpExpr is the temporality column of the kind. A summary has none: it is the
// Prometheus-style cumulative metric.
func (q MetricQuery) tmpExpr() string {
	if q.Info.Type == MetricSummary {
		return "toInt32(2)"
	}
	return "AggregationTemporality"
}

// sqlParts are the fragments Compile has built that a kind's own statement
// shares with every other.
type sqlParts struct {
	bucket, labels, total, gSelList, gNameList string
	from, sid, startCond                       string
	conds                                      []string
	b                                          *builder
}

const emptyMap = "(CAST([], 'Array(Int64)'), CAST([], 'Array(Float64)'))"

// expQuantileSQL compiles the quantile of an exponential histogram. Columns:
// bucket, labels, scale (Int32: the minimum scale of the rows), zero (Float64),
// pk, pv (positive bucket index and increment), nk, nv (negative), series_total.
// There is one row per distinct scale in a (bucket, labels); mergeExpRows folds
// them.
func (q MetricQuery) expQuantileSQL(p sqlParts) string {
	const w = "WINDOW w AS (PARTITION BY sid ORDER BY TimeUnix ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)"
	down := func(off, counts string) string {
		return "mapAdd((arrayMap(m -> bitShiftRight(toInt64(" + off + ") + m, d), range(toUInt32(length(" + counts + ")))), " +
			"arrayMap(x -> toFloat64(x), " + counts + ")), " + emptyMap + ")"
	}
	return "SELECT bucket, " + p.labels + " AS labels, ms AS scale, zero, pm.1 AS pk, pm.2 AS pv, nm.1 AS nk, nm.2 AS nv, " + p.total + " AS series_total FROM (\n" +
		"  SELECT " + p.bucket + " AS bucket" + p.gNameList + ", ms, sum(zinc) AS zero,\n" +
		"         sumMap(pt.1, arrayMap(x -> greatest(x, 0.), pt.2)) AS pm, sumMap(nt.1, arrayMap(x -> greatest(x, 0.), nt.2)) AS nm FROM (\n" +
		// increments: the sample less its predecessor, or the whole sample after a reset (or a delta one)
		"    SELECT TimeUnix, tmp, is_first, ms" + p.gNameList + ",\n" +
		"           if(tmp = 1 OR is_reset, z, greatest(z - pz, 0.)) AS zinc,\n" +
		"           if(tmp = 1 OR is_reset, (pk, pv), mapSubtract((pk, pv), (ppk, ppv))) AS pt,\n" +
		"           if(tmp = 1 OR is_reset, (nk, nv), mapSubtract((nk, nv), (pnk, pnv))) AS nt FROM (\n" +
		"      SELECT TimeUnix, tmp, ms" + p.gNameList + ", z, pz, pk, pv, nk, nv, ppk, ppv, pnk, pnv,\n" +
		"             isNaN(pc) AS is_first, (NOT isNaN(pc)) AND (StartTimeUnix != ps OR c < pc) AS is_reset FROM (\n" +
		"        SELECT TimeUnix, StartTimeUnix, tmp, ms, sid" + p.gNameList + ", c, z, pk, pv, nk, nv,\n" +
		"               lagInFrame(c, 1, nan) OVER w AS pc, lagInFrame(z, 1, nan) OVER w AS pz, lagInFrame(StartTimeUnix, 1, toDateTime(0)) OVER w AS ps,\n" +
		"               lagInFrame(pk, 1, pk) OVER w AS ppk, lagInFrame(pv, 1, pv) OVER w AS ppv, lagInFrame(nk, 1, nk) OVER w AS pnk, lagInFrame(nv, 1, nv) OVER w AS pnv FROM (\n" +
		"          SELECT TimeUnix, StartTimeUnix, tmp, ms, sid" + p.gNameList + ", c, z, pm.1 AS pk, pm.2 AS pv, nm.1 AS nk, nm.2 AS nv FROM (\n" +
		// every point re-expressed at the series' minimum scale: bucket index >> (scale - ms), merging counts
		"            SELECT TimeUnix, StartTimeUnix, tmp, ms, sid" + p.gNameList + ", c, z,\n" +
		"                   " + down("po", "pb") + " AS pm,\n" +
		"                   " + down("ngo", "ngb") + " AS nm FROM (\n" +
		"              SELECT TimeUnix, StartTimeUnix, AggregationTemporality AS tmp, toFloat64(Count) AS c, toFloat64(ZeroCount) AS z,\n" +
		"                     min(Scale) OVER w AS ms, toUInt8(Scale - min(Scale) OVER w) AS d, " + p.sid + " AS sid" + p.gSelList + ",\n" +
		"                     PositiveOffset AS po, PositiveBucketCounts AS pb, NegativeOffset AS ngo, NegativeBucketCounts AS ngb\n" +
		"              FROM " + p.from + "\n              " + where(p.conds) + "\n" +
		"              WINDOW w AS (PARTITION BY " + p.sid + " ORDER BY TimeUnix ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)\n" +
		"            )\n" +
		"          )\n" +
		"        )\n" +
		"        " + w + "\n" +
		"      )\n" +
		"    )\n" +
		"    WHERE (tmp = 1 OR NOT is_first) AND " + p.startCond + "\n" +
		"  ) GROUP BY bucket" + p.gNameList + ", ms\n" +
		") ORDER BY bucket" + p.gNameList + ", scale LIMIT " + strconv.Itoa(MaxMetricRows)
}

// summaryQuantileSQL compiles a stored quantile of a summary. Columns: bucket,
// labels, value, series_total, nseries (series answering in the group),
// nmissing (those that do not store the quantile). The value of a series in a
// bucket is its last sample's.
func (q MetricQuery) summaryQuantileSQL(p sqlParts, level float64) string {
	idx := "arrayFirstIndex(x -> abs(x - " + p.b.f64(level) + ") < 0.000000001, `ValueAtQuantiles.Quantile`)"
	return "SELECT bucket, " + p.labels + " AS labels, toFloat64(any(v)) AS value, " + p.total + " AS series_total, " +
		"toUInt64(count()) AS nseries, toUInt64(countIf(idx = 0)) AS nmissing FROM (\n" +
		"  SELECT " + p.bucket + " AS bucket" + p.gSelList + ", " + p.sid + " AS sid,\n" +
		"         argMax(arrayElement(`ValueAtQuantiles.Value`, " + idx + "), TimeUnix) AS v, argMax(" + idx + ", TimeUnix) AS idx\n" +
		"  FROM " + p.from + "\n  " + where(p.conds) + "\n  GROUP BY bucket" + p.gNameList + ", sid\n" +
		") GROUP BY bucket" + p.gNameList + " ORDER BY bucket" + p.gNameList + " LIMIT " + strconv.Itoa(MaxMetricRows)
}

// expRow is one row of expQuantileSQL.
type expRow struct {
	scale  int32
	zero   float64
	pk, nk []int64
	pv, nv []float64
}

// expMerged is the rows of one (bucket, group) at their common scale.
type expMerged struct {
	scale    int32
	zero     float64
	pos, neg map[int64]float64
}

// mergeExpRows folds rows onto the smallest scale among them: a bucket index
// at scale s is index >> (s - min) at the minimum, so finer buckets pool into
// their parent. The arithmetic shift rounds toward negative infinity, which is
// what keeps a negative index inside the bucket that contains it.
func mergeExpRows(rows []expRow) expMerged {
	m := expMerged{pos: map[int64]float64{}, neg: map[int64]float64{}}
	if len(rows) == 0 {
		return m
	}
	m.scale = rows[0].scale
	for _, r := range rows {
		m.scale = min(m.scale, r.scale)
	}
	for _, r := range rows {
		d := uint(r.scale - m.scale)
		m.zero += r.zero
		for i, k := range r.pk {
			if i < len(r.pv) {
				m.pos[k>>d] += r.pv[i]
			}
		}
		for i, k := range r.nk {
			if i < len(r.nv) {
				m.neg[k>>d] += r.nv[i]
			}
		}
	}
	return m
}

// bounds converts the merged buckets to the explicit form HistogramQuantile
// reads (len(counts) == len(bounds)+1). Only populated buckets, and the empty
// span between two that are not adjacent, produce bounds, so a sparse histogram
// stays small. Negative buckets mirror the positive; the zero bucket is the
// degenerate interval [0, 0].
func (e expMerged) bounds() (bounds, counts []float64) {
	type span struct{ lo, hi, n float64 }
	base := math.Exp2(math.Exp2(float64(-e.scale)))
	edge := func(i int64) float64 { return math.Exp2(float64(i) * math.Exp2(float64(-e.scale))) }
	_ = base

	var spans []span
	idx := func(m map[int64]float64) []int64 {
		ks := make([]int64, 0, len(m))
		for k, n := range m {
			if n > 0 {
				ks = append(ks, k)
			}
		}
		sort.Slice(ks, func(i, j int) bool { return ks[i] < ks[j] })
		return ks
	}
	neg := idx(e.neg)
	for i := len(neg) - 1; i >= 0; i-- {
		k := neg[i]
		spans = append(spans, span{-edge(k + 1), -edge(k), e.neg[k]})
	}
	if e.zero > 0 {
		spans = append(spans, span{0, 0, e.zero})
	}
	for _, k := range idx(e.pos) {
		spans = append(spans, span{edge(k), edge(k + 1), e.pos[k]})
	}
	if len(spans) == 0 {
		return nil, nil
	}
	for _, s := range spans {
		if len(bounds) == 0 {
			bounds, counts = []float64{s.lo}, []float64{0}
		} else if s.lo > bounds[len(bounds)-1] {
			bounds, counts = append(bounds, s.lo), append(counts, 0)
		}
		bounds, counts = append(bounds, s.hi), append(counts, s.n)
	}
	return bounds, append(counts, 0)
}

// summaryQuantileError is the refusal for a stored quantile that cannot be
// answered.
func summaryQuantileError(name string, level float64, nseries, nmissing uint64) error {
	if nmissing > 0 {
		return invalid("summary %s does not store the %s quantile in every series; read one it stores or use a histogram",
			name, strings.TrimRight(strconv.FormatFloat(level, 'f', 6, 64), "0"))
	}
	if nseries > 1 {
		return invalid("summary quantiles cannot be merged across series; group by the series' attributes or use a histogram")
	}
	return nil
}
