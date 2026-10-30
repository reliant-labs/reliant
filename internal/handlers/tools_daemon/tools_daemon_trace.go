// Copyright (c) 2025 Reliant Labs
package tools_daemon

// Tracing for the gateway's half of a daemon command.
//
// The daemon exports no spans (internal/observability.InstallPropagation), so
// the gateway span IS the daemon command in a trace: it names the daemon, where
// it dialed from, and splits the round trip into the daemon's own handler time
// (DaemonCommandResponse.handler_elapsed_ms) and everything else — the stream
// in both directions plus the daemon's send backlog. Before it,
// `nats.handle.daemon.command` had no children and the whole round trip was one
// opaque bar.

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

const daemonTracerName = "github.com/reliant-labs/reliant/internal/handlers/tools_daemon"

// Span attribute keys. client.address and geo.* are OpenTelemetry semantic
// conventions; the rest are ours.
const (
	attrDaemonID          = "reliant.daemon.id"
	attrDaemonType        = "reliant.daemon.type"
	attrDaemonPlatform    = "reliant.daemon.platform"
	attrDaemonAttach      = "reliant.daemon.attach_source"
	attrCommandType       = "reliant.daemon.command.type"
	attrCommandRequestID  = "reliant.daemon.command.request_id"
	attrCommandTimeoutMs  = "reliant.daemon.command.timeout_ms"
	attrCommandOutcome    = "reliant.daemon.command.outcome"
	attrCommandErrorCode  = "reliant.daemon.command.error_code"
	attrHandlerElapsedMs  = "reliant.daemon.command.handler_elapsed_ms"
	attrTransitElapsedMs  = "reliant.daemon.command.transit_ms"
	attrToolName          = "reliant.daemon.tool.name"
	attrClientAddress     = "client.address"
	attrNetworkPeerAddr   = "network.peer.address"
	attrGeoCountryISOCode = "geo.country.iso_code"
	attrGeoRegionISOCode  = "geo.region.iso_code"
	attrGeoLocalityName   = "geo.locality.name"
)

// Geo request headers. Nothing in this stack resolves an IP to a place, so the
// gateway reads the answer the load balancer in front of it can be told to
// write: a GCP Application Load Balancer's custom request headers
// ({client_region}, {client_region_subdivision}, {client_city}). Until the
// gateway's backend is configured to send them, they are absent and so are the
// geo.* attributes. Attribution only — never an input to a security decision.
const (
	headerClientRegion            = "X-Client-Region"
	headerClientRegionSubdivision = "X-Client-Region-Subdivision"
	headerClientCity              = "X-Client-City"
)

// daemonNetwork is where an inbound daemon stream came from.
type daemonNetwork struct {
	// clientAddress is the daemon's own address: the first X-Forwarded-For
	// hop when the stream came through a proxy, else the peer.
	clientAddress string
	// peerAddress is the immediate TCP peer (usually the load balancer).
	peerAddress string
	country     string
	region      string
	city        string
}

// daemonNetworkOf reads an inbound stream's origin from its request headers
// and transport peer. X-Forwarded-For is client-supplied in its leftmost hops,
// so the address is for attribution in traces, never for a security decision.
func daemonNetworkOf(header http.Header, peerAddr string) daemonNetwork {
	n := daemonNetwork{peerAddress: hostOnly(peerAddr)}
	if xff := header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		n.clientAddress = hostOnly(strings.TrimSpace(first))
	}
	if n.clientAddress == "" {
		if xri := strings.TrimSpace(header.Get("X-Real-Ip")); xri != "" {
			n.clientAddress = hostOnly(xri)
		}
	}
	if n.clientAddress == "" {
		n.clientAddress = n.peerAddress
	}
	n.country = geoHeader(header, headerClientRegion)
	n.region = geoHeader(header, headerClientRegionSubdivision)
	n.city = geoHeader(header, headerClientCity)
	return n
}

// geoHeader returns a geo header's value, or "" for an absent one or for the
// load balancer's own placeholders when it could not resolve the client.
func geoHeader(header http.Header, name string) string {
	v := strings.TrimSpace(header.Get(name))
	switch strings.ToLower(v) {
	case "", "unknown", "zz":
		return ""
	}
	if len(v) > 64 {
		v = v[:64]
	}
	return v
}

// hostOnly strips a port from addr, leaving IPv6 brackets out.
func hostOnly(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return strings.Trim(addr, "[]")
}

// daemonConnAttributes names a daemon connection on a span: which daemon, of
// what kind, and where it is.
func daemonConnAttributes(conn *daemonConnection) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(attrDaemonID, conn.daemonID),
		attribute.String(attrDaemonAttach, string(conn.attachSource)),
	}
	if conn.daemonType != "" {
		attrs = append(attrs, attribute.String(attrDaemonType, conn.daemonType))
	}
	if conn.platform != "" {
		attrs = append(attrs, attribute.String(attrDaemonPlatform, conn.platform))
	}
	n := conn.network
	client := n.clientAddress
	if client == "" && conn.podIP != nil {
		// Outbound: the gateway dialed the daemon's pod.
		client = *conn.podIP
	}
	if client != "" {
		attrs = append(attrs, attribute.String(attrClientAddress, client))
	}
	if n.peerAddress != "" && n.peerAddress != client {
		attrs = append(attrs, attribute.String(attrNetworkPeerAddr, n.peerAddress))
	}
	if n.country != "" {
		attrs = append(attrs, attribute.String(attrGeoCountryISOCode, n.country))
	}
	if n.region != "" {
		attrs = append(attrs, attribute.String(attrGeoRegionISOCode, n.region))
	}
	if n.city != "" {
		attrs = append(attrs, attribute.String(attrGeoLocalityName, n.city))
	}
	return attrs
}

// startDaemonCommandSpan opens the gateway's CLIENT span for one command sent
// to conn. Its context is what the command's trace_context carries to the
// daemon.
func startDaemonCommandSpan(ctx context.Context, conn *daemonConnection, req *reliantv1.DaemonCommandRequest) (context.Context, trace.Span) {
	attrs := append(daemonConnAttributes(conn),
		attribute.String(attrCommandType, req.GetCommandType()),
		attribute.String(attrCommandRequestID, req.GetRequestId()),
	)
	if req.GetTimeoutMs() > 0 {
		attrs = append(attrs, attribute.Int64(attrCommandTimeoutMs, int64(req.GetTimeoutMs())))
	}
	return otel.Tracer(daemonTracerName).Start(ctx, "gateway.daemon.command "+req.GetCommandType(),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...))
}

// endDaemonCommandSpan records how the command ended and ends span. A daemon
// that answered splits the round trip: its handler time, and the rest
// (transit_ms) — the stream both ways and its send backlog.
func endDaemonCommandSpan(span trace.Span, resp *reliantv1.DaemonCommandResponse, err error, roundTrip time.Duration) {
	defer span.End()
	switch {
	case err != nil:
		span.SetAttributes(attribute.String(attrCommandOutcome, "transport_error"))
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return
	case resp == nil:
		span.SetAttributes(attribute.String(attrCommandOutcome, "no_response"))
		return
	}
	span.AddEvent("daemon.command.response")
	handler := time.Duration(resp.GetHandlerElapsedMs()) * time.Millisecond
	attrs := []attribute.KeyValue{attribute.Int64(attrHandlerElapsedMs, resp.GetHandlerElapsedMs())}
	if resp.GetHandlerElapsedMs() > 0 && roundTrip >= handler {
		attrs = append(attrs, attribute.Int64(attrTransitElapsedMs, (roundTrip-handler).Milliseconds()))
	}
	if resp.GetSuccess() {
		attrs = append(attrs, attribute.String(attrCommandOutcome, "ok"))
		span.SetAttributes(attrs...)
		return
	}
	attrs = append(attrs, attribute.String(attrCommandOutcome, "daemon_error"))
	span.SetAttributes(attrs...)
	// The daemon's own message, bounded: it is the error the caller sees.
	msg := resp.GetErrorMessage()
	if len(msg) > 512 {
		msg = msg[:512]
	}
	span.SetStatus(codes.Error, msg)
}

// startDaemonToolSpan is startDaemonCommandSpan for a synchronous tool call
// (tools.request.sync): the gateway's CLIENT span for the daemon's execution.
func startDaemonToolSpan(ctx context.Context, conn *daemonConnection, req *toolexec.ToolExecutionRequest) (context.Context, trace.Span) {
	attrs := append(daemonConnAttributes(conn),
		attribute.String(attrToolName, req.ToolName),
		attribute.String(attrCommandRequestID, req.RequestID),
	)
	if req.TimeoutMs > 0 {
		attrs = append(attrs, attribute.Int64(attrCommandTimeoutMs, int64(req.TimeoutMs)))
	}
	return otel.Tracer(daemonTracerName).Start(ctx, "gateway.daemon.tool "+req.ToolName,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...))
}

// endDaemonToolSpan records how a synchronous tool call ended and ends span.
// A tool reporting an error result is the tool's outcome, not a failed round
// trip: it is an attribute, and only a transport failure marks the span.
func endDaemonToolSpan(span trace.Span, resp *toolexec.ToolExecutionResponse, err error) {
	defer span.End()
	switch {
	case err != nil:
		span.SetAttributes(attribute.String(attrCommandOutcome, "transport_error"))
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	case resp == nil:
		span.SetAttributes(attribute.String(attrCommandOutcome, "no_response"))
	case resp.Backgrounded:
		span.SetAttributes(attribute.String(attrCommandOutcome, "backgrounded"))
	case resp.IsError || !resp.Success:
		span.SetAttributes(attribute.String(attrCommandOutcome, "tool_error"))
		if resp.ErrorCode != "" {
			span.SetAttributes(attribute.String(attrCommandErrorCode, resp.ErrorCode))
		}
	default:
		span.SetAttributes(attribute.String(attrCommandOutcome, "ok"))
	}
}
