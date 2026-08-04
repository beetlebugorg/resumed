package app

import (
	"path/filepath"
	"testing"

	"github.com/beetlebugorg/resumed/internal/store"
)

// The rendered artifact's path is stored in the database, so it has to resolve
// from any working directory later. A relative --out used to be recorded
// verbatim, which meant `render --out jobs` then `serve --out ../jobs` left the
// web UI unable to open its own PDF.
func TestJobDirIsAbsoluteEvenWhenOutRootIsRelative(t *testing.T) {
	a := New(nil, "jobs")
	if !filepath.IsAbs(a.OutRoot) {
		t.Fatalf("OutRoot = %q, want an absolute path", a.OutRoot)
	}
	dir := a.JobDir(&store.Job{ID: 1, Company: "Example & Co", Title: "Staff Engineer"})
	if !filepath.IsAbs(dir) {
		t.Errorf("JobDir = %q, want an absolute path", dir)
	}
	if filepath.Base(dir) != "staff-engineer" || filepath.Base(filepath.Dir(dir)) != "example-co" {
		t.Errorf("JobDir = %q, want it to end in example-co/staff-engineer", dir)
	}
}

func TestJobDirFallsBackWhenCompanyOrTitleAreMissing(t *testing.T) {
	a := New(nil, t.TempDir())
	cases := []struct {
		name string
		job  store.Job
		want string
	}{
		{"no title", store.Job{ID: 7, Company: "Example & Co"}, "job-7"},
		{"nothing", store.Job{ID: 9}, "job-9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := filepath.Base(a.JobDir(&tc.job)); got != tc.want {
				t.Errorf("JobDir base = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Engineering Manager, Platform ": "engineering-manager-platform",
		"Example & Co":                   "example-co",
		"C++ / Go":                       "c-go",
		"  spaced  out  ":                "spaced-out",
		"":                               "",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
