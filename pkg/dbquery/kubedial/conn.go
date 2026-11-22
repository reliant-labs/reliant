package kubedial

import (
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// streamAddr is the net.Addr of a tunnelled connection: there is no socket, so
// the address names the pod the stream ends in.
type streamAddr string

func (streamAddr) Network() string  { return "kubedial" }
func (a streamAddr) String() string { return string(a) }

// readChunk is how much one pump read asks the stream for.
const readChunk = 32 << 10

// conn is a net.Conn over one port-forward stream. A Kubernetes stream is an
// io.ReadWriteCloser with no deadlines, but pgx cancels a query by setting a
// deadline in the past on the connection, so the deadlines are implemented
// here: reads come from a pump goroutine through a channel a deadline can
// abandon, and a write with a deadline runs in a goroutine a deadline can
// abandon. A timed-out connection is not reusable (a Postgres connection
// whose read or write was abandoned mid-message cannot be), so a timeout also
// poisons later writes.
type conn struct {
	s           io.ReadWriteCloser
	local       streamAddr
	remote      streamAddr
	reads       chan readResult
	rest        []byte
	readMu      sync.Mutex
	writeMu     sync.Mutex
	writeBroken bool
	readDL      deadline
	writeDL     deadline
	closeOnce   sync.Once
	closed      chan struct{}
	closeErr    error
}

type readResult struct {
	b   []byte
	err error
}

func newConn(s io.ReadWriteCloser, remote string) *conn {
	c := &conn{
		s:       s,
		local:   "kubedial",
		remote:  streamAddr(remote),
		reads:   make(chan readResult),
		closed:  make(chan struct{}),
		readDL:  makeDeadline(),
		writeDL: makeDeadline(),
	}
	go c.pump()
	return c
}

// pump moves the stream's bytes to Read. It ends with the stream: a read error
// is delivered once, then every later Read sees it again through c.closed.
func (c *conn) pump() {
	for {
		buf := make([]byte, readChunk)
		n, err := c.s.Read(buf)
		if n > 0 {
			select {
			case c.reads <- readResult{b: buf[:n]}:
			case <-c.closed:
				return
			}
		}
		if err != nil {
			select {
			case c.reads <- readResult{err: err}:
			case <-c.closed:
			}
			return
		}
	}
}

func (c *conn) Read(b []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if len(b) == 0 {
		return 0, nil
	}
	if len(c.rest) > 0 {
		n := copy(b, c.rest)
		c.rest = c.rest[n:]
		return n, nil
	}
	select {
	case <-c.readDL.wait():
		return 0, os.ErrDeadlineExceeded
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	select {
	case r := <-c.reads:
		if r.err != nil {
			return 0, r.err
		}
		n := copy(b, r.b)
		c.rest = r.b[n:]
		return n, nil
	case <-c.readDL.wait():
		return 0, os.ErrDeadlineExceeded
	case <-c.closed:
		return 0, net.ErrClosed
	}
}

func (c *conn) Write(b []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.writeBroken {
		return 0, os.ErrDeadlineExceeded
	}
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	case <-c.writeDL.wait():
		c.writeBroken = true
		return 0, os.ErrDeadlineExceeded
	default:
	}
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	// The caller may reuse b once Write returns, which a timed-out write
	// leaves running: the goroutine owns its own copy.
	buf := append([]byte(nil), b...)
	go func() {
		n, err := c.s.Write(buf)
		done <- result{n, err}
	}()
	select {
	case r := <-done:
		return r.n, r.err
	case <-c.writeDL.wait():
		c.writeBroken = true
		return 0, os.ErrDeadlineExceeded
	case <-c.closed:
		return 0, net.ErrClosed
	}
}

func (c *conn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.readDL.stop()
		c.writeDL.stop()
		c.closeErr = c.s.Close()
	})
	return c.closeErr
}

func (c *conn) LocalAddr() net.Addr  { return c.local }
func (c *conn) RemoteAddr() net.Addr { return c.remote }

func (c *conn) SetDeadline(t time.Time) error {
	c.readDL.set(t)
	c.writeDL.set(t)
	return nil
}

func (c *conn) SetReadDeadline(t time.Time) error {
	c.readDL.set(t)
	return nil
}

func (c *conn) SetWriteDeadline(t time.Time) error {
	c.writeDL.set(t)
	return nil
}

// deadline is net.Pipe's pipeDeadline: a channel that closes when the deadline
// passes and is replaced when the deadline moves.
type deadline struct {
	mu     sync.Mutex
	timer  *time.Timer
	cancel chan struct{}
}

func makeDeadline() deadline { return deadline{cancel: make(chan struct{})} }

func (d *deadline) set(t time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil && !d.timer.Stop() {
		<-d.cancel // the timer fired: wait for it to finish closing cancel
	}
	d.timer = nil

	closed := isClosed(d.cancel)
	if t.IsZero() {
		if closed {
			d.cancel = make(chan struct{})
		}
		return
	}
	if dur := time.Until(t); dur > 0 {
		if closed {
			d.cancel = make(chan struct{})
		}
		cancel := d.cancel
		d.timer = time.AfterFunc(dur, func() { close(cancel) })
		return
	}
	if !closed {
		close(d.cancel)
	}
}

func (d *deadline) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop()
	}
}

func (d *deadline) wait() chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cancel
}

func isClosed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}
