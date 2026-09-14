package render

import (
	"os"
	"strings"
	"testing"

	"github.com/beetlebugorg/resumed/internal/store"
)

// nestedDoc is one role whose lead-in bullet groups two children, which is how
// a client engagement inside a longer role reads.
func nestedDoc() *store.Document {
	return &store.Document{
		Profile: store.Profile{Name: "Pat Example", Location: "Springfield, IL"},
		Roles: []store.Role{{
			ID: 1, Company: "Northwind Systems", Location: "Springfield, IL",
			Title: "Principal Engineer", StartDate: "Aug 2023", EndDate: "Present",
			Bullets: []store.Bullet{
				{ID: 1, RoleID: 1, Text: "Client engagement (Apr-Oct 2025): hardened a cloud platform for an audit.", Children: []store.Bullet{
					{ID: 2, RoleID: 1, ParentID: 1, Text: "Moved every service into a private network."},
					{ID: 3, RoleID: 1, ParentID: 1, Text: "Restricted database access to a VPN connection."},
				}},
				{ID: 4, RoleID: 1, Text: "GitOps on Terraform and GitHub Actions.", Children: []store.Bullet{
					{ID: 5, RoleID: 1, ParentID: 4, Text: "An ephemeral environment per pull request.", Children: []store.Bullet{
						{ID: 6, RoleID: 1, ParentID: 5, Text: "Scaled to zero when idle."},
					}},
				}},
				{ID: 7, RoleID: 1, Text: "A bullet with no children."},
			},
		}},
	}
}

// Typst reads indentation as list depth, so a child has to be indented further
// than the "- " of its parent.
func TestTypstIndentsChildBullets(t *testing.T) {
	src := Typst(nestedDoc())

	find := func(want string) string {
		for _, line := range strings.Split(src, "\n") {
			if strings.Contains(line, want) {
				return line
			}
		}
		t.Fatalf("no bullet matching %q in output:\n%s", want, src)
		return ""
	}
	indent := func(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

	// Three levels, each deeper than the one above it.
	top := indent(find("hardened a cloud platform"))
	second := indent(find("private network"))
	third := indent(find("Scaled to zero"))
	if !(top < second && second < third) {
		t.Errorf("indents are %d, %d, %d: each level must be deeper", top, second, third)
	}
	if got := indent(find("no children")); got != top {
		t.Errorf("childless bullet indent %d, want %d to match a lead-in", got, top)
	}
	if got := indent(find("ephemeral environment")); got != second {
		t.Errorf("second-level bullet indent %d, want %d", got, second)
	}
}

// The nested list has to survive the real compiler, not just look right.
func TestNestedBulletsCompile(t *testing.T) {
	if !Available() {
		t.Skip("typst not on PATH")
	}
	dir := t.TempDir()
	typPath, err := Write(dir, "nested-resume", Typst(nestedDoc()))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	pdfPath, err := PDF(typPath)
	if err != nil {
		src, _ := os.ReadFile(typPath)
		t.Fatalf("compile: %v\n--- source ---\n%s", err, src)
	}
	info, err := os.Stat(pdfPath)
	if err != nil {
		t.Fatalf("stat pdf: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("compiled PDF is empty")
	}
}
