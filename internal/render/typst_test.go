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

// nastyDoc builds a document whose every text field is drawn from nasty, so a
// test that compiles it exercises escaping on every call site at once.
func nastyDoc() *store.Document {
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
	return doc
}

func TestTypstCompilesWithSpecialCharacters(t *testing.T) {
	if !Available() {
		t.Skip("typst not on PATH")
	}

	doc := nastyDoc()
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

// TestHighlightWrapsTerms covers the matching rules emphasis depends on:
// case-insensitivity, word boundaries, longest-term-wins, and terms that begin
// or end with punctuation.
func TestHighlightWrapsTerms(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		terms []string
		want  string
	}{{
		name:  "plain match",
		text:  "Ran GKE in production",
		terms: []string{"GKE"},
		want:  "Ran #box[#strong[GKE]] in production",
	}, {
		name:  "case insensitive, original casing kept",
		text:  "Brought it to Google Cloud",
		terms: []string{"google cloud"},
		want:  "Brought it to #box[#strong[Google Cloud]]",
	}, {
		name:  "longest term wins over a substring of it",
		text:  "Brought it to Google Cloud",
		terms: []string{"Cloud", "Google Cloud"},
		want:  "Brought it to #box[#strong[Google Cloud]]",
	}, {
		name:  "word boundary keeps a term out of a longer word",
		text:  "AWS is not AWSome",
		terms: []string{"AWS"},
		want:  "#box[#strong[AWS]] is not AWSome",
	}, {
		name:  "punctuation-edged term needs no word boundary",
		text:  "spend was about $300,000 a month.",
		terms: []string{"$300,000 a month"},
		want:  `spend was about #box[#strong[\$300,000 a month]].`,
	}, {
		name:  "every occurrence is wrapped",
		text:  "GKE here and GKE there",
		terms: []string{"GKE"},
		want:  "#box[#strong[GKE]] here and #box[#strong[GKE]] there",
	}, {
		name:  "no terms leaves text untouched",
		text:  "Ran GKE in production",
		terms: nil,
		want:  "Ran GKE in production",
	}, {
		name:  "a term that matches nothing is harmless",
		text:  "Ran GKE in production",
		terms: []string{"BigQuery"},
		want:  "Ran GKE in production",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := escTerms(c.text, c.terms); got != c.want {
				t.Errorf("\n want: %q\n  got: %q", c.want, got)
			}
		})
	}
}

// TestHighlightCannotInjectMarkup is the important one. Emphasis must not
// become a way around contentEscapes: whatever a term matches is escaped
// exactly as it would have been unhighlighted, so the only thing a term can do
// is make true text bold.
func TestHighlightCannotInjectMarkup(t *testing.T) {
	for _, text := range nasty {
		plain := esc(text)
		// A term matching the entire string is the strongest case: everything
		// that could be mangled is inside the emphasised span.
		got := escTerms(text, []string{text})
		if !strings.Contains(got, "#strong[") {
			continue // term did not match (e.g. text is empty after trimming)
		}
		inner := got
		if strings.HasPrefix(inner, "#box[") {
			inner = strings.TrimSuffix(strings.TrimPrefix(inner, "#box["), "]")
		}
		inner = strings.TrimSuffix(strings.TrimPrefix(inner, "#strong["), "]")
		if unescape(inner) != strings.TrimSpace(text) {
			t.Errorf("emphasised text changed:\n want: %q\n  got: %q", text, unescape(inner))
		}
		// A leading list/heading character is neutralised by the #strong[
		// prefix, so the backslash guard is correctly absent there.
		if strings.HasPrefix(plain, `\`) && strings.HasPrefix(got, `\`) {
			t.Errorf("double-guarded %q: %q", text, got)
		}
	}
}

// TestHighlightedDocumentCompiles proves typst accepts a document carrying
// emphasis, not just that the string looks right.
func TestHighlightedDocumentCompiles(t *testing.T) {
	if !Available() {
		t.Skip("typst not on PATH")
	}
	doc := nastyDoc()
	// Terms chosen to land inside the nasty strings, including one that starts
	// with a character Typst treats as markup.
	doc.Resume = &store.Resume{Highlight: []string{"C++", "$3m", "80%", "img-proxy", "Go/Golang"}}

	dir := t.TempDir()
	typPath, err := Write(dir, "highlighted", Typst(doc))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	src, _ := os.ReadFile(typPath)
	if !strings.Contains(string(src), "#strong[") {
		t.Fatal("no emphasis reached the generated source")
	}
	if _, err := PDF(typPath); err != nil {
		t.Fatalf("compile: %v\n--- source ---\n%s", err, src)
	}
}

// TestShortHighlightsDoNotBreak covers the ATS hazard that motivated boxing:
// a hyphenated keyword broken across lines loses its hyphen in extraction, so
// short terms are made unbreakable while long ones stay breakable.
func TestShortHighlightsDoNotBreak(t *testing.T) {
	got := escTerms("meet the AWS Well-Architected Framework here", []string{"AWS Well-Architected Framework"})
	if !strings.Contains(got, "#box[#strong[AWS Well-Architected Framework]]") {
		t.Errorf("short term not boxed: %q", got)
	}

	long := "a self-service infrastructure platform written in Java on top of the platform frameworks"
	got = escTerms("Built "+long+" last year", []string{long})
	if strings.Contains(got, "#box[") {
		t.Errorf("long phrase should stay breakable: %q", got)
	}
	if !strings.Contains(got, "#strong[") {
		t.Errorf("long phrase lost its emphasis: %q", got)
	}
}

// TestContentHashIsStableAcrossRenders checks the reason the render time and
// the hash are both written as placeholders before hashing: two renders of the
// same document produce the same content hash, so the value identifies the
// document rather than the moment it was built.
func TestContentHashIsStableAcrossRenders(t *testing.T) {
	doc := nastyDoc()
	doc.Resume = &store.Resume{ID: 42}
	doc.Job = &store.Job{ID: 7, Company: "Example & Co", Title: "Staff Engineer"}

	first := Typst(doc)
	second := Typst(doc)

	hashOf := func(src string) string {
		i := strings.Index(src, "content:")
		if i < 0 {
			t.Fatalf("no content hash in source")
		}
		return src[i : i+len("content:")+16]
	}
	if hashOf(first) != hashOf(second) {
		t.Errorf("hash changed between renders: %s vs %s", hashOf(first), hashOf(second))
	}
	if strings.Contains(first, hashPlaceholder) || strings.Contains(first, timePlaceholder) {
		t.Error("a placeholder survived into the output")
	}

	// Changing the document changes the hash.
	doc.Summary = doc.Summary + " Extra sentence."
	if hashOf(Typst(doc)) == hashOf(first) {
		t.Error("hash did not change when the document did")
	}
}

// TestMetadataNamesTheJob covers what the keywords are for: finding the render
// behind a PDF someone sent weeks ago.
func TestMetadataNamesTheJob(t *testing.T) {
	doc := nastyDoc()
	doc.Resume = &store.Resume{ID: 42}
	doc.Job = &store.Job{ID: 7, Company: "Example & Co", Title: "Staff Engineer"}
	src := Typst(doc)

	for _, want := range []string{"resume:42", "job:7", "company:Example & Co", "role:Staff Engineer"} {
		if !strings.Contains(src, want) {
			t.Errorf("keywords missing %q", want)
		}
	}
	if !strings.Contains(src, `title: "Pat Example - Resume - Example & Co - Staff Engineer"`) {
		t.Error("title does not name the candidate, the document, and the job")
	}
}
