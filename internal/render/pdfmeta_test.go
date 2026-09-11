package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetProducerIsIdempotent guards the incremental update from stacking: a
// second call finds /Producer already present and leaves the file alone.
func TestSetProducerIsIdempotent(t *testing.T) {
	if !Available() {
		t.Skip("typst not on PATH")
	}
	dir := t.TempDir()
	typPath, err := Write(dir, "doc", Typst(nastyDoc()))
	if err != nil {
		t.Fatal(err)
	}
	pdfPath, err := PDF(typPath)
	if err != nil {
		t.Fatal(err)
	}

	first, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(first), "/Producer"); n != 1 {
		t.Fatalf("want 1 /Producer after render, got %d", n)
	}
	if err := setProducer(pdfPath, Producer); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) {
		t.Errorf("second call changed the file: %d bytes then %d", len(first), len(second))
	}
	_ = filepath.Base(pdfPath)
}

// TestSetProducerLeavesNonTypstFilesAlone checks the guard rails: a file
// without a trailer gets an error rather than a corrupting append.
func TestSetProducerRejectsUnfamiliarFiles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "not.pdf")
	if err := os.WriteFile(p, []byte("%PDF-1.7\nnot really a pdf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p)
	if err := setProducer(p, "resumed"); err == nil {
		t.Error("want an error for a file with no trailer")
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Error("the file was modified despite the error")
	}
}
