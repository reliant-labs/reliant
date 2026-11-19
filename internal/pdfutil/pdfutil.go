// Copyright (c) 2025 Reliant Labs

// Package pdfutil wraps pdfcpu with the small set of PDF operations Reliant
// needs: counting pages and extracting a page range into a new self-contained
// PDF. It exists so the daemon command handler and the local daemon client
// share one implementation.
//
//forge:exclude-contract: pure PDF page counting and page-range extraction over in-memory bytes (pdfcpu); no I/O, state or time
package pdfutil

import (
	"bytes"
	"context"
	"fmt"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// MaxPagesPerRequest caps how many pages a single ReadPages call may extract.
// This mirrors the Claude Code CLI's PDF paging limit and keeps a single tool
// result from ballooning the context window.
const MaxPagesPerRequest = 20

// pdfcpu's default configuration lives on disk: the first
// model.NewDefaultConfiguration() creates <os.UserConfigDir()>/pdfcpu with a
// config.yml and a fonts directory, and PANICS ("config problem: mkdir
// /.config: read-only file system") when it cannot. The temporal worker runs
// with HOME=/ on a read-only root filesystem, so every PDF read_attachment
// panicked there (prod, 2026-11-10). Counting and trimming pages need neither
// the file nor user fonts, so every call takes pdfcpu's stateless
// configuration: its built-in defaults, built in memory, touching no file.
func config() *model.Configuration {
	return model.NewStatelessConfiguration()
}

// PageCount returns the number of pages in the given PDF bytes.
func PageCount(data []byte) (_ int, err error) {
	defer recoverPDFPanic("pdf page count", &err)
	// pdfcpu takes a context since v0.16; this package's API has none to pass
	// (the work is in-memory and bounded by the input).
	count, err := api.PageCount(context.Background(), bytes.NewReader(data), config())
	if err != nil {
		return 0, fmt.Errorf("pdf page count: %w", err)
	}
	return count, nil
}

// ExtractPages trims the source PDF down to the requested pages (a pdfcpu page
// selection string such as "1-5", "3", or "10-20") and returns the resulting
// self-contained PDF bytes. It rejects selections resolving to more than
// MaxPagesPerRequest pages.
func ExtractPages(data []byte, pages string) (_ []byte, err error) {
	defer recoverPDFPanic(fmt.Sprintf("pdf extract %q", pages), &err)
	total, err := PageCount(data)
	if err != nil {
		return nil, err
	}

	selected, err := api.PagesForSelection(total, []string{pages}, false)
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
	if err := api.Trim(context.Background(), bytes.NewReader(data), &out, []string{pages}, config()); err != nil {
		return nil, fmt.Errorf("pdf trim to %q: %w", pages, err)
	}
	return out.Bytes(), nil
}

// recoverPDFPanic turns a pdfcpu panic into an error. pdfcpu reports some
// failures by panicking (its fault.Fail), and the bytes come from users, so a
// malformed PDF must fail the one read, not the daemon command or the tool
// call goroutine that asked for it.
func recoverPDFPanic(op string, err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("%s: pdfcpu panicked: %v", op, r)
	}
}
