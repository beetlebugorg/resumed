// Package render turns an assembled resume Document into Typst source and,
// when the typst binary is available, a PDF.
package render

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/beetlebugorg/resumed/internal/store"
)

// The ATS-hardened template ships inside the binary and is written next to the
// generated .typ so the file compiles standalone.
//
//go:embed ats-resume.typ
var templateTypst string

//go:embed ats-cover-letter.typ
var coverTemplateTypst string

// TemplateName is the filename the generated resume imports.
const TemplateName = "ats-resume.typ"

// CoverTemplateName is the filename the generated cover letter imports. It must
// differ from the generated letter's own filename ("cover-letter.typ") or the
// file imports itself and typst reports a cyclic import.
const CoverTemplateName = "ats-cover-letter.typ"

// Template exposes the embedded template so callers can write it to disk.
func Template() string { return templateTypst }

// CoverTemplate exposes the embedded cover letter template.
func CoverTemplate() string { return coverTemplateTypst }

// quote renders s as a Typst string literal.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// contentEscapes are the markup characters that must be backslash-escaped when
// text is emitted into Typst content mode (bullets, summaries). Typst only
// accepts escapes for characters that are actually special, so this set is
// deliberately exact rather than generous.
var contentEscapes = strings.NewReplacer(
	`\`, `\\`,
	`#`, `\#`,
	`$`, `\$`,
	`*`, `\*`,
	`_`, `\_`,
	"`", "\\`",
	`<`, `\<`,
	`>`, `\>`,
	`@`, `\@`,
	`[`, `\[`,
	`]`, `\]`,
	`~`, `\~`,
	`"`, `\"`,
	`'`, `\'`,
)

// esc escapes text for Typst content mode.
func esc(s string) string { return escTerms(s, nil) }

// escTerms escapes text for Typst content mode, wrapping every occurrence of a
// highlight term in #strong[...]. The escaping is applied to the term text too,
// so emphasis can never be a route around contentEscapes: a term still cannot
// introduce markup, only bold text that was already there.
func escTerms(s string, terms []string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	spans := highlightSpans(s, terms)

	var out string
	if len(spans) == 0 {
		out = contentEscapes.Replace(s)
	} else {
		var b strings.Builder
		prev := 0
		for _, sp := range spans {
			b.WriteString(contentEscapes.Replace(s[prev:sp.lo]))
			// A short term is wrapped in a box so it cannot break across lines.
			// Typesetters break at an existing hyphen, and PDF text extractors
			// then drop it: "AWS Well-Architected Framework" comes back out as
			// "WellArchitected", which an ATS matching the exact phrase misses.
			// Highlight terms are precisely the words the author declared worth
			// matching, so they are the right set to protect. Long phrases are
			// emphasis rather than keywords and must stay breakable, or they
			// would overrun the line.
			boxed := sp.hi-sp.lo <= unbreakableLimit
			if boxed {
				b.WriteString("#box[")
			}
			b.WriteString("#strong[")
			b.WriteString(contentEscapes.Replace(s[sp.lo:sp.hi]))
			b.WriteString("]")
			if boxed {
				b.WriteString("]")
			}
			prev = sp.hi
		}
		b.WriteString(contentEscapes.Replace(s[prev:]))
		out = b.String()
	}

	// A leading -, +, /, or = starts a list or heading; neutralise it. Skipped
	// when the first character is inside a highlight span, because there the
	// text is preceded by "#strong[" and is no longer at the start of a line —
	// prefixing a backslash would escape the # and print it literally.
	if len(spans) > 0 && spans[0].lo == 0 {
		return out
	}
	switch s[0] {
	case '-', '+', '/', '=':
		out = `\` + out
	}
	return out
}

// unbreakableLimit is the longest highlight term kept on one line. Body lines
// hold roughly 95 characters, so 40 leaves room for a boxed term to move to the
// next line rather than overflow the page.
const unbreakableLimit = 40

// span is a half-open byte range of s to render bold.
type span struct{ lo, hi int }

// highlightSpans finds the non-overlapping ranges of s matching any term.
// Matching is case-insensitive over ASCII and respects word boundaries, so
// "AWS" does not match inside "AWSome" while "$300,000 a month" — which begins
// with punctuation — still matches. Longer terms are matched first so that
// "Google Cloud" wins over a bare "Cloud" covering the same text.
func highlightSpans(s string, terms []string) []span {
	if s == "" || len(terms) == 0 {
		return nil
	}
	ordered := make([]string, 0, len(terms))
	for _, t := range terms {
		if t = strings.TrimSpace(t); t != "" {
			ordered = append(ordered, t)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })

	hay := asciiLower(s)
	taken := make([]bool, len(s))
	var spans []span
	for _, t := range ordered {
		needle := asciiLower(t)
		for from := 0; from+len(needle) <= len(hay); {
			i := strings.Index(hay[from:], needle)
			if i < 0 {
				break
			}
			lo := from + i
			hi := lo + len(needle)
			from = lo + 1
			if !wordBounded(s, lo, hi) || overlaps(taken, lo, hi) {
				continue
			}
			for k := lo; k < hi; k++ {
				taken[k] = true
			}
			spans = append(spans, span{lo, hi})
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].lo < spans[j].lo })
	return spans
}

// asciiLower lowercases ASCII letters only. strings.ToLower is unusable here
// because a few Unicode characters change byte length when folded, which would
// desynchronise offsets between the haystack and the original string.
func asciiLower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

func isWordByte(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// wordBounded reports whether s[lo:hi] sits on word boundaries. The check only
// applies on a side where the match's own edge is a word character: a term
// ending in "." or starting with "$" needs no boundary there.
func wordBounded(s string, lo, hi int) bool {
	if lo > 0 && isWordByte(s[lo]) && isWordByte(s[lo-1]) {
		return false
	}
	if hi < len(s) && isWordByte(s[hi-1]) && isWordByte(s[hi]) {
		return false
	}
	return true
}

func overlaps(taken []bool, lo, hi int) bool {
	for k := lo; k < hi; k++ {
		if taken[k] {
			return true
		}
	}
	return false
}

// labelForComment flattens a job label for the generated header comment. The
// em-dash in Job.Label reads fine in the web UI but looks out of place in a
// file a candidate may hand to an employer, so it is normalised here.
var commentLabel = strings.NewReplacer("\n", " ", "\u2014", "-", "\u2013", "-")

func labelForComment(s string) string { return commentLabel.Replace(s) }

// Typst generates the Typst source for a document.
func Typst(doc *store.Document) string {
	var b strings.Builder

	fmt.Fprintf(&b, "// Generated by resumed. Do not edit by hand.\n")
	if doc.Job != nil {
		fmt.Fprintf(&b, "// Tailored for: %s\n", labelForComment(doc.Job.Label()))
		if doc.Job.URL != "" {
			fmt.Fprintf(&b, "// %s\n", doc.Job.URL)
		}
	}
	fmt.Fprintf(&b, "\n#import %s: *\n\n", quote(TemplateName))

	// Terms this posting wants emphasised. Empty for a full fact-base dump.
	var hi []string
	if doc.Resume != nil {
		hi = doc.Resume.Highlight
	}

	// Header.
	b.WriteString("#show: resume.with(\n")
	fmt.Fprintf(&b, "  author: %s,\n", quote(doc.Profile.Name))
	fmt.Fprintf(&b, "  location: %s,\n", quote(doc.Profile.Location))
	b.WriteString("  contacts: (\n")
	for _, c := range doc.Contacts {
		if c.URL != "" {
			fmt.Fprintf(&b, "    [#link(%s)[%s]],\n", quote(c.URL), esc(c.Value))
		} else {
			fmt.Fprintf(&b, "    [%s],\n", esc(c.Value))
		}
	}
	b.WriteString("  ),\n")
	var resumeID int64
	if doc.Resume != nil {
		resumeID = doc.Resume.ID
	}
	title, keywords := docMeta(doc.Profile.Name, "resume", "Resume", doc.Job, resumeID)
	writeMeta(&b, title, keywords)
	b.WriteString(")\n\n")

	if s := strings.TrimSpace(doc.Summary); s != "" {
		b.WriteString("= Summary\n\n")
		b.WriteString(escTerms(s, hi))
		b.WriteString("\n\n")
	}

	if len(doc.Roles) > 0 {
		b.WriteString("= Experience\n\n")
		for _, r := range doc.Roles {
			b.WriteString("#exp(\n")
			fmt.Fprintf(&b, "  company: %s,\n", quote(r.Company))
			if r.Location != "" {
				fmt.Fprintf(&b, "  location: %s,\n", quote(r.Location))
			}
			fmt.Fprintf(&b, "  role: %s,\n", quote(r.Title))
			if d := r.Dates(); d != "" {
				fmt.Fprintf(&b, "  date: %s,\n", quote(d))
			}
			if r.Summary != "" {
				fmt.Fprintf(&b, "  summary: [%s],\n", escTerms(r.Summary, hi))
			}
			if len(r.Bullets) > 0 {
				b.WriteString("  details: [\n")
				for _, bl := range r.Bullets {
					fmt.Fprintf(&b, "    - %s\n", escTerms(bl.Text, hi))
				}
				b.WriteString("  ],\n")
			}
			b.WriteString(")\n\n")
		}
	}

	if len(doc.SkillGroups) > 0 {
		b.WriteString("= Skills\n\n")
		b.WriteString("#skills((\n")
		for _, g := range doc.SkillGroups {
			fmt.Fprintf(&b, "  (%s, (\n", quote(g.Category))
			for _, n := range g.Names {
				// Deliberately not highlighted. skills() already renders the
				// category label bold, so a bold skill sitting next to it reads
				// as one long bold run and the emphasis disappears — worse than
				// no emphasis at all.
				fmt.Fprintf(&b, "    [%s],\n", esc(n))
			}
			b.WriteString("  )),\n")
		}
		b.WriteString("))\n\n")
	}

	if len(doc.Projects) > 0 {
		b.WriteString("= Projects\n\n")
		for _, p := range doc.Projects {
			b.WriteString("#proj(\n")
			fmt.Fprintf(&b, "  name: %s,\n", quote(p.Name))
			if p.URL != "" {
				fmt.Fprintf(&b, "  url: %s,\n", quote(p.URL))
			}
			// Project dates are deliberately not emitted. The fact base only
			// knows them to the year ("2009 - Present"), and ATS guidance
			// treats a year-only range as unsafe. Inventing months to satisfy
			// the format would be a lie, so the date is dropped instead. Real
			// employment dates, which are known to the month, still render.
			if p.Summary != "" {
				fmt.Fprintf(&b, "  summary: [%s],\n", escTerms(p.Summary, hi))
			}
			if len(p.Bullets) > 0 {
				b.WriteString("  details: [\n")
				for _, bl := range p.Bullets {
					fmt.Fprintf(&b, "    - %s\n", escTerms(bl.Text, hi))
				}
				b.WriteString("  ],\n")
			}
			b.WriteString(")\n\n")
		}
	}

	if len(doc.Patents) > 0 {
		b.WriteString("= Patents\n\n")
		for _, p := range doc.Patents {
			b.WriteString("#patent(\n")
			fmt.Fprintf(&b, "  id: %s,\n", quote(p.PatentID))
			if p.URL != "" {
				fmt.Fprintf(&b, "  url: %s,\n", quote(p.URL))
			}
			if p.Date != "" {
				fmt.Fprintf(&b, "  date: %s,\n", quote(p.Date))
			}
			if p.Summary != "" {
				fmt.Fprintf(&b, "  summary: %s,\n", quote(p.Summary))
			}
			b.WriteString(")\n\n")
		}
	}

	return stamp(b.String(), time.Now())
}

// LetterDate renders the stored timestamp as "2 January 2006" for the letter
// head. It uses the letter's own updated_at rather than the wall clock so a
// re-render does not silently re-date a letter that was already sent.
func LetterDate(ts string) string {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return ""
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05Z", "2006-01-02"} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t.Format("January 2, 2006")
		}
	}
	return ""
}

// CoverLetterTypst generates the Typst source for a cover letter. The header
// is built from the same profile and contacts as the resume so the two
// documents read as one set.
func CoverLetterTypst(p store.Profile, contacts []store.Contact, job *store.Job, c *store.CoverLetter) string {
	var b strings.Builder

	fmt.Fprintf(&b, "// Generated by resumed. Do not edit by hand.\n")
	if job != nil {
		fmt.Fprintf(&b, "// Cover letter for: %s\n", labelForComment(job.Label()))
		if job.URL != "" {
			fmt.Fprintf(&b, "// %s\n", job.URL)
		}
	}
	fmt.Fprintf(&b, "\n#import %s: *\n\n", quote(CoverTemplateName))

	b.WriteString("#show: cover-letter.with(\n")
	fmt.Fprintf(&b, "  author: %s,\n", quote(p.Name))
	fmt.Fprintf(&b, "  location: %s,\n", quote(p.Location))
	b.WriteString("  contacts: (\n")
	for _, ct := range contacts {
		if ct.URL != "" {
			fmt.Fprintf(&b, "    [#link(%s)[%s]],\n", quote(ct.URL), esc(ct.Value))
		} else {
			fmt.Fprintf(&b, "    [%s],\n", esc(ct.Value))
		}
	}
	b.WriteString("  ),\n")

	if d := LetterDate(c.UpdatedAt); d != "" {
		fmt.Fprintf(&b, "  date: %s,\n", quote(d))
	}

	// The recipient block carries only what the posting actually told us.
	// Inventing a hiring manager's name is exactly the kind of fabrication
	// this tool exists to prevent.
	var recipient []string
	if job != nil {
		if job.Company != "" {
			recipient = append(recipient, strings.TrimSpace(job.Company))
		}
		if job.Title != "" {
			recipient = append(recipient, "Re: "+strings.TrimSpace(job.Title))
		}
	}
	if len(recipient) > 0 {
		b.WriteString("  recipient: (\n")
		for _, line := range recipient {
			fmt.Fprintf(&b, "    [%s],\n", esc(line))
		}
		b.WriteString("  ),\n")
	}

	if c.Greeting != "" {
		fmt.Fprintf(&b, "  greeting: [%s],\n", esc(c.Greeting))
	}
	if c.Closing != "" {
		fmt.Fprintf(&b, "  closing: [%s],\n", esc(c.Closing))
	}
	title, keywords := docMeta(p.Name, "cover-letter", "Cover Letter", job, c.ID)
	writeMeta(&b, title, keywords)
	b.WriteString(")\n\n")

	for _, para := range c.Paragraphs() {
		b.WriteString(esc(para))
		b.WriteString("\n\n")
	}

	return stamp(b.String(), time.Now())
}

// ErrNoTypst reports that the typst binary is not on PATH. Callers treat this
// as non-fatal: the .typ source is still written and saved.
var ErrNoTypst = fmt.Errorf("typst binary not found on PATH (install with: brew install typst)")

// Available reports whether typst can be invoked.
func Available() bool {
	_, err := exec.LookPath("typst")
	return err == nil
}

// Write writes the generated .typ (and the resume template beside it) into dir
// and returns the path to the .typ file.
func Write(dir, base, src string) (string, error) {
	return WriteWith(dir, base, src, TemplateName, templateTypst)
}

// WriteWith is Write with an explicit companion template, so cover letters can
// ship their own alongside the generated source. Both files land in dir, which
// is what lets the generated .typ compile standalone.
func WriteWith(dir, base, src, tmplName, tmplSrc string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmplPath := filepath.Join(dir, tmplName)
	if err := os.WriteFile(tmplPath, []byte(tmplSrc), 0o644); err != nil {
		return "", fmt.Errorf("write template: %w", err)
	}
	typPath := filepath.Join(dir, base+".typ")
	if err := os.WriteFile(typPath, []byte(src), 0o644); err != nil {
		return "", fmt.Errorf("write typst source: %w", err)
	}
	return typPath, nil
}

// PDF compiles typPath to a PDF next to it and returns the PDF path.
func PDF(typPath string) (string, error) {
	if !Available() {
		return "", ErrNoTypst
	}
	pdfPath := strings.TrimSuffix(typPath, ".typ") + ".pdf"
	cmd := exec.Command("typst", "compile", "--root", filepath.Dir(typPath), typPath, pdfPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("typst compile failed: %w\n%s", err, stderr.String())
	}
	// A PDF without /Producer is still a valid PDF, so a failure here does not
	// fail the render.
	_ = setProducer(pdfPath, Producer)
	return pdfPath, nil
}

// ---------------------------------------------------------------- provenance

// Placeholders stand in for the two metadata values that cannot be known while
// the source is being written: the content hash cannot hash a string it is
// already part of, and the render time changes on every run, which would make
// the hash differ for identical documents. Both are substituted afterwards, and
// the hash is taken over the source while both are still placeholders. Two
// renders of the same document therefore produce the same content hash.
const (
	hashPlaceholder = "0000000000000000"
	timePlaceholder = "0000-00-00T00:00:00Z"
)

// stamp substitutes the real hash and render time into a finished source.
func stamp(src string, now time.Time) string {
	sum := sha256.Sum256([]byte(src))
	src = strings.Replace(src, hashPlaceholder, hex.EncodeToString(sum[:8]), 1)
	return strings.Replace(src, timePlaceholder, now.UTC().Format(time.RFC3339), 1)
}

// docMeta builds the title and keywords written into the PDF. The keywords are
// key:value pairs so a reader can tell what each one is without a legend.
func docMeta(name, kind, display string, job *store.Job, id int64) (string, []string) {
	title := name + " - " + display
	keywords := []string{
		"resumed",
		kind + ":" + strconv.FormatInt(id, 10),
		"rendered:" + timePlaceholder,
		"content:" + hashPlaceholder,
	}
	if job != nil {
		// Job.Label joins with an em dash, which reads oddly next to the
		// hyphens already in the title.
		title += " - " + labelForComment(job.Label())
		keywords = append(keywords, "job:"+strconv.FormatInt(job.ID, 10))
		if job.Company != "" {
			keywords = append(keywords, "company:"+job.Company)
		}
		if job.Title != "" {
			keywords = append(keywords, "role:"+job.Title)
		}
	}
	return title, keywords
}

// writeMeta emits the title and keywords arguments shared by both templates.
func writeMeta(b *strings.Builder, title string, keywords []string) {
	fmt.Fprintf(b, "  title: %s,\n", quote(title))
	b.WriteString("  keywords: (\n")
	for _, k := range keywords {
		fmt.Fprintf(b, "    %s,\n", quote(k))
	}
	b.WriteString("  ),\n")
}
