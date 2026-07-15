// Copyright (c) 2025 Reliant Labs

// Package pdfutil wraps pdfcpu with the small set of PDF operations Reliant
// needs: counting pages and extracting a page range into a new self-contained
// PDF. It exists so the daemon command handler and the local daemon client
// share one implementation.
package pdfutil

import (
	"bytes"
	"fmt"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// MaxPagesPerRequest caps how many pages a single ReadPages call may extract.
// This mirrors the Claude Code CLI's PDF paging limit and keeps a single tool
// result from ballooning the context window.
const MaxPagesPerRequest = 20

// PageCount returns the number of pages in the given PDF bytes.
func PageCount(data []byte) (int, error) {
	count, err := api.PageCount(bytes.NewReader(data), model.NewDefaultConfiguration())
	if err != nil {
		return 0, fmt.Errorf("pdf page count: %w", err)
	}
	return count, nil
}

// ExtractPages trims the source PDF down to the requested pages (a pdfcpu page
// selection string such as "1-5", "3", or "10-20") and returns the resulting
// self-contained PDF bytes. It rejects selections resolving to more than
// MaxPagesPerRequest pages.
func ExtractPages(data []byte, pages string) ([]byte, error) {
	total, err := PageCount(data)
	if err != nil {
		return nil, err
	}

	selected, err := api.PagesForPageSelection(total, []string{pages}, false, true)
	if err != nil {
		return nil, fmt.Errorf("pdf page selection %q: %w", pages, err)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("pdf page selection %q resolved to no pages (document has %d)", pages, total)
	}
	if len(selected) > MaxPagesPerRequest {
		return nil, fmt.Errorf("pdf page selection %q resolves to %d pages, exceeds the %d-page-per-request limit", pages, len(selected), MaxPagesPerRequest)
	}

	var out bytes.Buffer
	if err := api.Trim(bytes.NewReader(data), &out, []string{pages}, model.NewDefaultConfiguration()); err != nil {
		return nil, fmt.Errorf("pdf trim to %q: %w", pages, err)
	}
	return out.Bytes(), nil
}
