package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// seed builds a minimal fact base and returns the role and its two bullets.
func seed(t *testing.T, s *Store) (roleID, keepID, dropID int64) {
	t.Helper()
	if err := s.SetProfile(Profile{Name: "Pat Example", Location: "Springfield, IL"}); err != nil {
		t.Fatal(err)
	}
	roleID, err := s.AddRole(Role{Company: "Northwind Systems", Title: "Staff Engineer", StartDate: "Mar 2021", EndDate: "Present"})
	if err != nil {
		t.Fatal(err)
	}
	keepID, err = s.AddBullet(Bullet{RoleID: roleID, Text: "Built Ops Desk."})
	if err != nil {
		t.Fatal(err)
	}
	dropID, err = s.AddBullet(Bullet{RoleID: roleID, Text: "Superseded thin version."})
	if err != nil {
		t.Fatal(err)
	}
	return roleID, keepID, dropID
}

// TestRetiredFactStillRendersExistingResume is the invariant that makes
// retirement safe: a resume already generated references its facts by id, and
// must keep rendering after one of them is retired. If this breaks, retiring a
// fact silently corrupts a resume that may already have been sent.
func TestRetiredFactStillRendersExistingResume(t *testing.T) {
	s := testStore(t)
	roleID, keepID, dropID := seed(t, s)

	jobID, err := s.AddJob(Job{Company: "Docker", Title: "Staff Engineer"})
	if err != nil {
		t.Fatal(err)
	}
	resume, err := s.CreateResume(jobID, "Summary.", "", []ResumeItem{
		{Kind: KindRole, RefID: roleID, Position: 1},
		{Kind: KindBullet, RefID: keepID, Position: 2},
		{Kind: KindBullet, RefID: dropID, Position: 3},
	})
	if err != nil {
		t.Fatalf("create resume: %v", err)
	}

	if err := s.SetFactRetired(KindBullet, dropID, true); err != nil {
		t.Fatalf("retire: %v", err)
	}

	doc, err := s.Assemble(resume.ID)
	if err != nil {
		t.Fatalf("assemble after retiring a cited fact: %v", err)
	}
	if len(doc.Roles) != 1 {
		t.Fatalf("want 1 role, got %d", len(doc.Roles))
	}
	if got := len(doc.Roles[0].Bullets); got != 2 {
		t.Fatalf("existing resume lost a bullet after retirement: want 2, got %d", got)
	}
	var texts []string
	for _, b := range doc.Roles[0].Bullets {
		texts = append(texts, b.Text)
	}
	if !strings.Contains(strings.Join(texts, " "), "Superseded thin version.") {
		t.Errorf("retired bullet vanished from an existing resume: %q", texts)
	}
}

func TestRetiredFactHiddenFromNewTailoring(t *testing.T) {
	s := testStore(t)
	_, keepID, dropID := seed(t, s)
	if err := s.SetFactRetired(KindBullet, dropID, true); err != nil {
		t.Fatal(err)
	}

	active, err := s.FactBase()
	if err != nil {
		t.Fatal(err)
	}
	all, err := s.FactBaseAll()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(active.Roles[0].Bullets); got != 1 {
		t.Errorf("FactBase should hide the retired bullet: got %d bullets", got)
	}
	if active.Roles[0].Bullets[0].ID != keepID {
		t.Errorf("FactBase kept the wrong bullet")
	}
	if got := len(all.Roles[0].Bullets); got != 2 {
		t.Errorf("FactBaseAll should include the retired bullet: got %d", got)
	}
	for _, b := range all.Roles[0].Bullets {
		if b.ID == dropID && !b.Retired() {
			t.Error("retired bullet not marked Retired()")
		}
	}
}

func TestRetiredFactRejectedByNewResume(t *testing.T) {
	s := testStore(t)
	roleID, _, dropID := seed(t, s)
	if err := s.SetFactRetired(KindBullet, dropID, true); err != nil {
		t.Fatal(err)
	}
	jobID, err := s.AddJob(Job{Company: "Anthropic", Title: "Staff Engineer"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateResume(jobID, "Summary.", "", []ResumeItem{
		{Kind: KindRole, RefID: roleID, Position: 1},
		{Kind: KindBullet, RefID: dropID, Position: 2},
	})
	if err == nil {
		t.Fatal("a retired fact must not be usable in a new resume")
	}
	if !strings.Contains(err.Error(), "retired") {
		t.Errorf("error should explain retirement, got: %v", err)
	}
}

func TestRestoreBringsFactBack(t *testing.T) {
	s := testStore(t)
	_, _, dropID := seed(t, s)
	if err := s.SetFactRetired(KindBullet, dropID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFactRetired(KindBullet, dropID, false); err != nil {
		t.Fatal(err)
	}
	fb, err := s.FactBase()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(fb.Roles[0].Bullets); got != 2 {
		t.Errorf("restore should return the bullet: got %d", got)
	}
}

// TestOneResumePerJob checks that re-tailoring replaces rather than accumulates.
func TestOneResumePerJob(t *testing.T) {
	s := testStore(t)
	roleID, keepID, _ := seed(t, s)
	jobID, err := s.AddJob(Job{Company: "Docker", Title: "Staff Engineer"})
	if err != nil {
		t.Fatal(err)
	}
	items := []ResumeItem{
		{Kind: KindRole, RefID: roleID, Position: 1},
		{Kind: KindBullet, RefID: keepID, Position: 2},
	}
	if _, err := s.CreateResume(jobID, "First.", "", items); err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateResume(jobID, "Second.", "", items)
	if err != nil {
		t.Fatal(err)
	}

	list, err := s.ListResumes(jobID)
	if err != nil {
		t.Fatal(err)
	}
	// Exactly one row, holding the newest content. Note the id is NOT a useful
	// assertion here: resumes.id is an INTEGER PRIMARY KEY, so SQLite is free
	// to reuse the rowid of the row we just deleted, and in practice it does.
	if len(list) != 1 {
		t.Fatalf("want exactly one resume per job, got %d", len(list))
	}
	if list[0].Summary != "Second." {
		t.Errorf("want the newest resume kept, got %q", list[0].Summary)
	}
	if list[0].ID != second.ID {
		t.Error("kept resume is not the one just written")
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM resumes WHERE job_id = ?`, jobID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("resumes table holds %d rows for the job, want 1", count)
	}
	// The replaced resume's items must not linger.
	var orphans int
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM resume_items WHERE resume_id NOT IN (SELECT id FROM resumes)`).
		Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Errorf("%d orphaned resume_items left behind", orphans)
	}
	// Its items must have survived the replace.
	its, err := s.ListResumeItems(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(its) != 2 {
		t.Errorf("want 2 items on the current resume, got %d", len(its))
	}
}

func TestRetireUnknownKindOrID(t *testing.T) {
	s := testStore(t)
	if err := s.SetFactRetired("nonsense", 1, true); err == nil {
		t.Error("unknown kind should error")
	}
	if err := s.SetFactRetired(KindBullet, 9999, true); err == nil {
		t.Error("unknown id should error")
	}
}
