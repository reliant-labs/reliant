package gocacheprog

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The on-disk layout is cmd/go's DiskCache (cmd/go/internal/cache), so a tier
// written here is a tier plain `go` reads, and the reverse.

const (
	hashSize = 32
	hexSize  = hashSize * 2
	// entrySize is an index file's exact length:
	// "v1 <hex id> <hex out> <size, 20 wide> <unixnano, 20 wide>\n".
	entrySize = 2 + 1 + hexSize + 1 + hexSize + 1 + 20 + 1 + 20 + 1
	// mtimeInterval is cmd/go's: an entry's mtime is restamped on use only
	// when it is at least this stale, so a hot entry is not rewritten on
	// every build.
	mtimeInterval = time.Hour
)

// fileName is the path of the cache file for id with the given suffix ("a"
// for an index entry, "d" for an output).
func fileName(dir string, id []byte, suffix string) string {
	return filepath.Join(dir, fmt.Sprintf("%02x", id[0]), fmt.Sprintf("%x", id)+"-"+suffix)
}

// indexEntry is one parsed `-a` file.
type indexEntry struct {
	outputID []byte
	size     int64
	time     time.Time
}

// readIndex reads and strictly parses the index entry for actionID in dir.
// Anything that is not exactly cmd/go's format, for exactly this action, is a
// miss (ok false), never an error: a damaged entry only costs a rebuild.
func readIndex(dir string, actionID []byte) (indexEntry, bool) {
	f, err := os.Open(fileName(dir, actionID, "a"))
	if err != nil {
		return indexEntry{}, false
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, entrySize+1) // +1 detects a file that is too long
	n, err := io.ReadFull(f, buf)
	if n != entrySize || !errors.Is(err, io.ErrUnexpectedEOF) {
		return indexEntry{}, false
	}
	return parseIndex(buf[:entrySize], actionID)
}

func parseIndex(e []byte, actionID []byte) (indexEntry, bool) {
	if len(e) != entrySize || e[0] != 'v' || e[1] != '1' || e[2] != ' ' ||
		e[3+hexSize] != ' ' || e[3+hexSize+1+hexSize] != ' ' ||
		e[3+hexSize+1+hexSize+1+20] != ' ' || e[entrySize-1] != '\n' {
		return indexEntry{}, false
	}
	eid := e[3 : 3+hexSize]
	eout := e[3+hexSize+1 : 3+2*hexSize+1]
	esize := e[3+2*hexSize+2 : 3+2*hexSize+2+20]
	etime := e[3+2*hexSize+2+21 : 3+2*hexSize+2+21+20]
	id := make([]byte, hashSize)
	if _, err := hex.Decode(id, eid); err != nil || !bytes.Equal(id, actionID) {
		return indexEntry{}, false
	}
	out := make([]byte, hashSize)
	if _, err := hex.Decode(out, eout); err != nil {
		return indexEntry{}, false
	}
	size, err := strconv.ParseInt(strings.TrimLeft(string(esize), " "), 10, 64)
	if err != nil || size < 0 {
		return indexEntry{}, false
	}
	tm, err := strconv.ParseInt(strings.TrimLeft(string(etime), " "), 10, 64)
	if err != nil || tm < 0 {
		return indexEntry{}, false
	}
	return indexEntry{outputID: out, size: size, time: time.Unix(0, tm)}, true
}

// formatIndex is the index entry cmd/go's putIndexEntry writes.
func formatIndex(actionID, outputID []byte, size int64, now time.Time) []byte {
	return fmt.Appendf(nil, "v1 %x %x %20d %20d\n", actionID, outputID, size, now.UnixNano())
}

// outputOK reports whether path is a regular file of exactly size bytes. An
// executable entry (a `-d` directory) or a truncated output is not a hit.
func outputOK(path string, size int64) (os.FileInfo, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return nil, false
	}
	return info, true
}

// markUsed restamps path to now when its mtime is at least mtimeInterval
// stale, like cmd/go's markUsed. Errors are ignored: a missed restamp only
// makes the entry look older to the trim.
func markUsed(path string, info os.FileInfo, now time.Time) {
	if info == nil {
		var err error
		if info, err = os.Stat(path); err != nil {
			return
		}
	}
	if now.Sub(info.ModTime()) >= mtimeInterval {
		_ = os.Chtimes(path, now, now)
	}
}

// writeAtomic writes data to path through a temp file in the same directory
// and a rename, so a reader sees either no file or the whole of it. It does
// not fsync: a cache entry lost in a crash is a rebuild, and a torn one is a
// miss (readIndex and outputOK check lengths). The mode is cmd/go's 0666,
// subject to the umask.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	f, err := createTemp(path)
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		_ = os.Remove(tmp)
	}
	return werr
}

// createTemp creates a new file beside path, mode 0666 so the umask decides
// (os.CreateTemp would make it 0600, unreadable to the other users a trusted
// tier serves).
func createTemp(path string) (*os.File, error) {
	for range 100 {
		name := path + ".tmp-" + strconv.FormatUint(rand.Uint64(), 36)
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return f, err
	}
	return nil, fmt.Errorf("create a temp file beside %s: too many collisions", path)
}
