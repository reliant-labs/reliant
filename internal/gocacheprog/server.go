package gocacheprog

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// The wire types mirror cmd/go/internal/cacheprog, which cannot be imported.
// Requests arrive as JSON lines on stdin; a put with a body is followed by a
// line holding the body as a base64 JSON string. Responses go to stdout as
// JSON lines, in any order, matched by ID.

type request struct {
	ID       int64
	Command  string
	ActionID []byte `json:",omitempty"`
	OutputID []byte `json:",omitempty"`
	BodySize int64  `json:",omitempty"`
}

type response struct {
	ID            int64
	Err           string     `json:",omitempty"`
	KnownCommands []string   `json:",omitempty"`
	Miss          bool       `json:",omitempty"`
	OutputID      []byte     `json:",omitempty"`
	Size          int64      `json:",omitempty"`
	Time          *time.Time `json:",omitempty"`
	DiskPath      string     `json:",omitempty"`
}

// maxInFlight bounds the requests handled at once. Reading stops while it is
// reached, so at most this many put bodies are held in memory.
const maxInFlight = 16

// server answers cmd/go's cache requests from the two tiers of cfg.
type server struct {
	cfg   Config
	now   func() time.Time
	stats stats

	encMu sync.Mutex
	bw    *bufio.Writer
	enc   *json.Encoder
}

// newServer returns a server over an already Resolved cfg.
func newServer(cfg Config) *server {
	return &server{cfg: cfg, now: time.Now}
}

// serve speaks the GOCACHEPROG protocol on r and w until cmd/go sends close
// or closes r. It returns an error only when the stream itself is broken;
// a request that fails is answered with its error.
func (s *server) serve(r io.Reader, w io.Writer) error {
	start := s.now()
	s.bw = bufio.NewWriter(w)
	s.enc = json.NewEncoder(s.bw)
	defer func() { s.writeStats(s.now().Sub(start)) }()

	if err := s.reply(&response{ID: 0, KnownCommands: []string{"get", "put", "close"}}); err != nil {
		return err
	}

	dec := json.NewDecoder(bufio.NewReader(r))
	sem := make(chan struct{}, maxInFlight)
	var wg sync.WaitGroup
	for {
		var req request
		if err := dec.Decode(&req); err != nil {
			wg.Wait()
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read request: %w", err)
		}
		var body []byte
		if req.Command == "put" && req.BodySize > 0 {
			if err := dec.Decode(&body); err != nil {
				wg.Wait()
				return fmt.Errorf("read body of request %d: %w", req.ID, err)
			}
		}
		if req.Command == "close" {
			wg.Wait()
			return s.reply(&response{ID: req.ID})
		}
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			res := s.handle(&req, body)
			res.ID = req.ID
			_ = s.reply(res) // a broken stdout ends the next Decode too
		}()
	}
}

func (s *server) reply(res *response) error {
	s.encMu.Lock()
	defer s.encMu.Unlock()
	if err := s.enc.Encode(res); err != nil {
		return err
	}
	return s.bw.Flush()
}

func (s *server) handle(req *request, body []byte) *response {
	switch req.Command {
	case "get":
		if len(req.ActionID) != hashSize {
			return &response{Err: fmt.Sprintf("get: action id is %d bytes, want %d", len(req.ActionID), hashSize)}
		}
		return s.get(req.ActionID)
	case "put":
		if len(req.ActionID) != hashSize || len(req.OutputID) != hashSize {
			return &response{Err: "put: action and output ids must be 32 bytes"}
		}
		if int64(len(body)) != req.BodySize {
			return &response{Err: fmt.Sprintf("put: body is %d bytes, BodySize says %d", len(body), req.BodySize)}
		}
		res, err := s.put(req.ActionID, req.OutputID, body)
		if err != nil {
			return &response{Err: err.Error()}
		}
		return res
	default:
		return &response{Err: fmt.Sprintf("unknown command %q", req.Command)}
	}
}

// get looks the action up in the trusted tier, then in the overlay.
func (s *server) get(actionID []byte) *response {
	s.stats.Gets.Add(1)
	if s.cfg.Trusted != "" {
		if e, ok := readIndex(s.cfg.Trusted, actionID); ok {
			path := fileName(s.cfg.Trusted, e.outputID, "d")
			if info, ok := outputOK(path, e.size); ok {
				// Returned in place: no bytes are ever written to trusted.
				// The restamp is best effort, like markUsed: it fails
				// (EPERM, EROFS) for a reader that is not the owner or
				// behind a read-only mount, and changes no bytes.
				now := s.now()
				markUsed(path, info, now)
				markUsed(fileName(s.cfg.Trusted, actionID, "a"), nil, now)
				s.stats.TrustedHits.Add(1)
				return hit(e, path)
			}
		}
	}
	if e, ok := readIndex(s.cfg.Overlay, actionID); ok {
		now := s.now()
		path := fileName(s.cfg.Overlay, e.outputID, "d")
		info, ok := outputOK(path, e.size)
		if ok {
			markUsed(path, info, now)
		} else if s.cfg.Trusted != "" {
			// A deduplicated put: the overlay indexes trusted bytes.
			path = fileName(s.cfg.Trusted, e.outputID, "d")
			_, ok = outputOK(path, e.size)
		}
		if ok {
			markUsed(fileName(s.cfg.Overlay, actionID, "a"), nil, now)
			s.stats.OverlayHits.Add(1)
			return hit(e, path)
		}
	}
	s.stats.Misses.Add(1)
	return &response{Miss: true}
}

func hit(e indexEntry, path string) *response {
	t := e.time
	return &response{OutputID: e.outputID, Size: e.size, Time: &t, DiskPath: path}
}

// put stores body as the output of actionID in the overlay.
func (s *server) put(actionID, outputID, body []byte) (*response, error) {
	sum := sha256.Sum256(body)
	if !bytes.Equal(sum[:], outputID) {
		return nil, fmt.Errorf("put: output id %x is not the sha256 of the body (%x)", outputID, sum)
	}
	size := int64(len(body))
	s.stats.Puts.Add(1)
	s.stats.PutBytes.Add(size)

	path := ""
	if s.cfg.Trusted != "" {
		if p := fileName(s.cfg.Trusted, outputID, "d"); fileHasSize(p, size) {
			path = p
			s.stats.DedupedPuts.Add(1)
		}
	}
	if path == "" {
		path = fileName(s.cfg.Overlay, outputID, "d")
		if !fileHasSize(path, size) {
			if err := writeAtomic(path, body); err != nil {
				return nil, fmt.Errorf("put: write output: %w", err)
			}
		}
	}
	if err := writeAtomic(fileName(s.cfg.Overlay, actionID, "a"), formatIndex(actionID, outputID, size, s.now())); err != nil {
		return nil, fmt.Errorf("put: write index: %w", err)
	}
	return &response{DiskPath: path}, nil
}

func fileHasSize(path string, size int64) bool {
	_, ok := outputOK(path, size)
	return ok
}

// stats are the counters of one helper process.
type stats struct {
	Gets, TrustedHits, OverlayHits, Misses atomic.Int64
	Puts, PutBytes, DedupedPuts            atomic.Int64
}

// statsLine is the JSON line appended to the stats file at exit.
type statsLine struct {
	PID         int     `json:"pid"`
	Dir         string  `json:"dir"`
	Trusted     string  `json:"trusted"`
	TrustedNote string  `json:"trusted_note,omitempty"`
	Overlay     string  `json:"overlay"`
	Gets        int64   `json:"gets"`
	TrustedHits int64   `json:"trusted_hits"`
	OverlayHits int64   `json:"overlay_hits"`
	Misses      int64   `json:"misses"`
	Puts        int64   `json:"puts"`
	PutBytes    int64   `json:"put_bytes"`
	DedupedPuts int64   `json:"deduped_puts"`
	Seconds     float64 `json:"seconds"`
}

// writeStats appends this process's counters to cfg.StatsPath, if set. One
// short O_APPEND write per process, so concurrent builds do not interleave.
func (s *server) writeStats(elapsed time.Duration) {
	if s.cfg.StatsPath == "" {
		return
	}
	dir, _ := os.Getwd()
	line, err := json.Marshal(statsLine{
		PID: os.Getpid(), Dir: dir, Trusted: s.cfg.Trusted, TrustedNote: s.cfg.TrustedNote, Overlay: s.cfg.Overlay,
		Gets: s.stats.Gets.Load(), TrustedHits: s.stats.TrustedHits.Load(), OverlayHits: s.stats.OverlayHits.Load(),
		Misses: s.stats.Misses.Load(), Puts: s.stats.Puts.Load(), PutBytes: s.stats.PutBytes.Load(),
		DedupedPuts: s.stats.DedupedPuts.Load(), Seconds: elapsed.Seconds(),
	})
	if err != nil {
		return
	}
	f, err := os.OpenFile(s.cfg.StatsPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o666)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

// Run resolves cfg against the environment and serves the protocol on r and
// w: what `forge cache prog` does.
func Run(cfg Config, r io.Reader, w io.Writer) error {
	cfg, err := Resolve(cfg, os.Getenv)
	if err != nil {
		return err
	}
	return newServer(cfg).serve(r, w)
}
