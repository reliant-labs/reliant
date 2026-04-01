package attachment

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ExtractTextFromFile extracts human-readable text from attachment bytes.
// For plain text formats this returns the content as-is.
// For .docx and .pdf this performs best-effort extraction.
func ExtractTextFromFile(filename string, data []byte) (string, error) {
	ext := strings.ToLower(filepath.Ext(filename))

	switch ext {
	case ".docx":
		text, err := extractTextFromDOCX(data)
		if err != nil {
			return "", fmt.Errorf("failed to extract text from DOCX: %w", err)
		}
		if strings.TrimSpace(text) == "" {
			return "", fmt.Errorf("DOCX contains no extractable text")
		}
		return text, nil
	case ".pdf":
		text, err := extractTextFromPDF(data)
		if err != nil {
			return "", fmt.Errorf("failed to extract text from PDF: %w", err)
		}
		if strings.TrimSpace(text) == "" {
			return "", fmt.Errorf("PDF contains no extractable text")
		}
		return text, nil
	default:
		if !utf8.Valid(data) {
			return "", fmt.Errorf("file content is not valid UTF-8 text")
		}
		return string(data), nil
	}
}

func extractTextFromDOCX(data []byte) (string, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}

	var docXML []byte
	for _, f := range r.File {
		if f.Name == "word/document.xml" {
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			docXML, err = io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return "", err
			}
			break
		}
	}

	if len(docXML) == 0 {
		return "", fmt.Errorf("word/document.xml not found")
	}

	dec := xml.NewDecoder(bytes.NewReader(docXML))
	var sb strings.Builder
	var inText bool

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				sb.WriteByte('\t')
			case "br", "cr":
				sb.WriteByte('\n')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				sb.WriteByte('\n')
			}
		case xml.CharData:
			if inText {
				sb.Write([]byte(t))
			}
		}
	}

	return normalizeWhitespace(sb.String()), nil
}

func extractTextFromPDF(data []byte) (string, error) {
	streams := extractPDFStreams(data)
	if len(streams) == 0 {
		return "", fmt.Errorf("no PDF streams found")
	}

	var pieces []string
	for _, s := range streams {
		content := maybeInflatePDFStream(s)
		if txt := extractPDFTextLiterals(content); txt != "" {
			pieces = append(pieces, txt)
		}
	}

	if len(pieces) == 0 {
		return "", nil
	}

	return normalizeWhitespace(strings.Join(pieces, "\n")), nil
}

var pdfStreamRE = regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)

func extractPDFStreams(data []byte) [][]byte {
	matches := pdfStreamRE.FindAllSubmatch(data, -1)
	out := make([][]byte, 0, len(matches))
	for _, m := range matches {
		if len(m) > 1 {
			out = append(out, m[1])
		}
	}
	return out
}

func maybeInflatePDFStream(b []byte) []byte {
	zr, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return b
	}
	defer zr.Close()
	inflated, err := io.ReadAll(zr)
	if err != nil {
		return b
	}
	return inflated
}

var (
	pdfTjRE  = regexp.MustCompile(`\((?:\\.|[^\\)])*\)\s*Tj`)
	pdfTJRE  = regexp.MustCompile(`\[(?:.|\n|\r)*?\]\s*TJ`)
	pdfStrRE = regexp.MustCompile(`\((?:\\.|[^\\)])*\)`)
)

func extractPDFTextLiterals(content []byte) string {
	s := string(content)
	var out []string

	for _, m := range pdfTjRE.FindAllString(s, -1) {
		if str := extractFirstPDFString(m); str != "" {
			out = append(out, str)
		}
	}

	for _, arr := range pdfTJRE.FindAllString(s, -1) {
		for _, strToken := range pdfStrRE.FindAllString(arr, -1) {
			if str := decodePDFStringLiteral(strToken); str != "" {
				out = append(out, str)
			}
		}
	}

	return strings.TrimSpace(strings.Join(out, " "))
}

func extractFirstPDFString(s string) string {
	start := strings.IndexByte(s, '(')
	if start == -1 {
		return ""
	}
	end := strings.LastIndexByte(s, ')')
	if end <= start {
		return ""
	}
	return decodePDFStringLiteral(s[start : end+1])
}

func decodePDFStringLiteral(token string) string {
	if len(token) < 2 || token[0] != '(' || token[len(token)-1] != ')' {
		return ""
	}

	body := token[1 : len(token)-1]
	var sb strings.Builder

	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' {
			sb.WriteByte(c)
			continue
		}
		if i+1 >= len(body) {
			break
		}
		i++
		n := body[i]
		switch n {
		case 'n':
			sb.WriteByte('\n')
		case 'r':
			sb.WriteByte('\r')
		case 't':
			sb.WriteByte('\t')
		case 'b':
			sb.WriteByte('\b')
		case 'f':
			sb.WriteByte('\f')
		case '(', ')', '\\':
			sb.WriteByte(n)
		case '\n', '\r':
			// line continuation
		default:
			// octal: up to 3 digits
			if n >= '0' && n <= '7' {
				oct := []byte{n}
				for j := 0; j < 2 && i+1 < len(body); j++ {
					next := body[i+1]
					if next < '0' || next > '7' {
						break
					}
					i++
					oct = append(oct, next)
				}
				if v, err := strconv.ParseInt(string(oct), 8, 32); err == nil {
					sb.WriteByte(byte(v))
				}
			} else {
				sb.WriteByte(n)
			}
		}
	}

	return strings.TrimSpace(sb.String())
}

func normalizeWhitespace(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t\r")
	}
	joined := strings.Join(lines, "\n")
	joined = strings.ReplaceAll(joined, "\r\n", "\n")
	joined = strings.ReplaceAll(joined, "\r", "\n")
	return strings.TrimSpace(joined)
}
