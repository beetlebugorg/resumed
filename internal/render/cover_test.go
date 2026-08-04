package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/beetlebugorg/resumed/internal/store"
)

// A cover letter is free prose, so it is more exposed to Typst's markup
// characters than a resume bullet is. Same fixture as the resume test for the
// same reason: a letter that silently turns "#1" into a heading is worse than
// one that fails to build.
func TestCoverLetterCompilesWithSpecialCharacters(t *testing.T) {
	if !Available() {
		t.Skip("typst not on PATH")
	}

	letter := &store.CoverLetter{
		ID: 1, JobID: 1, Version: 2,
		Greeting: "Dear Example Engineering Team,",
		Body:     strings.Join(nasty, "\n\n"),
		Closing:  "Sincerely,",
	}
	profile := store.Profile{Name: "Pat Example", Location: "Springfield, IL"}
	contacts := []store.Contact{
		{Kind: "email", Value: "pat@example.com", URL: "mailto:pat@example.com"},
		{Kind: "phone", Value: "555-0100", URL: "tel:+15550100"},
	}
	job := &store.Job{ID: 1, Company: "Example & Co", Title: "Engineering Manager, Platform"}

	// Deliberately the real output name the app uses. An earlier version of this
	// test passed with a placeholder base while production emitted a file that
	// imported itself, so the basename is part of what is under test.
	dir := t.TempDir()
	typPath, err := WriteWith(dir, "cover-letter", CoverLetterTypst(profile, contacts, job, letter),
		CoverTemplateName, CoverTemplate())
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	pdfPath, err := PDF(typPath)
	if err != nil {
		src, _ := os.ReadFile(typPath)
		t.Fatalf("compile: %v\n--- source ---\n%s", err, src)
	}
	info, err := os.Stat(pdfPath)
	if err != nil {
		t.Fatalf("stat pdf: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("compiled PDF is empty")
	}
	if _, err := os.Stat(filepath.Join(dir, CoverTemplateName)); err != nil {
		t.Errorf("cover template not written next to source: %v", err)
	}
	if CoverTemplateName == "cover-letter.typ" {
		t.Error("template name collides with the generated letter's filename; it would import itself")
	}
}

// The letter is authored as blank-line-separated paragraphs; losing one to a
// stray newline would silently drop an argument from the letter.
func TestParagraphsSplitsOnBlankLines(t *testing.T) {
	c := store.CoverLetter{Body: "First para.\n\nSecond\nwrapped para.\r\n\r\n\n\nThird para.\n\n  \n"}
	got := c.Paragraphs()
	want := []string{"First para.", "Second wrapped para.", "Third para."}
	if len(got) != len(want) {
		t.Fatalf("got %d paragraphs %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("paragraph %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A letter with no job attached still has to render — plenty of applications
// are made without a parsed company or title.
func TestCoverLetterWithoutJob(t *testing.T) {
	src := CoverLetterTypst(
		store.Profile{Name: "Pat Example"},
		nil,
		nil,
		&store.CoverLetter{Body: "One paragraph."},
	)
	if !strings.Contains(src, "cover-letter.with") {
		t.Errorf("missing template call:\n%s", src)
	}
	if strings.Contains(src, "recipient:") {
		t.Errorf("recipient block should be absent without a job:\n%s", src)
	}
}
