package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/beetlebugorg/resumed/internal/store"
)

// testServer wires the real mux over a real database, so the path patterns and
// the store both get exercised rather than stubbed. The server opens and closes
// the database per request, so assertions get their own connection.
func testServer(t *testing.T) (http.Handler, func() *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	open := func() *store.Store {
		st, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		return st
	}
	s, err := newServer(func() (*store.Store, error) { return store.Open(path) }, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s.withStore(s.routes()), open
}

func seedFacts(t *testing.T, open func() *store.Store) (roleID, bulletID int64) {
	t.Helper()
	st := open()
	if err := st.SetProfile(store.Profile{Name: "Pat Example"}); err != nil {
		t.Fatal(err)
	}
	roleID, err := st.AddRole(store.Role{
		Company: "Northwind Systems", Title: "Staff Engineer",
		StartDate: "Mar 2021", EndDate: "Present",
	})
	if err != nil {
		t.Fatal(err)
	}
	bulletID, err = st.AddBullet(store.Bullet{RoleID: roleID, Text: "Built the deploy console."})
	if err != nil {
		t.Fatal(err)
	}
	return roleID, bulletID
}

func do(t *testing.T, h http.Handler, method, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if form == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Editing a bullet in the browser has to behave exactly like update_facts over
// MCP: a new version, with the old wording left on the row existing resumes
// cite.
func TestSaveFactVersionsRatherThanOverwrites(t *testing.T) {
	h, open := testServer(t)
	_, bulletID := seedFacts(t, open)

	rec := do(t, h, "POST", "/facts/bullet/1/save", url.Values{
		"text": {"Built the deploy console for 40 teams."},
		"tags": {"platform"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save returned %d: %s", rec.Code, rec.Body.String())
	}

	fb, err := open().FactBase()
	if err != nil {
		t.Fatal(err)
	}
	bullets := fb.Roles[0].Bullets
	if len(bullets) != 1 {
		t.Fatalf("want one current bullet, got %d", len(bullets))
	}
	if bullets[0].Text != "Built the deploy console for 40 teams." {
		t.Errorf("correction not saved: %q", bullets[0].Text)
	}
	if bullets[0].ID == bulletID {
		t.Error("edited in place instead of adding a version")
	}
	// The swapped-in row must carry the new id, or the next edit from this page
	// would target the version it just replaced.
	if !strings.Contains(rec.Body.String(), "for 40 teams") {
		t.Errorf("response did not show the corrected text: %s", rec.Body.String())
	}
}

// An unchanged submit should not manufacture a version.
func TestSaveFactWithNoChangeIsANoOp(t *testing.T) {
	h, open := testServer(t)
	_, bulletID := seedFacts(t, open)

	rec := do(t, h, "POST", "/facts/bullet/1/save", url.Values{
		"text": {"Built the deploy console."},
		"tags": {""},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save returned %d", rec.Code)
	}
	fb, _ := open().FactBase()
	if got := fb.Roles[0].Bullets[0].ID; got != bulletID {
		t.Errorf("a no-op edit created version %d", got)
	}
}

// Correcting a role must not strand its bullets. This is the web-facing half of
// the store fix: the role gets a new row id, the bullets keep the old one, and
// the page still has to show them under the role.
func TestSaveRoleKeepsItsBullets(t *testing.T) {
	h, open := testServer(t)
	_, bulletID := seedFacts(t, open)

	rec := do(t, h, "POST", "/facts/role/1/save", url.Values{
		"company": {"Northwind Systems"},
		"title":   {"Principal Engineer"},
		"summary": {""}, "location": {""},
		"start_date": {"Mar 2021"}, "end_date": {"Present"},
	})
	// A container redraws the page rather than patching itself in place.
	if rec.Code != http.StatusNoContent || rec.Header().Get("HX-Refresh") != "true" {
		t.Fatalf("want a refresh response, got %d %q", rec.Code, rec.Header().Get("HX-Refresh"))
	}

	fb, err := open().FactBase()
	if err != nil {
		t.Fatal(err)
	}
	if len(fb.Roles) != 1 {
		t.Fatalf("want one role, got %d", len(fb.Roles))
	}
	if fb.Roles[0].Title != "Principal Engineer" {
		t.Errorf("correction not saved: %q", fb.Roles[0].Title)
	}
	if len(fb.Roles[0].Bullets) != 1 || fb.Roles[0].Bullets[0].ID != bulletID {
		t.Fatalf("role lost its bullets: %+v", fb.Roles[0].Bullets)
	}

	// And the rendered page shows them, which is what the user actually checks.
	page := do(t, h, "GET", "/facts", nil)
	if !strings.Contains(page.Body.String(), "Built the deploy console.") {
		t.Error("facts page does not show the bullet after the role was corrected")
	}
	if !strings.Contains(page.Body.String(), "Principal Engineer") {
		t.Error("facts page does not show the corrected title")
	}
}

// The facts page shows one row per fact. A correction supersedes a wording, it
// does not add a second fact.
func TestFactsPageShowsOneRowPerFact(t *testing.T) {
	h, open := testServer(t)
	seedFacts(t, open)

	do(t, h, "POST", "/facts/bullet/1/save", url.Values{
		"text": {"Built the deploy console for 40 teams."}, "tags": {""},
	})

	body := do(t, h, "GET", "/facts", nil).Body.String()
	if strings.Contains(body, "Built the deploy console.<") {
		t.Error("superseded wording still listed on the facts page")
	}
	if n := strings.Count(body, "Built the deploy console"); n != 1 {
		t.Errorf("bullet appears %d times, want 1", n)
	}
}

// Retire and restore are routed explicitly. An invented verb must not fall
// through to "restore", which is what a wildcard segment used to do.
func TestUnknownFactVerbIsNotAnAction(t *testing.T) {
	h, open := testServer(t)
	_, bulletID := seedFacts(t, open)
	if err := open().SetFactRetired(store.KindBullet, bulletID, true); err != nil {
		t.Fatal(err)
	}

	rec := do(t, h, "POST", "/facts/bullet/1/unretire", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown verb returned %d, want 404", rec.Code)
	}
	fb, _ := open().FactBaseAll()
	if !fb.Roles[0].Bullets[0].Retired() {
		t.Error("an unknown verb restored the fact")
	}
}

func TestFactEditForBuildsTheRightFields(t *testing.T) {
	fb := &store.FactBase{
		Roles: []store.Role{{
			ID: 7, Company: "Northwind Systems", Title: "Staff Engineer",
			Bullets: []store.Bullet{{ID: 9, Text: "Built it.", Tags: "platform"}},
		}},
		Skills: []store.Skill{{ID: 3, Name: "Go/Golang", Category: "Languages"}},
	}

	role, err := factEditFor(fb, store.KindRole, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !role.Group {
		t.Error("a role is a container and should redraw as a group")
	}
	var names []string
	for _, f := range role.Fields {
		names = append(names, f.Name)
	}
	want := "company title location start_date end_date summary"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("role fields = %q, want %q", got, want)
	}

	bullet, err := factEditFor(fb, store.KindBullet, 9)
	if err != nil {
		t.Fatal(err)
	}
	if bullet.Group {
		t.Error("a bullet is a leaf row, not a group")
	}
	if bullet.Context != "Northwind Systems — Staff Engineer" {
		t.Errorf("bullet context = %q", bullet.Context)
	}
	if bullet.Fields[0].Name != "text" || !bullet.Fields[0].Multi {
		t.Errorf("a bullet's text wants a textarea: %+v", bullet.Fields[0])
	}

	if _, err := factEditFor(fb, store.KindSkill, 404); err == nil {
		t.Error("a missing fact should be an error, not an empty form")
	}
	if _, err := factEditFor(fb, "nonsense", 1); err == nil {
		t.Error("an unknown kind should be rejected")
	}
}
