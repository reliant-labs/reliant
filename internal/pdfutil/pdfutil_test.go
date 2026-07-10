// Copyright (c) 2025 Reliant Labs

package pdfutil

import (
	"fmt"
	"strings"
	"testing"
)

// buildMinimalPDF hand-writes a valid PDF with n blank pages. It avoids
// depending on pdfcpu's document-generation API or on system sample files.
func buildMinimalPDF(n int) []byte {
	var b strings.Builder
	offsets := []int{}
	write := func(s string) { b.WriteString(s) }
	obj := func(id int, body string) {
		offsets = append(offsets, b.Len())
		write(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", id, body))
	}
	write("%PDF-1.4\n")
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	kids := ""
	for i := 0; i < n; i++ {
		kids += fmt.Sprintf("%d 0 R ", 3+i)
	}
	obj(2, fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", n, strings.TrimSpace(kids)))
	for i := 0; i < n; i++ {
		obj(3+i, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>")
	}
	xrefStart := b.Len()
	total := 2 + n
	write(fmt.Sprintf("xref\n0 %d\n", total+1))
	write("0000000000 65535 f \n")
	for _, off := range offsets {
		write(fmt.Sprintf("%010d 00000 n \n", off))
	}
	write(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF", total+1, xrefStart))
	return []byte(b.String())
}

func TestPageCount(t *testing.T) {
	data := buildMinimalPDF(7)
	n, err := PageCount(data)
	if err != nil {
		t.Fatalf("PageCount: %v", err)
	}
	if n != 7 {
		t.Fatalf("expected 7 pages, got %d", n)
	}
}

func TestPageCountRejectsNonPDF(t *testing.T) {
	if _, err := PageCount([]byte("this is not a pdf")); err == nil {
		t.Fatal("expected error for non-PDF input")
	}
}

func TestExtractPagesRange(t *testing.T) {
	data := buildMinimalPDF(10)
	out, err := ExtractPages(data, "3-7")
	if err != nil {
		t.Fatalf("ExtractPages: %v", err)
	}
	got, err := PageCount(out)
	if err != nil {
		t.Fatalf("re-count extracted PDF: %v", err)
	}
	if got != 5 {
		t.Fatalf("expected 5 pages in extract, got %d", got)
	}
}

func TestExtractPagesSingle(t *testing.T) {
	data := buildMinimalPDF(4)
	out, err := ExtractPages(data, "2")
	if err != nil {
		t.Fatalf("ExtractPages: %v", err)
	}
	got, err := PageCount(out)
	if err != nil {
		t.Fatalf("re-count: %v", err)
	}
	if got != 1 {
		t.Fatalf("expected 1 page, got %d", got)
	}
}

func TestExtractPagesEnforcesCap(t *testing.T) {
	data := buildMinimalPDF(40)
	_, err := ExtractPages(data, fmt.Sprintf("1-%d", MaxPagesPerRequest+5))
	if err == nil {
		t.Fatalf("expected cap error when exceeding %d pages", MaxPagesPerRequest)
	}
	if !strings.Contains(err.Error(), "per-request") {
		t.Fatalf("expected per-request cap error, got: %v", err)
	}
}

func TestExtractPagesAtCapSucceeds(t *testing.T) {
	data := buildMinimalPDF(40)
	out, err := ExtractPages(data, fmt.Sprintf("1-%d", MaxPagesPerRequest))
	if err != nil {
		t.Fatalf("extract at cap should succeed: %v", err)
	}
	got, err := PageCount(out)
	if err != nil {
		t.Fatalf("re-count: %v", err)
	}
	if got != MaxPagesPerRequest {
		t.Fatalf("expected %d pages, got %d", MaxPagesPerRequest, got)
	}
}

func TestExtractPagesOutOfRange(t *testing.T) {
	data := buildMinimalPDF(3)
	if _, err := ExtractPages(data, "50-60"); err == nil {
		t.Fatal("expected error for out-of-range page selection")
	}
}
