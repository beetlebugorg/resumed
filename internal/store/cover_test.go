package store

import "testing"

func testJob(t *testing.T, s *Store) int64 {
	t.Helper()
	id, err := s.AddJob(Job{Company: "Example & Co", Title: "Engineering Manager", Status: "saved"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// One letter per job. Saving again must replace the text and bump the version
// rather than accumulating rows, which is the same model the resume side
// converged on.
func TestSaveCoverLetterReplacesInPlace(t *testing.T) {
	s := testStore(t)
	jobID := testJob(t, s)

	first, err := s.SaveCoverLetter(CoverLetter{JobID: jobID, Body: "First draft."})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if first.Version != 1 {
		t.Errorf("first version = %d, want 1", first.Version)
	}

	second, err := s.SaveCoverLetter(CoverLetter{
		JobID: jobID, Body: "Second draft.", Greeting: "Hello,", Rationale: "sharper opening",
	})
	if err != nil {
		t.Fatalf("resave: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("id changed on resave: %d -> %d", first.ID, second.ID)
	}
	if second.Version != 2 {
		t.Errorf("second version = %d, want 2", second.Version)
	}
	if second.Body != "Second draft." || second.Greeting != "Hello," {
		t.Errorf("text not replaced: %+v", second)
	}

	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM cover_letters WHERE job_id = ?`, jobID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("got %d rows for the job, want 1", count)
	}
}

// Rendered output must not outlive the prose it was built from, or a stale PDF
// gets attached to an application after the letter was rewritten.
func TestSaveCoverLetterClearsStaleRender(t *testing.T) {
	s := testStore(t)
	jobID := testJob(t, s)

	c, err := s.SaveCoverLetter(CoverLetter{JobID: jobID, Body: "Draft."})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetCoverLetterOutput(c.ID, "#import ...", "/tmp/cover-letter.pdf"); err != nil {
		t.Fatal(err)
	}
	again, err := s.SaveCoverLetter(CoverLetter{JobID: jobID, Body: "Rewritten."})
	if err != nil {
		t.Fatal(err)
	}
	if again.PDFPath != "" || again.Typst != "" {
		t.Errorf("stale render survived rewrite: pdf=%q typst=%q", again.PDFPath, again.Typst)
	}
}

// A job without a letter is an ordinary state, not an error — the job page has
// to render before anything has been written.
func TestCoverLetterByJobMissingIsNotAnError(t *testing.T) {
	s := testStore(t)
	jobID := testJob(t, s)

	c, err := s.CoverLetterByJob(jobID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c != nil {
		t.Errorf("got %+v, want nil", c)
	}
}

func TestSaveCoverLetterRejectsEmptyBodyAndUnknownJob(t *testing.T) {
	s := testStore(t)
	jobID := testJob(t, s)

	if _, err := s.SaveCoverLetter(CoverLetter{JobID: jobID, Body: "   \n "}); err == nil {
		t.Error("empty body accepted")
	}
	if _, err := s.SaveCoverLetter(CoverLetter{JobID: 9999, Body: "Hello."}); err == nil {
		t.Error("unknown job accepted")
	}
}

// Deleting a job takes its letter with it; an orphaned letter would show up
// against no application at all.
func TestCoverLetterCascadesWithJob(t *testing.T) {
	s := testStore(t)
	jobID := testJob(t, s)

	if _, err := s.SaveCoverLetter(CoverLetter{JobID: jobID, Body: "Draft."}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteJob(jobID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM cover_letters WHERE job_id = ?`, jobID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("got %d orphaned letters, want 0", count)
	}
}
