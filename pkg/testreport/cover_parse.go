package testreport

import (
	"bufio"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// parseGoCover reads a Go text profile: a `mode:` line, then one line per
// block, `<file>:<startLine>.<startCol>,<endLine>.<endCol> <statements>
// <count>`. Concatenated profiles (several `mode:` lines) are accepted; if
// any is `set`, the input is a set input. It returns the input's mode.
func parseGoCover(br *bufio.Reader, b *covBuilder, lim Limits) (string, error) {
	mode := ""
	n := 0
	for {
		line, tooLong, err := readLine(br, lim.MaxLineBytes)
		n++
		if tooLong {
			return "", fmt.Errorf("testreport: go profile line %d is longer than %d bytes", n, lim.MaxLineBytes)
		}
		if l := strings.TrimSpace(line); l != "" {
			if m, ok := strings.CutPrefix(l, "mode:"); ok {
				m = strings.TrimSpace(m)
				switch m {
				case "set", "count", "atomic":
				default:
					return "", fmt.Errorf("testreport: go profile line %d: unknown mode %q", n, m)
				}
				if mode != "set" {
					mode = m
				}
			} else {
				if mode == "" {
					return "", fmt.Errorf("testreport: go profile: no mode line before line %d", n)
				}
				if perr := goCoverLine(l, b); perr != nil {
					return "", fmt.Errorf("testreport: go profile line %d: %w", n, perr)
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
	}
	if mode == "" {
		return "", ErrNoInput
	}
	return mode, nil
}

func goCoverLine(l string, b *covBuilder) error {
	sp2 := strings.LastIndexByte(l, ' ')
	if sp2 < 0 {
		return errors.New("malformed block")
	}
	sp1 := strings.LastIndexByte(l[:sp2], ' ')
	if sp1 < 0 {
		return errors.New("malformed block")
	}
	loc, nStr, cStr := l[:sp1], l[sp1+1:sp2], l[sp2+1:]
	colon := strings.LastIndexByte(loc, ':')
	if colon <= 0 {
		return errors.New("malformed block position")
	}
	name, pos := loc[:colon], loc[colon+1:]
	start, end, ok := strings.Cut(pos, ",")
	if !ok {
		return errors.New("malformed block position")
	}
	sl, sc, err1 := lineCol(start)
	el, ec, err2 := lineCol(end)
	stmts, err3 := strconv.Atoi(nStr)
	count, err4 := strconv.ParseInt(cStr, 10, 64)
	if err := errors.Join(err1, err2, err3, err4); err != nil || stmts < 0 || count < 0 {
		return errors.New("malformed block numbers")
	}
	var f *builderFile
	var err error
	if strings.HasPrefix(name, "/") || filepath.IsAbs(name) {
		// A file outside any module (command-line-arguments) is named by
		// its absolute path.
		f, err = b.fileFor("", name, GranularityStatement)
	} else {
		// Every other name is the file's import-path-qualified name
		// ("example.com/demo/a/a.go"), on every OS: it is kept whole as
		// ImportPath (the package is its directory), and Merge derives the
		// repo-relative Path from MergeOptions.ModulePath. A repo path
		// cannot be read off the name alone, because the module path's
		// length is not in the profile.
		f, err = b.fileFor(name, "", GranularityStatement)
	}
	if err != nil {
		return err
	}
	return b.add(f, Block{StartLine: sl, StartCol: sc, EndLine: el, EndCol: ec, Statements: stmts}, count)
}

func lineCol(s string) (int, int, error) {
	l, c, ok := strings.Cut(s, ".")
	if !ok {
		return 0, 0, errors.New("want line.col")
	}
	line, err := strconv.Atoi(l)
	if err != nil {
		return 0, 0, err
	}
	col, err := strconv.Atoi(c)
	if err != nil {
		return 0, 0, err
	}
	if line < 0 || col < 0 {
		return 0, 0, errors.New("negative position")
	}
	return line, col, nil
}

// parseLCOV reads an LCOV tracefile. Only SF (the file) and DA (a line and
// its hit count) records carry what the coverage document holds; functions
// and branches are ignored.
func parseLCOV(br *bufio.Reader, b *covBuilder, lim Limits) error {
	var cur *builderFile
	n, records := 0, 0
	for {
		line, tooLong, err := readLine(br, lim.MaxLineBytes)
		n++
		if tooLong {
			return fmt.Errorf("testreport: lcov line %d is longer than %d bytes", n, lim.MaxLineBytes)
		}
		l := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(l, "SF:"):
			f, ferr := b.fileFor("", strings.TrimSpace(l[3:]), GranularityLine)
			if ferr != nil {
				return ferr
			}
			cur = f
			records++
		case strings.HasPrefix(l, "DA:"):
			if cur == nil {
				return fmt.Errorf("testreport: lcov line %d: DA before SF", n)
			}
			fields := strings.Split(l[3:], ",")
			if len(fields) < 2 {
				return fmt.Errorf("testreport: lcov line %d: malformed DA", n)
			}
			ln, err1 := strconv.Atoi(strings.TrimSpace(fields[0]))
			hits, err2 := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64)
			if err1 != nil || err2 != nil || ln < 0 {
				return fmt.Errorf("testreport: lcov line %d: malformed DA", n)
			}
			if aerr := b.add(cur, Block{StartLine: ln, EndLine: ln, Statements: 1}, hits); aerr != nil {
				return aerr
			}
		case l == "end_of_record":
			cur = nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}
	if records == 0 {
		return ErrNoInput
	}
	return nil
}

// parseCobertura reads Cobertura XML. A class's filename is joined to the
// first <source> (when the filename is relative), then made relative to
// opts.Root. Only a class's own <lines> count; a method's <lines> repeat
// them.
func parseCobertura(br *bufio.Reader, b *covBuilder) error {
	d := xml.NewDecoder(br)
	d.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	var (
		source    string
		inSource  bool
		cur       *builderFile
		inMethods int
		sawRoot   bool
	)
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var le *LimitError
			if errors.As(err, &le) {
				return le
			}
			return fmt.Errorf("testreport: cobertura: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "coverage":
				sawRoot = true
			case "source":
				inSource = source == ""
			case "class":
				name := attr(t, "filename")
				if source != "" && !strings.HasPrefix(name, "/") && !filepath.IsAbs(name) {
					name = path.Join(filepath.ToSlash(source), name)
				}
				f, ferr := b.fileFor("", name, GranularityLine)
				if ferr != nil {
					return ferr
				}
				cur = f
			case "methods":
				inMethods++
			case "line":
				if cur == nil || inMethods > 0 {
					continue
				}
				ln, err1 := strconv.Atoi(attr(t, "number"))
				hits, err2 := strconv.ParseInt(attr(t, "hits"), 10, 64)
				if err1 != nil || err2 != nil || ln < 0 {
					return fmt.Errorf("testreport: cobertura: malformed <line number=%q hits=%q>", attr(t, "number"), attr(t, "hits"))
				}
				if aerr := b.add(cur, Block{StartLine: ln, EndLine: ln, Statements: 1}, hits); aerr != nil {
					return aerr
				}
			}
		case xml.CharData:
			if inSource {
				source += strings.TrimSpace(string(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "source":
				inSource = false
			case "class":
				cur = nil
			case "methods":
				inMethods--
			}
		}
	}
	if !sawRoot {
		return ErrNoInput
	}
	return nil
}

// istanbulFile is one file of istanbul's coverage-final.json.
type istanbulFile struct {
	Path         string `json:"path"`
	StatementMap map[string]struct {
		Start istanbulPos `json:"start"`
		End   istanbulPos `json:"end"`
	} `json:"statementMap"`
	S map[string]json.Number `json:"s"`
}

type istanbulPos struct {
	Line   int  `json:"line"`
	Column *int `json:"column"`
}

func (p istanbulPos) col() int {
	if p.Column == nil || *p.Column < 0 {
		return 0
	}
	return *p.Column
}

// parseIstanbul reads istanbul's coverage-final.json: per file, a statement
// map and a hit count per statement. Statements map one to one onto blocks.
func parseIstanbul(br *bufio.Reader, b *covBuilder) error {
	var doc map[string]istanbulFile
	dec := json.NewDecoder(br)
	if err := dec.Decode(&doc); err != nil {
		var le *LimitError
		if errors.As(err, &le) {
			return le
		}
		if errors.Is(err, io.EOF) {
			return ErrNoInput
		}
		return fmt.Errorf("testreport: istanbul: %w", err)
	}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f := doc[k]
		name := f.Path
		if name == "" {
			name = k
		}
		bf, err := b.fileFor("", name, GranularityStatement)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(f.StatementMap))
		for id := range f.StatementMap {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			st := f.StatementMap[id]
			hits := int64(0)
			if v, ok := f.S[id]; ok {
				h, err := v.Int64()
				if err != nil || h < 0 {
					return fmt.Errorf("testreport: istanbul: %s: statement %s: malformed count %q", name, id, v)
				}
				hits = h
			}
			blk := Block{StartLine: max(st.Start.Line, 0), StartCol: st.Start.col(), EndLine: max(st.End.Line, 0), EndCol: st.End.col(), Statements: 1}
			if err := b.add(bf, blk, hits); err != nil {
				return err
			}
		}
	}
	return nil
}
