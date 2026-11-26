package obsquery

// Trace sampling and the weight of a stored span.
//
// The hosted gateway tail-samples traces (errors always, slow traces, 10% of the
// rest). A stored span therefore stands for MORE than one real span, and every
// count, ratio and percentile over spans must be weighted by how many it
// stands for, or error ratios are inflated about tenfold and request counts
// deflated.
//
// The weight is carried by the OpenTelemetry probability-sampling threshold the
// tail_sampling processor writes into every kept span's W3C tracestate when its
// usetracestate feature gate is on (deploy/kcl/lib/observability/gateway.k):
//
//	tracestate: ot=th:e6666666666666   P(kept) = 1 - 0xe6666666666666/2^56 = 0.1   weight 10
//	tracestate: ot=th:0                P(kept) = 1                                 weight 1
//
// weight = 1 / P(kept). A span with no (or an unparseable) threshold was not
// sampled by us, so it weighs 1: browser spans, which skip the sampler, and
// anything stored before the gate was turned on. The weight is capped so one
// corrupt value cannot make a count meaningless.
const (
	// maxSpanWeight caps a span's weight (P(kept) of 1e-6).
	maxSpanWeight = "1000000.0"

	// spanThreshold is the th value of the tracestate's ot entry, '' when it
	// has none. The regex is the definition, but at ~0.75µs a span it was most
	// of the cost of every weighted statement (a 6h sum over prod's ~10M spans:
	// 8.1s against 1.3s). Nearly every stored tracestate is the sampler's own
	// "ot=th:<hex>" (or empty), and for that shape the value is the text after
	// "th:" when it is all lowercase hex, which string functions answer without
	// the regex; any other shape still goes through it.
	// TestSpanThresholdFastPathMatchesTheRegex holds the two to the same value.
	spanThreshold = "if(startsWith(TraceState, 'ot=th:') AND position(TraceState, ',') = 0 AND position(TraceState, ';') = 0, " +
		"if(length(TraceState) > 6 AND trim(BOTH '0123456789abcdef' FROM substring(TraceState, 7)) = '', substring(TraceState, 7), ''), " +
		"if(TraceState = '', '', " + spanThresholdRegex + "))"
	// spanThresholdRegex is the threshold by its definition: the hex after th:
	// in the ot entry's semicolon-separated values.
	spanThresholdRegex = "extract(TraceState, '(?:^|,)ot=(?:[^;,]*;)*th:([0-9a-f]+)(?:[;,]|$)')"
	// 72057594037927936 = 2^56, the threshold space (14 hex digits). unhex needs
	// whole bytes and the 8 bytes are big-endian: left-pad one zero byte, right-pad
	// the digits to 14, reverse for the little-endian reinterpret.
	// Rounded to 6 places: th:e6666666666666 is exactly 0.9 of the space up to
	// float noise, and 10.000000000000002 would leak into every count.
	spanWeightExpr = "if(" + spanThreshold + " = '', 1.0, round(least(" + maxSpanWeight + ", 1 / greatest(1 - " +
		"reinterpretAsUInt64(reverse(unhex(concat('00', rightPad(left(" + spanThreshold + ", 14), 14, '0'))))) / 72057594037927936, 0.000001)), 6))"

	// spanWeightInt is the weight as the UInt64 the weighted-quantile functions
	// require.
	spanWeightInt = "toUInt64(round(" + spanWeightExpr + "))"

	// spanWeightRegexExpr and spanWeightIntRegex are the weight read through
	// spanThresholdRegex alone: the values of the deprecated SpanWeightSQL and
	// SpanWeightIntSQL, which a public constant must keep.
	spanWeightRegexExpr = "if(" + spanThresholdRegex + " = '', 1.0, round(least(" + maxSpanWeight + ", 1 / greatest(1 - " +
		"reinterpretAsUInt64(reverse(unhex(concat('00', rightPad(left(" + spanThresholdRegex + ", 14), 14, '0'))))) / 72057594037927936, 0.000001)), 6))"
	spanWeightIntRegex = "toUInt64(round(" + spanWeightRegexExpr + "))"
)
