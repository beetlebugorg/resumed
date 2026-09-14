package store

import "testing"

func TestDateKeyReadsResumeDates(t *testing.T) {
	if dateKey("Aug 2023") >= dateKey("Present") {
		t.Error("an ongoing role should sort above every dated one")
	}
	if dateKey("Aug 2023") <= dateKey("Jul 2023") {
		t.Error("later month should sort higher within a year")
	}
	if dateKey("Jan 2024") <= dateKey("Dec 2023") {
		t.Error("later year should sort higher across a year boundary")
	}
	if dateKey("September 2009") != dateKey("Sep 2009") {
		t.Error("a full month name should read the same as its abbreviation")
	}
	for _, s := range []string{"", "   ", "sometime", "the 90s"} {
		if got := dateKey(s); got != 0 {
			t.Errorf("dateKey(%q) = %d, want 0 for an unreadable date", s, got)
		}
	}
	// A bare year is still orderable, just without a month.
	if dateKey("2015") == 0 {
		t.Error("a bare year should be readable")
	}
	if dateKey("2015") >= dateKey("Feb 2015") {
		t.Error("a bare year should sort below any month within it")
	}
}

// The stored order after an import: the first six rows have a position and
// every row added since has 0.
func TestSortRolesByDateFixesTheRealOrder(t *testing.T) {
	roles := []Role{
		{ID: 15, Company: "Northwind", StartDate: "Aug 2023", EndDate: "Present"},
		{ID: 12, Company: "Globex", StartDate: "Jul 2000", EndDate: "May 2005"},
		{ID: 13, Company: "Initrode", StartDate: "Aug 1999", EndDate: "Jul 2000"},
		{ID: 7, Company: "Contoso", Title: "VP Engineering", StartDate: "Nov 2016", EndDate: "Aug 2023", Position: 1},
		{ID: 2, Company: "Contoso", Title: "VP Platform", StartDate: "Oct 2018", EndDate: "Aug 2023", Position: 2},
		{ID: 3, Company: "Contoso", Title: "Director of Engineering", StartDate: "Nov 2016", EndDate: "Oct 2018", Position: 3},
		{ID: 4, Company: "Contoso", Title: "Staff Engineer", StartDate: "Apr 2014", EndDate: "Nov 2016", Position: 4},
		{ID: 5, Company: "Contoso", Title: "Architect", StartDate: "Oct 2009", EndDate: "Apr 2014", Position: 5},
		{ID: 6, Company: "Initech", StartDate: "May 2005", EndDate: "Oct 2009", Position: 6},
	}
	SortRolesByDate(roles)

	want := []int64{15, 2, 7, 3, 4, 5, 6, 12, 13}
	for i, id := range want {
		if roles[i].ID != id {
			t.Fatalf("position %d = role %d, want %d\ngot order: %v", i, roles[i].ID, id, ids(roles))
		}
	}
}

// Two roles beginning in the same month order by end date.
func TestSortRolesByDateBreaksTiesOnEndDate(t *testing.T) {
	roles := []Role{
		{ID: 3, StartDate: "Nov 2016", EndDate: "Oct 2018"},
		{ID: 7, StartDate: "Nov 2016", EndDate: "Aug 2023"},
	}
	SortRolesByDate(roles)
	if roles[0].ID != 7 {
		t.Errorf("got %v, want the longer-running role first", ids(roles))
	}
}

// An undated role does not displace a dated one.
func TestSortRolesByDateKeepsUndatedRolesLast(t *testing.T) {
	roles := []Role{
		{ID: 1, Company: "Undated"},
		{ID: 2, Company: "Old", StartDate: "Aug 1999"},
	}
	SortRolesByDate(roles)
	if roles[0].ID != 2 {
		t.Errorf("got %v, want the dated role first", ids(roles))
	}
}

func ids(roles []Role) []int64 {
	out := make([]int64, len(roles))
	for i, r := range roles {
		out[i] = r.ID
	}
	return out
}

func TestSplitRangeRecoversBothHalves(t *testing.T) {
	for _, tc := range []struct{ in, start, end string }{
		{"2026 - Present", "2026", "Present"},
		{"2018 - 2020", "2018", "2020"},
		{"2018–2020", "2018", "2020"}, // en dash
		{"Aug 2023 to Present", "Aug 2023", "Present"},
		{"2024", "2024", ""}, // a single date is the start
		{"", "", ""},
	} {
		start, end := splitRange(tc.in)
		if start != tc.start || end != tc.end {
			t.Errorf("splitRange(%q) = (%q, %q), want (%q, %q)", tc.in, start, end, tc.start, tc.end)
		}
	}
}

// Projects order on end date, so a project still running stays above a newer
// one that finished.
func TestSortProjectsByDateRanksLiveWorkFirst(t *testing.T) {
	projects := []Project{
		{ID: 1, Name: "atlas", Date: "2026 - Present"},
		{ID: 6, Name: "relay", Date: "2018 - 2020"},
		{ID: 7, Name: "harbor", Date: "2026 - Present"},
		{ID: 8, Name: "gridmap", Date: "2026 - Present"},
		{ID: 2, Name: "tileset", Date: "2026 - Present", Position: 1},
		{ID: 3, Name: "vendor-mcp", Date: "2025 - Present", Position: 2},
		{ID: 4, Name: "go-thumb", Date: "2024 - Present", Position: 3},
		{ID: 5, Name: "thumbd", Date: "2009 - Present", Position: 4},
	}
	SortProjectsByDate(projects)

	want := []string{
		"atlas", "harbor", "gridmap", "tileset",
		"vendor-mcp", "go-thumb", "thumbd", "relay",
	}
	for i, name := range want {
		if projects[i].Name != name {
			t.Fatalf("position %d = %q, want %q\ngot order: %v", i, projects[i].Name, name, names(projects))
		}
	}
}

func names(projects []Project) []string {
	out := make([]string, len(projects))
	for i, p := range projects {
		out[i] = p.Name
	}
	return out
}
