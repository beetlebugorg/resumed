package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/beetlebugorg/resumed/internal/store"
)

// nasty holds the characters that actually appear in resume bullets and mean
// something to Typst. A resume that silently drops "C++" or turns "#1 tool"
// into a heading is worse than one that fails to build, so this compiles for
// real and checks the text survives.
var nasty = []string{
	"Built img-proxy, a C module handling 20B+ image pulls",
	"Reduced costs by 80% — from $3m to $600k/year",
	"Wrote C++ and C# services; used *nix tooling",
	"Scaled to #1 in throughput (p99 < 50ms, > 99.9% uptime)",
	"Migrated [legacy] systems @ Contoso — see <docs//notes>",
	`Handled "quoted" text and 'apostrophes' plus a backslash \ here`,
	"- leading dash that must not become a nested list",
	"= leading equals that must not become a heading",
	"+ leading plus, / leading slash",
	"Used ~50 servers with a b~c tilde",
	"Backtick `code` and $math$ and _underscores_",
}

func TestTypstCompilesWithSpecialCharacters(t *testing.T) {
	if !Available() {
		t.Skip("typst not on PATH")
	}

	bullets := make([]store.Bullet, len(nasty))
	for i, text := range nasty {
		bullets[i] = store.Bullet{ID: int64(i + 1), RoleID: 1, Text: text}
	}

	doc := &store.Document{
		Profile: store.Profile{
			Name:     "Pat Example",
			Location: "Springfield, IL",
			Summary:  strings.Join(nasty, " "),
		},
		Contacts: []store.Contact{
			{Kind: "email", Value: "pat@example.com", URL: "mailto:pat@example.com"},
		},
		Roles: []store.Role{{
			ID: 1, Company: "Example & Co", Location: "Chicago, IL",
			Title: "CTO — Managed Services", StartDate: "Oct 2018", EndDate: "Aug 2023",
			Summary: "Ran 100+ environments (AWS/GCP/Azure)",
			Bullets: bullets,
		}},
		SkillGroups: []store.SkillGroup{
			{Category: "Languages", Names: []string{"Go/Golang", "C++", "C#", "F*"}},
		},
		Projects: []store.Project{{
			ID: 1, Name: "img-proxy", URL: "github.com/example/img-proxy",
			Date: "2009 - Present", Summary: "Image resizing — 100% C",
			Bullets: []store.ProjectBullet{{ID: 1, ProjectID: 1, Text: "Serves #1 traffic @ scale"}},
		}},
		Patents: []store.Patent{{
			ID: 1, PatentID: "US11831495B2", URL: "https://patents.google.com/patent/US11831495B2",
			Date: "2023", Summary: "Hierarchical cloud resource configuration",
		}},
	}

	dir := t.TempDir()
	typPath, err := Write(dir, "test-resume", Typst(doc))
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

	// The template must land beside the source, or the .typ will not compile
	// on its own later.
	if _, err := os.Stat(filepath.Join(dir, TemplateName)); err != nil {
		t.Errorf("template not written next to source: %v", err)
	}
}

// unescape reverses esc: drop the backslash in front of any escaped
// character. Typst has no letter escapes, so a backslash before a normal
// character is left alone.
func unescape(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == '\\' && i+1 < len(rs) && strings.ContainsRune(`\#$*_`+"`"+`<>@[]~"'-+/=`, rs[i+1]) {
			continue
		}
		b.WriteRune(rs[i])
	}
	return b.String()
}

// TestEscapeIsLossless proves escaping never drops or mangles a character.
// The compile test above shows typst accepts the output; this shows the output
// still says what the fact base said.
func TestEscapeIsLossless(t *testing.T) {
	for _, want := range nasty {
		if got := unescape(esc(want)); got != want {
			t.Errorf("round trip changed text:\n want: %q\n  got: %q\n  esc: %q", want, got, esc(want))
		}
	}
}

// TestEscapeNeutralisesMarkup checks the specific ways a bullet could hijack
// the document: becoming a heading, a nested list, or bold/italic runs.
func TestEscapeNeutralisesMarkup(t *testing.T) {
	cases := map[string]string{
		"= Heading":    `\= Heading`,
		"- nested":     `\- nested`,
		"+ numbered":   `\+ numbered`,
		"/ term":       `\/ term`,
		"*bold*":       `\*bold\*`,
		"_italic_":     `\_italic\_`,
		"#import evil": `\#import evil`,
		"C++ and C#":   `C++ and C\#`,
		"a@b.com":      `a\@b.com`,
		"<label>":      `\<label\>`,
		"mod_dims":     `mod\_dims`,
		"80% of $3m":   `80% of \$3m`,
		"[bracket]":    `\[bracket\]`,
		"back\\slash":  `back\\slash`,
		"~tilde":       `\~tilde`,
		"`code`":       "\\`code\\`",
	}
	for in, want := range cases {
		if got := esc(in); got != want {
			t.Errorf("esc(%q) = %q, want %q", in, got, want)
		}
	}
}
