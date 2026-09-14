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
				{ID: 4, RoleID: 1, Text: "A bullet with no children."},
			},
		}},
	}
}

// Typst reads indentation as list depth, so a child has to be indented further
// than the "- " of its parent.
func TestTypstIndentsChildBullets(t *testing.T) {
	src := Typst(nestedDoc())

	var parent, child, plain string
	for _, line := range strings.Split(src, "\n") {
		switch {
		case strings.Contains(line, "hardened a cloud platform"):
			parent = line
		case strings.Contains(line, "private network"):
			child = line
		case strings.Contains(line, "no children"):
			plain = line
		}
	}
	if parent == "" || child == "" || plain == "" {
		t.Fatalf("missing bullets in output:\n%s", src)
	}
	indent := func(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }
	if indent(child) <= indent(parent) {
		t.Errorf("child indent %d, parent indent %d: child must be deeper",
			indent(child), indent(parent))
	}
	if indent(plain) != indent(parent) {
		t.Errorf("childless bullet indent %d, want %d to match a parent",
			indent(plain), indent(parent))
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
