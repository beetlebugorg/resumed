package store

import (
	"strings"
	"testing"
)

// TestUpdateFactLeavesCitedResumesAlone is the reason fact versions exist. A
// correction must not reach back into a resume that was already built, whether
// or not the job was ever marked applied.
func TestUpdateFactLeavesCitedResumesAlone(t *testing.T) {
	s := testStore(t)
	roleID, keepID, _ := seed(t, s)

	jobID, err := s.AddJob(Job{Company: "Northwind Systems", Title: "Staff Engineer", Status: "tailoring"})
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

	newID, err := s.UpdateFact(KindBullet, keepID, map[string]string{"text": "Built the deploy console for 40 teams."})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if newID == keepID {
		t.Fatal("update changed the row in place instead of adding a version")
	}

	// The resume still points at the version it selected.
	doc, err := s.Assemble(res.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, r := range doc.Roles {
		for _, b := range r.Bullets {
			got = b.Text
		}
	}
	if !strings.Contains(got, "Built the deploy console.") {
		t.Errorf("resume drifted to the new version: %q", got)
	}

	// New tailoring sees the correction, and sees it once.
	fb, err := s.FactBase()
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, r := range fb.Roles {
		for _, b := range r.Bullets {
			texts = append(texts, b.Text)
		}
	}
	joined := strings.Join(texts, " | ")
	if !strings.Contains(joined, "for 40 teams") {
		t.Errorf("fact base does not show the correction: %s", joined)
	}
	if strings.Contains(joined, "Built the deploy console.") {
		t.Errorf("fact base still offers the superseded version: %s", joined)
	}
}

// TestUpdateFactRejectsUnknownFields keeps structural columns out of reach.
func TestUpdateFactRejectsUnknownFields(t *testing.T) {
	s := testStore(t)
	_, keepID, _ := seed(t, s)

	for _, field := range []string{"id", "version", "fact_id", "retired_at", "role_id"} {
		if _, err := s.UpdateFact(KindBullet, keepID, map[string]string{field: "x"}); err == nil {
			t.Errorf("field %q should not be updatable", field)
		}
	}
	if _, err := s.UpdateFact("nonsense", keepID, map[string]string{"text": "x"}); err == nil {
		t.Error("unknown table should be rejected")
	}
}
