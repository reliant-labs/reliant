// Copyright (c) 2025 Reliant Labs

package grpc

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/klauspost/compress/gzip"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const streamTestProcedure = "/test.v1.StreamService/Stream"

// snapshotLike is a message shaped like the chat snapshot: JSON text, which is
// what the stream's bytes mostly are.
var snapshotLike = strings.Repeat(`{"update_type":"thread","entity_id":"thread:0f3c","data_json":"{\"status\":\"running\"}"},`, 2000)

// streamServer serves one Connect server stream behind compressBrowserStreams.
// It sends the snapshot-like message, then blocks until release is closed
// before sending the second, so a test can prove the first arrived while the
// stream was still open.
func streamServer(t *testing.T, release <-chan struct{}) *httptest.Server {
	t.Helper()
	handler := connect.NewServerStreamHandler(streamTestProcedure,
		func(ctx context.Context, _ *connect.Request[wrapperspb.StringValue], stream *connect.ServerStream[wrapperspb.StringValue]) error {
			if err := stream.Send(wrapperspb.String(snapshotLike)); err != nil {
				return err
			}
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			return stream.Send(wrapperspb.String("second"))
		})
	srv := httptest.NewServer(compressBrowserStreams(handler))
	t.Cleanup(srv.Close)
	return srv
}

// openStream starts the stream the way connect-web does: a POST of one
// enveloped request with no Connect-Accept-Encoding. The transport leaves
// decompression to the test, so the test sees what went over the wire.
func openStream(t *testing.T, url string, header http.Header) *http.Response {
	t.Helper()
	msg, err := proto.Marshal(wrapperspb.String("hi"))
	require.NoError(t, err)
	body := make([]byte, 5+len(msg))
	binary.BigEndian.PutUint32(body[1:5], uint32(len(msg)))
	copy(body[5:], msg)

	req, err := http.NewRequest(http.MethodPost, url+streamTestProcedure, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/connect+proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return resp
}

// readEnvelope reads one Connect envelope and returns its flags and payload.
func readEnvelope(t *testing.T, r io.Reader) (byte, []byte) {
	t.Helper()
	var prefix [5]byte
	_, err := io.ReadFull(r, prefix[:])
	require.NoError(t, err)
	payload := make([]byte, binary.BigEndian.Uint32(prefix[1:]))
	_, err = io.ReadFull(r, payload)
	require.NoError(t, err)
	return prefix[0], payload
}

// countingReader counts the bytes that crossed the wire.
type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// A browser's stream is gzipped, and each message is readable as soon as it
// is sent: the first arrives, decoded, while the handler is still blocked
// before the second.
func TestCompressBrowserStreams_GzipsAndFlushesEachMessage(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := streamServer(t, release)

	resp := openStream(t, srv.URL, http.Header{"Accept-Encoding": {"gzip, deflate, br"}})
	require.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))

	wire := &countingReader{r: resp.Body}
	zr, err := gzip.NewReader(wire)
	require.NoError(t, err)

	flags, payload := readEnvelope(t, zr)
	require.Zero(t, flags&0x01, "the envelope itself must not be compressed: connect-web rejects that")
	var got wrapperspb.StringValue
	require.NoError(t, proto.Unmarshal(payload, &got))
	require.Equal(t, snapshotLike, got.GetValue(), "the first message must arrive whole before the stream continues")

	wireForFirst := wire.n
	require.Less(t, wireForFirst*5, len(payload),
		"JSON-shaped snapshot must shrink at least 5x on the wire: %d bytes for a %d byte message", wireForFirst, len(payload))
	t.Logf("first message: %d bytes, %d on the wire", len(payload), wireForFirst)

	close(release)
	_, payload = readEnvelope(t, zr)
	require.NoError(t, proto.Unmarshal(payload, &got))
	require.Equal(t, "second", got.GetValue())

	flags, _ = readEnvelope(t, zr)
	require.Equal(t, byte(0x02), flags, "the stream must end with Connect's end-of-stream envelope")
}

// A client that did not offer gzip gets the plain stream.
func TestCompressBrowserStreams_PlainWithoutAcceptEncoding(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	close(release)
	srv := streamServer(t, release)

	resp := openStream(t, srv.URL, nil)
	require.Empty(t, resp.Header.Get("Content-Encoding"))
	_, payload := readEnvelope(t, resp.Body)
	var got wrapperspb.StringValue
	require.NoError(t, proto.Unmarshal(payload, &got))
	require.Equal(t, snapshotLike, got.GetValue())
}

// A Connect client that negotiates envelope compression gets that, not a
// second layer of gzip around it.
func TestCompressBrowserStreams_LeavesEnvelopeCompressionToConnect(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	close(release)
	srv := streamServer(t, release)

	resp := openStream(t, srv.URL, http.Header{
		"Accept-Encoding":         {"gzip"},
		"Connect-Accept-Encoding": {"gzip"},
	})
	require.Empty(t, resp.Header.Get("Content-Encoding"))
	require.Equal(t, "gzip", resp.Header.Get("Connect-Content-Encoding"))
}

// A unary response is compressed by connect-go itself, from the standard
// Accept-Encoding header; the middleware must leave it exactly as connect-go
// wrote it.
func TestCompressBrowserStreams_LeavesUnaryToConnect(t *testing.T) {
	t.Parallel()
	handler := connect.NewUnaryHandler("/test.v1.StreamService/Unary",
		func(context.Context, *connect.Request[wrapperspb.StringValue]) (*connect.Response[wrapperspb.StringValue], error) {
			return connect.NewResponse(wrapperspb.String(snapshotLike)), nil
		})
	srv := httptest.NewServer(compressBrowserStreams(handler))
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/test.v1.StreamService/Unary", strings.NewReader(`"hi"`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))

	zr, err := gzip.NewReader(resp.Body)
	require.NoError(t, err)
	body, err := io.ReadAll(zr)
	require.NoError(t, err)
	require.Contains(t, string(body), "update_type", "a single gzip layer must decode to the JSON body")
}
