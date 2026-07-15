package attachment

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ExtractTextFromFile extracts human-readable text from attachment bytes.
// For plain text formats this returns the content as-is.
// For .docx this performs best-effort extraction.
//
// PDFs are NOT handled here: they are binary documents read on demand (and
// paginated) via the read_attachment tool, not scraped to text.
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