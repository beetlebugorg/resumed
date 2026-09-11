package web

import (
	"net/url"
	"testing"

	"github.com/beetlebugorg/resumed/internal/store"
)

func jobs() []store.Job {
	// Order as ListJobs returns it: most recently touched first.
	return []store.Job{
		{ID: 1, Company: "Render", Title: "Infrastructure Engineer", Status: "tailoring"},
		{ID: 2, Company: "Fly", Title: "Infra Ops", Location: "Remote", Status: "saved"},
		{ID: 3, Company: "Anthropic", Title: "Developer Productivity", Status: "applied"},
		{ID: 4, Company: "render", Title: "Engineering Manager", Status: "rejected"},
	}
}

func TestParseListQueryRejectsNonsense(t *testing.T) {
	lq := parseListQuery(url.Values{
		"q":      {"  fly  "},
		"status": {"not-a-status"},
		"sort":   {"sideways"},
	})
	if lq.Q != "fly" {
		t.Errorf("Q = %q, want %q", lq.Q, "fly")
	}
	if lq.Status != "" {
		t.Errorf("Status = %q, want it dropped", lq.Status)
	}
	if lq.Sort != sortRecent {
		t.Errorf("Sort = %q, want %q", lq.Sort, sortRecent)
	}
}

// Defaults are left out so the common address stays readable, and so a link
// copied out of the browser is the short one.
func TestHrefOmitsDefaults(t *testing.T) {
	plain := listQuery{Sort: sortRecent}
	if got := plain.href("/jobs/7", tabResume); got != "/jobs/7" {
		t.Errorf("href = %q, want %q", got, "/jobs/7")
	}
	full := listQuery{Q: "fly", Status: "saved", Sort: sortCompany}
	want := "/jobs/7?q=fly&sort=company&status=saved&tab=notes"
	if got := full.href("/jobs/7", tabNotes); got != want {
		t.Errorf("href = %q, want %q", got, want)
	}
}

func TestBuildJobListSearchIsCaseInsensitive(t *testing.T) {
	v := buildJobList(jobs(), listQuery{Q: "render", Sort: sortRecent}, 0)
	if len(v.Rows) != 2 {
		t.Fatalf("matched %d rows, want 2", len(v.Rows))
	}
	if v.Count != "2 of 4 jobs" {
		t.Errorf("Count = %q, want %q", v.Count, "2 of 4 jobs")
	}
}

// The description is not searched. It matches almost every query, which
// leaves the list as long as it started.
func TestBuildJobListIgnoresDescription(t *testing.T) {
	js := jobs()
	js[0].Description = "we use kubernetes"
	v := buildJobList(js, listQuery{Q: "kubernetes", Sort: sortRecent}, 0)
	if len(v.Rows) != 0 {
		t.Errorf("matched %d rows, want none", len(v.Rows))
	}
}

func TestBuildJobListStatusFilterAndSelection(t *testing.T) {
	v := buildJobList(jobs(), listQuery{Status: "saved", Sort: sortRecent}, 2)
	if len(v.Rows) != 1 || v.Rows[0].Job.ID != 2 {
		t.Fatalf("rows = %+v, want only job 2", v.Rows)
	}
	if !v.Rows[0].Selected {
		t.Error("job 2 is the open one and should be marked selected")
	}
	if v.Rows[0].Href != "/jobs/2?status=saved" {
		t.Errorf("Href = %q, should carry the filter forward", v.Rows[0].Href)
	}
}

// Case is ignored on the company, so "Render" and "render" group together
// instead of falling into separate halves of the list. The title breaks the
// tie.
func TestBuildJobListSortsByCompanyIgnoringCase(t *testing.T) {
	v := buildJobList(jobs(), listQuery{Sort: sortCompany}, 0)
	var got []string
	for _, r := range v.Rows {
		got = append(got, r.Job.Company+"/"+r.Job.Title)
	}
	want := []string{
		"Anthropic/Developer Productivity",
		"Fly/Infra Ops",
		"render/Engineering Manager",
		"Render/Infrastructure Engineer",
	}
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// Grouping follows the pipeline order rather than the alphabet, so the view
// shows where applications are stacked up. Empty statuses are omitted.
func TestBuildJobListGroupsInPipelineOrder(t *testing.T) {
	v := buildJobList(jobs(), listQuery{Sort: sortStatus}, 0)
	if len(v.Rows) != 0 {
		t.Error("grouped lists put their rows in Groups, not Rows")
	}
	var got []string
	for _, g := range v.Groups {
		got = append(got, g.Name)
	}
	want := []string{"saved", "tailoring", "applied", "rejected"}
	if len(got) != len(want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("groups = %v, want %v", got, want)
		}
	}
}

func TestCountLine(t *testing.T) {
	for _, c := range []struct {
		shown, total int
		want         string
	}{
		{4, 4, "4 jobs"},
		{1, 1, "1 job"},
		{0, 4, "0 of 4 jobs"},
	} {
		if got := countLine(c.shown, c.total); got != c.want {
			t.Errorf("countLine(%d, %d) = %q, want %q", c.shown, c.total, got, c.want)
		}
	}
}

// Every page and fragment has to parse and execute; a template that only fails
// at render time fails in front of the user.
func TestTemplatesParse(t *testing.T) {
	if _, err := newServer(nil, ""); err != nil {
		t.Fatal(err)
	}
}
