// Copyright (c) 2025 Reliant Labs
package toolexec

// This lives in toolexec, next to the router, because it is the worker-side end
// of the daemon relay: it needs the router's stream type and the relay's typed
// errors, and nothing in llm/ should know NATS exists. llm only sees an
// http.RoundTripper (llm.DriverOptions.Transport).

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// LocalModelRelay is the part of DaemonRouter the transport needs. Declared
// here, at the consumer.
type LocalModelRelay interface {
	OpenLocalModelHTTP(ctx context.Context, userID, daemonID string, req *reliantv1.LocalModelHTTPRequest) (*LocalModelHTTPStream, error)
}

type localModelTransport struct {
	relay      LocalModelRelay
	userID     string
	daemonID   string
	endpointID string
}

// NewLocalModelTransport returns a RoundTripper that sends every request to the
// local model endpoint endpointID on daemonID, through the daemon relay. The
// request URL's host is ignored (the daemon dials the endpoint it knows by id);
// only path and query are relayed. Streaming responses (SSE) flow through the
// response Body as the daemon produces them.
func NewLocalModelTransport(relay LocalModelRelay, userID, daemonID, endpointID string) http.RoundTripper {
	return &localModelTransport{relay: relay, userID: userID, daemonID: daemonID, endpointID: endpointID}
}

func (t *localModelTransport) RoundTrip(httpReq *http.Request) (*http.Response, error) {
	var body []byte
	if httpReq.Body != nil {
		var err error
		body, err = io.ReadAll(httpReq.Body)
		_ = httpReq.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading local model request body: %w", err)
		}
	}

	headers := make(map[string]string, len(httpReq.Header))
	for k, v := range httpReq.Header {
		// The daemon talks to a server it knows by id: client credentials and
		// hop-by-hop framing are not for it.
		switch strings.ToLower(k) {
		case "authorization", "content-length", "host", "connection", "accept-encoding":
			continue
		}
		headers[k] = strings.Join(v, ", ")
	}

	req := &reliantv1.LocalModelHTTPRequest{
		EndpointId: t.endpointID,
		Method:     httpReq.Method,
		Path:       relayPath(httpReq.URL),
		Headers:    headers,
		Body:       body,
	}

	stream, err := t.relay.OpenLocalModelHTTP(httpReq.Context(), t.userID, t.daemonID, req)
	if err != nil {
		return nil, err
	}

	head, err := stream.Next()
	if err != nil {
		stream.Close()
		return nil, err
	}
	if head.GetError() != "" && head.GetStatus() == 0 {
		stream.Close()
		return nil, &LocalModelRelayError{DaemonID: t.daemonID, Message: head.GetError()}
	}

	respHeader := make(http.Header, len(head.GetHeaders()))
	for k, v := range head.GetHeaders() {
		respHeader.Set(k, v)
	}
	// The relay hands us a decoded body; a stale Content-Length/Encoding would
	// make the HTTP client mis-frame it.
	respHeader.Del("Content-Encoding")
	respHeader.Del("Content-Length")

	pr, pw := io.Pipe()
	resp := &http.Response{
		Status:        fmt.Sprintf("%d %s", head.GetStatus(), http.StatusText(int(head.GetStatus()))),
		StatusCode:    int(head.GetStatus()),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        respHeader,
		Body:          &relayBody{pr: pr, stream: stream},
		ContentLength: -1,
		Request:       httpReq,
	}

	go t.pump(stream, head, pw)
	return resp, nil
}

// pump copies the remaining chunks into the pipe. A failure after the head
// surfaces as a read error on the response body.
func (t *localModelTransport) pump(stream *LocalModelHTTPStream, head *reliantv1.LocalModelHTTPChunk, pw *io.PipeWriter) {
	chunk := head
	for {
		if len(chunk.GetData()) > 0 {
			if _, err := pw.Write(chunk.GetData()); err != nil {
				return // reader closed; relayBody.Close cancels the stream
			}
		}
		if chunk.GetError() != "" {
			_ = pw.CloseWithError(&LocalModelRelayError{DaemonID: t.daemonID, Message: chunk.GetError()})
			return
		}
		if chunk.GetDone() {
			_ = pw.Close()
			return
		}
		next, err := stream.Next()
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		chunk = next
	}
}

// relayBody closes the relay stream (cancelling the daemon-side request if it
// is still running) when the HTTP client closes the response body.
type relayBody struct {
	pr     *io.PipeReader
	stream *LocalModelHTTPStream
}

func (b *relayBody) Read(p []byte) (int, error) { return b.pr.Read(p) }

func (b *relayBody) Close() error {
	b.stream.Close()
	return b.pr.Close()
}

// relayPath is the request path plus query exactly as the HTTP client built
// it. The daemon appends it to the endpoint's own base URL, so callers build
// the client with a placeholder base URL that has no path (e.g.
// "http://local-model.invalid/") and the openai client yields
// "/chat/completions".
func relayPath(u *url.URL) string {
	if u == nil {
		return "/"
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return path
}
