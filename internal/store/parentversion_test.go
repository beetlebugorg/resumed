package store

import "testing"

// TestUpdateParentKeepsChildren is the companion to
// TestUpdateFactLeavesCitedResumesAlone. That one covers correcting a bullet,
// which is safe because a bullet has no children. Correcting a role is not:
// versioning inserts a new row with a new id, while the role's bullets still
// point at the old one.
func TestUpdateParentKeepsChildren(t *testing.T) {
	s := testStore(t)
	roleID, keepID, dropID := seed(t, s)

	newRoleID, err := s.UpdateFact(KindRole, roleID, map[string]string{"location": "Dallas, TX"})
	if err != nil {
		t.Fatalf("update role: %v", err)
	}
	if newRoleID == roleID {
		t.Fatal("update changed the row in place instead of adding a version")
	}

	fb, err := s.FactBase()
	if err != nil {
		t.Fatal(err)
	}
	if len(fb.Roles) != 1 {
		t.Fatalf("want exactly one current role, got %d", len(fb.Roles))
	}
	got := fb.Roles[0]
	if got.Location != "Dallas, TX" {
		t.Errorf("correction not visible: location = %q", got.Location)
	}
	if len(got.Bullets) != 2 {
		t.Fatalf("role lost its bullets across the version: got %d, want 2", len(got.Bullets))
	}
	seen := map[int64]bool{}
	for _, b := range got.Bullets {
		seen[b.ID] = true
	}
	if !seen[keepID] || !seen[dropID] {
		t.Errorf("bullets did not follow the role: got %v", seen)
	}
}

// TestAssembleAfterParentVersioned guards the rendering path. Assemble groups a
// bullet under its role by role_id, and skips a bullet whose role was not
// selected. If a corrected role breaks that link the bullet is dropped from the
// PDF silently, with no error to notice.
func TestAssembleAfterParentVersioned(t *testing.T) {
	s := testStore(t)
	roleID, keepID, _ := seed(t, s)

	newRoleID, err := s.UpdateFact(KindRole, roleID, map[string]string{"title": "Principal Engineer"})
	if err != nil {
		t.Fatal(err)
	}

	jobID, err := s.AddJob(Job{Company: "Northwind Systems", Title: "Staff Engineer", Status: "tailoring"})
	if err != nil {
		t.Fatal(err)
	}
	// Tailoring after the correction cites the new role version, while the
	// bullet still carries the old role_id.
	res, err := s.CreateResume(jobID, "Summary.", "", nil, []ResumeItem{
		{Kind: KindRole, RefID: newRoleID},
		{Kind: KindBullet, RefID: keepID, ParentRefID: &newRoleID},
	})
	if err != nil {
		t.Fatal(err)
	}

	doc, err := s.Assemble(res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Roles) != 1 {
		t.Fatalf("want 1 role, got %d", len(doc.Roles))
	}
	if got := len(doc.Roles[0].Bullets); got != 1 {
		t.Fatalf("bullet dropped from the rendered resume: got %d bullets, want 1", got)
	}
}

// TestFactBaseAllShowsOneRowPerFact covers the facts page, which reads
// FactBaseAll so it can show retired facts. Retired and superseded are not the
// same thing: a corrected fact should appear once, as its current version, not
// once per edit.
func TestFactBaseAllShowsOneRowPerFact(t *testing.T) {
	s := testStore(t)
	_, keepID, _ := seed(t, s)

	if _, err := s.UpdateFact(KindBullet, keepID, map[string]string{"text": "Built the deploy console for 40 teams."}); err != nil {
		t.Fatal(err)
	}

	fb, err := s.FactBaseAll()
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, r := range fb.Roles {
		for _, b := range r.Bullets {
			texts = append(texts, b.Text)
		}
	}
	if len(texts) != 2 {
		t.Fatalf("facts page would list %d bullets for 2 facts: %v", len(texts), texts)
	}
}
