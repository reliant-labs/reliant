package kubedial

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	streamhttp "k8s.io/streaming/pkg/httpstream"
)

// errorDrain is how long a stream that just ended waits for the pod's error
// stream before it reports a bare EOF. A variable so a test can shrink it.
var errorDrain = 500 * time.Millisecond

// spdyDialer is the real PodDialer: the Kubernetes port-forward protocol over
// SPDY tunnelled in a websocket, with plain SPDY as the fallback for an API
// server or proxy that refuses the upgrade (what kubectl does).
type spdyDialer struct {
	cfg *rest.Config
	cs  kubernetes.Interface
}

func (d *spdyDialer) DialPod(ctx context.Context, namespace, pod string, port int) (io.ReadWriteCloser, error) {
	u := d.cs.CoreV1().RESTClient().Post().Namespace(namespace).Resource("pods").Name(pod).SubResource("portforward").URL()
	rt, upgrader, err := spdy.RoundTripperFor(d.cfg)
	if err != nil {
		return nil, err
	}
	dialer := spdy.NewDialerForStreaming(upgrader, &http.Client{Transport: rt}, http.MethodPost, u)
	if ws, err := portforward.NewSPDYOverWebsocketDialerForStreaming(u, d.cfg); err == nil {
		dialer = portforward.NewFallbackDialerForStreaming(ws, dialer, func(err error) bool {
			return streamhttp.IsUpgradeFailure(err) || streamhttp.IsHTTPSProxyError(err)
		})
	}
	return dialStreams(ctx, dialer, port)
}

// dialStreams negotiates the port-forward protocol on dialer and opens the
// error and data streams for port. Dialer.Dial takes no context, so the dial
// runs in a goroutine the context can abandon; a connection that arrives after
// that is closed.
func dialStreams(ctx context.Context, dialer streamhttp.Dialer, port int) (io.ReadWriteCloser, error) {
	type result struct {
		conn     streamhttp.Connection
		protocol string
		err      error
	}
	done := make(chan result, 1)
	go func() {
		conn, protocol, err := dialer.Dial(portforward.PortForwardProtocolV1Name)
		done <- result{conn, protocol, err}
	}()
	var r result
	select {
	case r = <-done:
	case <-ctx.Done():
		go func() {
			if late := <-done; late.conn != nil {
				_ = late.conn.Close()
			}
		}()
		return nil, ctx.Err()
	}
	if r.err != nil {
		return nil, r.err
	}
	if r.protocol != portforward.PortForwardProtocolV1Name {
		_ = r.conn.Close()
		return nil, fmt.Errorf("unable to negotiate protocol: client supports %q, server returned %q", portforward.PortForwardProtocolV1Name, r.protocol)
	}

	// One forward per connection, so the request id is always 0: it only
	// pairs this error stream with this data stream.
	headers := http.Header{}
	headers.Set(corev1.StreamType, corev1.StreamTypeError)
	headers.Set(corev1.PortHeader, strconv.Itoa(port))
	headers.Set(corev1.PortForwardRequestIDHeader, "0")
	errStream, err := r.conn.CreateStream(headers)
	if err != nil {
		_ = r.conn.Close()
		return nil, fmt.Errorf("create error stream: %w", err)
	}
	// Nothing is written to the error stream.
	_ = errStream.Close()
	headers.Set(corev1.StreamType, corev1.StreamTypeData)
	dataStream, err := r.conn.CreateStream(headers)
	if err != nil {
		_ = r.conn.Close()
		return nil, fmt.Errorf("create data stream: %w", err)
	}
	s := &forwardStream{conn: r.conn, data: dataStream, errMsg: make(chan string, 1)}
	go s.watchErrors(errStream)
	return s, nil
}

// forwardStream is the data stream of one forward. The kubelet reports why a
// forward failed (the pod refused the port, nothing listens) on the error
// stream and then ends the data stream, which alone looks like a bare EOF.
type forwardStream struct {
	conn streamhttp.Connection
	data streamhttp.Stream
	// errMsg receives the error stream's content once it ends: empty when
	// nothing went wrong.
	errMsg    chan string
	closeOnce sync.Once
}

func (s *forwardStream) watchErrors(errStream streamhttp.Stream) {
	msg, err := io.ReadAll(errStream)
	text := strings.TrimSpace(string(msg))
	if err != nil && text == "" {
		text = err.Error()
	}
	s.errMsg <- text
	if text != "" {
		// The forward is dead; end the data stream so a blocked Read wakes.
		_ = s.data.Reset()
	}
}

func (s *forwardStream) Read(b []byte) (int, error) {
	n, err := s.data.Read(b)
	if err != nil {
		if remote := s.remoteError(); remote != nil {
			return n, remote
		}
	}
	return n, err
}

// remoteError is the pod's reason for ending the forward, if it gave one in
// time.
func (s *forwardStream) remoteError() error {
	select {
	case msg := <-s.errMsg:
		s.errMsg <- msg
		if msg != "" {
			return errors.New(msg)
		}
	case <-time.After(errorDrain):
	}
	return nil
}

func (s *forwardStream) Write(b []byte) (int, error) { return s.data.Write(b) }

func (s *forwardStream) Close() error {
	s.closeOnce.Do(func() {
		_ = s.data.Reset()
		s.conn.RemoveStreams(s.data)
		_ = s.conn.Close()
	})
	return nil
}
