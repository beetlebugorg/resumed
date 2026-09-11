package web

import (
	"strings"
	"testing"
	"time"

	"github.com/beetlebugorg/resumed/internal/store"
)

// A short note is never folded. The control costs more attention than the
// text it hides.
func TestSplitNoteLeavesShortNotesWhole(t *testing.T) {
	body := "Recruiter is Dana.\n\nRange 230-260."
	lead, rest := splitNote(body)
	if lead != body || rest != "" {
		t.Errorf("split a short note: lead=%q rest=%q", lead, rest)
	}
}

func TestSplitNoteFoldsAtTheFirstBlankLine(t *testing.T) {
	head := "Resume 18 and cover letter built and verified."
	tail := strings.Repeat("Framing note about the title de-inflation. ", 20)
	lead, rest := splitNote(head + "\n\n" + tail)
	if lead != head {
		t.Errorf("lead = %q, want %q", lead, head)
	}
	if rest != strings.TrimSpace(tail) {
		t.Errorf("rest = %q..., want the remainder", rest[:40])
	}
}

// Windows line endings are the author's paragraph break too.
func TestSplitNoteHandlesCRLF(t *testing.T) {
	tail := strings.Repeat("more reasoning. ", 40)
	lead, rest := splitNote("Lead line.\r\n\r\n" + tail)
	if lead != "Lead line." || rest == "" {
		t.Errorf("lead=%q rest empty=%v", lead, rest == "")
	}
}

// One long paragraph has no break to fold on, so it runs rather than being
// cut at an arbitrary character.
func TestSplitNoteLeavesOneLongParagraphAlone(t *testing.T) {
	body := strings.Repeat("a single unbroken paragraph that keeps going. ", 20)
	lead, rest := splitNote(body)
	if rest != "" {
		t.Errorf("rest = %q, want none", rest[:40])
	}
	if lead != strings.TrimSpace(body) {
		t.Error("lead should be the whole paragraph")
	}
}

func stamp(d time.Duration) string {
	return time.Now().UTC().Add(-d).Format(stampLayout)
}

func TestRelTime(t *testing.T) {
	for _, c := range []struct {
		ago  time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{20 * time.Minute, "20 minutes ago"},
		{90 * time.Minute, "an hour ago"},
		{5 * time.Hour, "5 hours ago"},
		{30 * time.Hour, "yesterday"},
		{3 * 24 * time.Hour, "3 days ago"},
	} {
		if got := relTime(stamp(c.ago)); got != c.want {
			t.Errorf("relTime(%v ago) = %q, want %q", c.ago, got, c.want)
		}
	}
	// Older than a week falls back to a date.
	if got := relTime("2026-07-31 14:02:11"); !strings.Contains(got, "2026") {
		t.Errorf("old note = %q, want a date", got)
	}
	// A database clock slightly ahead of ours must not read as "in the future".
	if got := relTime(stamp(-30 * time.Second)); got != "just now" {
		t.Errorf("future stamp = %q, want %q", got, "just now")
	}
}

// Timestamps are stored UTC; the tooltip shows the reader's own clock.
func TestFullTimeIsLocal(t *testing.T) {
	utc := time.Date(2026, 9, 11, 22, 1, 55, 0, time.UTC)
	want := utc.Local().Format("Mon 2 Jan 2006, 15:04")
	if got := fullTime("2026-09-11 22:01:55"); got != want {
		t.Errorf("fullTime = %q, want %q", got, want)
	}
}

func TestTimeHelpersFallBackOnJunk(t *testing.T) {
	if got := relTime("not a timestamp"); got != "not a timestamp" {
		t.Errorf("relTime junk = %q", got)
	}
	if got := fullTime("not a timestamp"); got != "not a timestamp" {
		t.Errorf("fullTime junk = %q", got)
	}
}

// Authorship drives the rail colour and the name on each card, and anything
// that is not Claude is the user.
func TestBuildNotesMarksAuthorship(t *testing.T) {
	got := buildNotes([]store.JobNote{
		{ID: 1, Body: "mine", Author: "user"},
		{ID: 2, Body: "theirs", Author: "claude"},
		{ID: 3, Body: "legacy row", Author: ""},
	})
	for i, want := range []bool{true, false, true} {
		if got[i].Mine != want {
			t.Errorf("note %d Mine = %v, want %v", got[i].Note.ID, got[i].Mine, want)
		}
	}
}
