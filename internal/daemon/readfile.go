// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"bytes"
	"fmt"
	"os"
)

// ReadFileContent reads path and returns the requested line window as the
// file's literal bytes. It is the single implementation behind both
// LocalClient.ReadFile and the daemon's fs.read_file command, so a tool call
// sees the same content whether the daemon is in-process or remote.
//
// The contract is byte-exactness: Content is the exact slice of the file
// covering lines [Offset, Offset+Limit), terminators included. An unbounded
// read reproduces the file verbatim; consecutive windows concatenate back into
// it.
//
// That matters because the edit tools splice into Content and write the result
// straight back to disk. The previous bufio.Scanner + strings.Join(lines, "\n")
// reconstruction could not preserve its input — ScanLines drops each line's
// terminator and its dropCR helper strips \r — so every read silently deleted
// the file's final newline and re-encoded CRLF as LF, and both losses were
// written back. An old_string ending at EOF could never match, and a one-word
// edit to a CRLF file rewrote every line in it. Scanner also caps token length,
// which failed the read outright on a long minified line.
func ReadFileContent(path string, opts *ReadFileOpts) (*FileContent, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	offset, limit := 0, 0
	if opts != nil {
		offset, limit = opts.Offset, opts.Limit
	}
	if offset < 0 {
		offset = 0
	}
	if limit < 0 {
		limit = 0
	}

	start, end, totalLines := lineWindow(data, offset, limit)

	return &FileContent{
		Content:    string(data[start:end]),
		TotalLines: totalLines,
		Truncated:  limit > 0 && totalLines > offset+limit,
		Size:       info.Size(),
	}, nil
}

// lineWindow locates the byte range of lines [offset, offset+limit) within data
// and counts the file's lines. A line runs from just past the previous '\n'
// through its own '\n' inclusive; a trailing fragment with no '\n' is the final
// line, so "a\nb" and "a\nb\n" are both two lines and "" is zero. Both returned
// bounds land on line starts, which is what keeps every terminator inside the
// window and makes data[start:end] byte-identical to that region of the file.
// limit == 0 means "through end of file".
func lineWindow(data []byte, offset, limit int) (start, end, totalLines int) {
	n := len(data)
	// Default both bounds to EOF so an offset past the last line yields an
	// empty window rather than an out-of-range slice.
	start, end = n, n

	pos, line := 0, 0
	for pos < n {
		if line == offset {
			start = pos
		}
		if limit > 0 && line == offset+limit {
			end = pos
		}
		if nl := bytes.IndexByte(data[pos:], '\n'); nl < 0 {
			pos = n
		} else {
			pos += nl + 1
		}
		line++
	}
	totalLines = line

	if offset == 0 {
		start = 0
	}
	if limit == 0 || offset+limit >= totalLines {
		end = n
	}
	return start, end, totalLines
}
