package store

import (
	"strings"
	"testing"
)

// TestAppliedResumeCannotBeReplaced is the invariant behind the sent snapshot:
// once an application is out, the resume backing it stops moving. Without this,
// a reworded fact or a changed template silently rewrites a document an
// employer is already holding.
func TestAppliedResumeCannotBeReplaced(t *testing.T) {
	s := testStore(t)
	roleID, keepID, _ := seed(t, s)

	jobID, err := s.AddJob(Job{Company: "Northwind", Title: "Staff Engineer", Status: "tailoring"})
	if err != nil {
		t.Fatal(err)
	}
	items := []ResumeItem{
		{Kind: "role", RefID: roleID},
		{Kind: "bullet", RefID: keepID, ParentRefID: &roleID},
	}
	res, err := s.CreateResume(jobID, "First.", "", nil, items)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// While tailoring, replacing is normal.
	if _, err := s.CreateResume(jobID, "Second.", "", nil, items); err != nil {
		t.Fatalf("replace while tailoring: %v", err)
	}

	if err := s.UpdateJobFields(jobID, map[string]any{"status": "applied"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateResume(jobID, "Third.", "", nil, items); err == nil {
		t.Fatal("replaced the resume for an applied job; it should be frozen")
	} else if !strings.Contains(err.Error(), "frozen") {
		t.Errorf("error should explain the freeze, got: %v", err)
	}

	// interviewing and offer are live applications too.
	for _, status := range []string{"interviewing", "offer"} {
		if err := s.UpdateJobFields(jobID, map[string]any{"status": status}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateResume(jobID, "Nope.", "", nil, items); err == nil {
			t.Errorf("status %q allowed a replacement", status)
		}
	}

	// Moving back to tailoring is the deliberate, visible way to unfreeze.
	if err := s.UpdateJobFields(jobID, map[string]any{"status": "tailoring"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateResume(jobID, "Fourth.", "", nil, items); err != nil {
		t.Errorf("tailoring should unfreeze, got: %v", err)
	}
	_ = res
}

// TestSnapshotIsWriteOnce guards the path applied -> interviewing -> offer: only
// the first transition may record what was sent, or a render slipped in later
// would quietly become the record.
func TestSnapshotIsWriteOnce(t *testing.T) {
	s := testStore(t)
	roleID, keepID, _ := seed(t, s)
	jobID, err := s.AddJob(Job{Company: "Northwind", Title: "Staff Engineer", Status: "tailoring"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.CreateResume(jobID, "Summary.", "", nil, []ResumeItem{
		{Kind: "role", RefID: roleID},
		{Kind: "bullet", RefID: keepID, ParentRefID: &roleID},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.MarkResumeSent(res.ID, "AS SENT", "/tmp/resume.sent.pdf"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkResumeSent(res.ID, "LATER RENDER", "/tmp/other.pdf"); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetResume(res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SentTypst != "AS SENT" {
		t.Errorf("snapshot was overwritten: %q", got.SentTypst)
	}
	if !got.Sent() {
		t.Error("Sent() should report true once a snapshot exists")
	}
}
