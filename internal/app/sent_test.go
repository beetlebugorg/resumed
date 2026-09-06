package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/beetlebugorg/resumed/internal/store"
)

// TestApplyingFreezesACopyOfWhatWasSent is the whole point of the feature: the
// live PDF is overwritten in place by later renders, so the bytes an employer
// received only survive if they are copied aside at the moment of applying.
func TestApplyingFreezesACopyOfWhatWasSent(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := New(st, filepath.Join(dir, "jobs"))

	if err := st.SetProfile(store.Profile{Name: "Pat Example"}); err != nil {
		t.Fatal(err)
	}
	roleID, err := st.AddRole(store.Role{Company: "Northwind", Title: "Staff Engineer"})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := st.AddJob(store.Job{Company: "Northwind", Title: "Staff Engineer", Status: "tailoring"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := st.CreateResume(jobID, "Summary.", "", nil, []store.ResumeItem{
		{Kind: "role", RefID: roleID},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Stand in for a render: a PDF on disk and its source in the database.
	pdf := filepath.Join(dir, "resume.pdf")
	if err := os.WriteFile(pdf, []byte("PDF AS SENT"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.SetResumeOutput(res.ID, "TYPST AS SENT", pdf); err != nil {
		t.Fatal(err)
	}

	if err := a.SetJobStatus(jobID, "applied"); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got, err := st.GetResume(res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Sent() {
		t.Fatal("applying did not capture a sent snapshot")
	}
	if got.SentTypst != "TYPST AS SENT" {
		t.Errorf("SentTypst = %q", got.SentTypst)
	}
	if got.SentPDFPath == "" {
		t.Fatal("no sent PDF was written")
	}

	// The copy must be a separate file: overwriting the live PDF, as a later
	// render does, must not disturb it.
	if err := os.WriteFile(pdf, []byte("A LATER RENDER"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(got.SentPDFPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "PDF AS SENT" {
		t.Errorf("sent copy changed when the live PDF was rewritten: %q", b)
	}

	// Moving deeper into the pipeline must not re-snapshot.
	if err := a.SetJobStatus(jobID, "interviewing"); err != nil {
		t.Fatal(err)
	}
	again, err := st.GetResume(res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.SentTypst != "TYPST AS SENT" {
		t.Errorf("interviewing re-snapshotted: %q", again.SentTypst)
	}
}
