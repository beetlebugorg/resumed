package store

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
)

// Fact-base types. These describe things that are actually true; tailoring may
// select and reword them but never invent new ones.

type Profile struct {
	Name     string `json:"name"`
	Location string `json:"location"`
	Summary  string `json:"summary"`
}

type Contact struct {
	ID       int64  `json:"id"`
	Kind     string `json:"kind"`
	Value    string `json:"value"`
	URL      string `json:"url,omitempty"`
	Position int    `json:"-"`
}

type Role struct {
	ID        int64    `json:"id"`
	Company   string   `json:"company"`
	Location  string   `json:"location,omitempty"`
	Title     string   `json:"title"`
	StartDate string   `json:"start_date,omitempty"`
	EndDate   string   `json:"end_date,omitempty"`
	Summary   string   `json:"summary,omitempty"`
	Position  int      `json:"-"`
	RetiredAt string   `json:"retired_at,omitempty"`
	Bullets   []Bullet `json:"bullets,omitempty"`
}

// Dates in the fact base are free text, "Aug 2023" or "Present", because that
// is what a resume prints. Ordering them means reading that text back.
var monthOrder = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

// dateKey converts a date to a comparable year-month. "Present" returns a
// value above every real date. Text the parser does not recognize returns 0,
// which orders it last.
func dateKey(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.EqualFold(s, "present") || strings.EqualFold(s, "current") {
		return 1 << 30
	}
	var month, year int
	for _, f := range strings.Fields(strings.ReplaceAll(s, ",", " ")) {
		lower := strings.ToLower(f)
		if len(lower) >= 3 {
			if m, ok := monthOrder[lower[:3]]; ok {
				month = m
				continue
			}
		}
		// A four-digit year is the only number in a date line.
		if n, err := strconv.Atoi(f); err == nil && n > 1900 && n < 2200 {
			year = n
		}
	}
	if year == 0 {
		return 0
	}
	return year*100 + month
}

// SortRolesByDate orders roles newest first by start date, then by end date,
// then by stored position.
func SortRolesByDate(roles []Role) {
	slices.SortStableFunc(roles, func(a, b Role) int {
		if c := cmp.Compare(dateKey(b.StartDate), dateKey(a.StartDate)); c != 0 {
			return c
		}
		// Of two roles beginning in the same month, the one that ran later
		// goes first, so a promotion precedes the job it followed.
		if c := cmp.Compare(dateKey(b.EndDate), dateKey(a.EndDate)); c != 0 {
			return c
		}
		return cmp.Compare(a.Position, b.Position)
	})
}

// splitRange separates a project's single date string, "2018 - 2020" or
// "2026 - Present", into start and end. A role stores the two in separate
// columns. The hyphen is tried last so an en dash matches first.
func splitRange(s string) (start, end string) {
	for _, sep := range []string{"–", "—", " to ", "-"} {
		if i := strings.Index(s, sep); i >= 0 {
			return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+len(sep):])
		}
	}
	return strings.TrimSpace(s), ""
}

// SortProjectsByDate orders projects by end date, then start date, then
// stored position. A project that began years before the others and is
// still running sorts below all of them under a start-date order.
func SortProjectsByDate(projects []Project) {
	slices.SortStableFunc(projects, func(a, b Project) int {
		aStart, aEnd := splitRange(a.Date)
		bStart, bEnd := splitRange(b.Date)
		if c := cmp.Compare(dateKey(bEnd), dateKey(aEnd)); c != 0 {
			return c
		}
		if c := cmp.Compare(dateKey(bStart), dateKey(aStart)); c != 0 {
			return c
		}
		return cmp.Compare(a.Position, b.Position)
	})
}

// Dates renders the "Oct 2018 - Aug 2023" form used on the resume.
func (r Role) Dates() string {
	switch {
	case r.StartDate != "" && r.EndDate != "":
		return r.StartDate + " - " + r.EndDate
	case r.StartDate != "":
		return r.StartDate
	default:
		return r.EndDate
	}
}

type Bullet struct {
	ID     int64 `json:"id"`
	RoleID int64 `json:"role_id"`
	// ParentID names a lead-in bullet this one belongs under, or 0. It holds a
	// bullet row id; grouping resolves it through fact_id, so a corrected
	// parent keeps its children.
	ParentID  int64  `json:"parent_id,omitempty"`
	Text      string `json:"text"`
	Tags      string `json:"tags,omitempty"`
	Source    string `json:"source,omitempty"`
	Position  int    `json:"-"`
	RetiredAt string `json:"retired_at,omitempty"`
	// Children is filled by NestBullets for rendering and is never serialized.
	// The fact base is flat, and parent_id is what records the grouping. The
	// json tag also keeps Bullet from referring to itself in a generated
	// schema, which the MCP SDK rejects as a cycle.
	Children []Bullet `json:"-"`
}

// NestBullets groups children under their parents and returns the top level.
// factOf maps a bullet row id to its fact id, so a child written against an
// earlier version of its parent still finds it. A child whose parent is absent
// from the list stays at the top level, which is what happens when tailoring
// selects a child and drops its lead-in.
func NestBullets(bullets []Bullet, factOf map[int64]int64) []Bullet {
	// Flatten first, so nesting an already-nested list returns the same shape
	// instead of dropping the children held on the rows above.
	flat := make([]Bullet, 0, len(bullets))
	for _, b := range bullets {
		children := b.Children
		b.Children = nil
		flat = append(flat, b)
		for _, ch := range children {
			ch.Children = nil
			flat = append(flat, ch)
		}
	}

	at := map[int64]int{}
	var top []Bullet
	for _, b := range flat {
		if b.ParentID != 0 {
			continue
		}
		at[factOf[b.ID]] = len(top)
		top = append(top, b)
	}
	for _, b := range flat {
		if b.ParentID == 0 {
			continue
		}
		i, ok := at[factOf[b.ParentID]]
		if !ok {
			top = append(top, b)
			continue
		}
		top[i].Children = append(top[i].Children, b)
	}
	return top
}

type Project struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	URL       string          `json:"url,omitempty"`
	Date      string          `json:"date,omitempty"`
	Summary   string          `json:"summary,omitempty"`
	Tags      string          `json:"tags,omitempty"`
	Position  int             `json:"-"`
	RetiredAt string          `json:"retired_at,omitempty"`
	Bullets   []ProjectBullet `json:"bullets,omitempty"`
}

type ProjectBullet struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	Text      string `json:"text"`
	Tags      string `json:"tags,omitempty"`
	Source    string `json:"source,omitempty"`
	Position  int    `json:"-"`
	RetiredAt string `json:"retired_at,omitempty"`
}

type Patent struct {
	ID        int64  `json:"id"`
	PatentID  string `json:"patent_id"`
	URL       string `json:"url,omitempty"`
	Date      string `json:"date,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Position  int    `json:"-"`
	RetiredAt string `json:"retired_at,omitempty"`
}

type Skill struct {
	ID        int64  `json:"id"`
	Category  string `json:"category"`
	Name      string `json:"name"`
	Tags      string `json:"tags,omitempty"`
	Position  int    `json:"-"`
	RetiredAt string `json:"retired_at,omitempty"`
}

// FactBase is the whole source of truth, handed to Claude in one call so it can
// tailor without guessing.
type FactBase struct {
	Profile  Profile   `json:"profile"`
	Contacts []Contact `json:"contacts"`
	Roles    []Role    `json:"roles"`
	Projects []Project `json:"projects"`
	Patents  []Patent  `json:"patents"`
	Skills   []Skill   `json:"skills"`
}

// ------------------------------------------------------------ application track

type Job struct {
	ID          int64  `json:"id"`
	URL         string `json:"url,omitempty"`
	Company     string `json:"company,omitempty"`
	Title       string `json:"title,omitempty"`
	Location    string `json:"location,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// Label is a human-readable "Company — Title" for lists and filenames.
func (j Job) Label() string {
	switch {
	case j.Company != "" && j.Title != "":
		return j.Company + " — " + j.Title
	case j.Company != "":
		return j.Company
	case j.Title != "":
		return j.Title
	default:
		return j.URL
	}
}

type JobNote struct {
	ID        int64  `json:"id"`
	JobID     int64  `json:"job_id"`
	Body      string `json:"body"`
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
}

type Question struct {
	ID         int64  `json:"id"`
	JobID      *int64 `json:"job_id,omitempty"`
	Question   string `json:"question"`
	Rationale  string `json:"rationale,omitempty"`
	Answer     string `json:"answer,omitempty"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
	AnsweredAt string `json:"answered_at,omitempty"`
}

type Resume struct {
	ID        int64  `json:"id"`
	JobID     int64  `json:"job_id"`
	Version   int    `json:"version"`
	Summary   string `json:"summary,omitempty"`
	Rationale string `json:"rationale,omitempty"`
	// Highlight lists terms the renderer bolds wherever they appear in the
	// summary and bullet text. Stored newline-separated.
	Highlight []string `json:"highlight,omitempty"`
	Typst     string   `json:"-"`
	// The frozen copy of what was sent. Empty until the job reaches a status
	// that means an application is out in the world.
	SentTypst   string `json:"-"`
	SentPDFPath string `json:"sent_pdf_path,omitempty"`
	SentAt      string `json:"sent_at,omitempty"`
	PDFPath     string `json:"pdf_path,omitempty"`
	CreatedAt   string `json:"created_at"`
}

// CoverLetter is the prose companion to a tailored resume. Unlike a Resume it
// has no item rows: a letter argues rather than selects, so its text is stored
// directly and Rationale carries the audit trail of which facts it leans on.
type CoverLetter struct {
	ID        int64  `json:"id"`
	JobID     int64  `json:"job_id"`
	Version   int    `json:"version"`
	Greeting  string `json:"greeting,omitempty"`
	Body      string `json:"body"`
	Closing   string `json:"closing,omitempty"`
	Rationale string `json:"rationale,omitempty"`
	// Links are printed under the signature.
	Links     []CoverLink `json:"links,omitempty"`
	Typst     string      `json:"-"`
	PDFPath   string      `json:"pdf_path,omitempty"`
	CreatedAt string      `json:"created_at"`
	UpdatedAt string      `json:"updated_at"`
}

// CoverLink is one line under a letter's signature. Label is the link text,
// usually a project name, and Note is the description printed after it.
type CoverLink struct {
	Label string `json:"label,omitempty"`
	URL   string `json:"url"`
	Note  string `json:"note,omitempty"`
}

// Href is the address to link to. A stored link is written the way it should
// read on the page, without a scheme, so the scheme is added here. Both the PDF
// renderer and the web template use this, which keeps one rule in one place and
// keeps html/template out of an ambiguous URL context.
func (l CoverLink) Href() string {
	if strings.Contains(l.URL, "://") {
		return l.URL
	}
	return "https://" + l.URL
}

// joinLinks and splitLinks move links between the slice the program uses and
// the "label|url" lines the column holds. A link with no URL is dropped, since
// a label on its own points nowhere.
func joinLinks(links []CoverLink) string {
	var lines []string
	for _, l := range links {
		url := strings.TrimSpace(l.URL)
		if url == "" {
			continue
		}
		lines = append(lines, strings.TrimSpace(l.Label)+"|"+url+"|"+strings.TrimSpace(l.Note))
	}
	return strings.Join(lines, "\n")
}

func splitLinks(s string) []CoverLink {
	var out []CoverLink
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		var label, url, note string
		switch len(parts) {
		case 1:
			url = parts[0]
		case 2:
			label, url = parts[0], parts[1]
		default:
			label, url, note = parts[0], parts[1], parts[2]
		}
		if url = strings.TrimSpace(url); url != "" {
			out = append(out, CoverLink{
				Label: strings.TrimSpace(label),
				URL:   url,
				Note:  strings.TrimSpace(note),
			})
		}
	}
	return out
}

// Paragraphs splits the stored body on blank lines, which is how the letter is
// authored and how the template lays it out.
func (c CoverLetter) Paragraphs() []string {
	var out []string
	for _, p := range strings.Split(strings.ReplaceAll(c.Body, "\r\n", "\n"), "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, strings.Join(strings.Fields(p), " "))
		}
	}
	return out
}

type ResumeItem struct {
	ID           int64  `json:"id"`
	ResumeID     int64  `json:"resume_id"`
	Kind         string `json:"kind"`
	RefID        int64  `json:"ref_id"`
	ParentRefID  *int64 `json:"parent_ref_id,omitempty"`
	Position     int    `json:"position"`
	OverrideText string `json:"override_text,omitempty"`
}

// Item kinds.
const (
	KindRole          = "role"
	KindBullet        = "bullet"
	KindProject       = "project"
	KindProjectBullet = "project_bullet"
	KindPatent        = "patent"
	KindSkill         = "skill"
)

// Job statuses, in pipeline order.
var JobStatuses = []string{"saved", "tailoring", "applied", "interviewing", "offer", "rejected", "closed"}

// sentStatuses are the states in which an application is live: someone outside
// holds a copy of the resume. A resume is frozen in these states so the file on
// disk keeps matching the file they have. "rejected" and "closed" are absent
// deliberately — once an application is over, re-tailoring for a second attempt
// is reasonable, and the sent snapshot survives regardless.
var sentStatuses = map[string]bool{"applied": true, "interviewing": true, "offer": true}

// IsSentStatus reports whether a job status means the resume has gone out.
func IsSentStatus(status string) bool { return sentStatuses[status] }

// Sent reports whether this resume has a frozen copy of what was sent.
func (r Resume) Sent() bool { return r.SentAt != "" }

// Retired reports whether a fact has been retired from new tailoring. The row
// is kept so previously generated resumes still render.
func (r Role) Retired() bool          { return r.RetiredAt != "" }
func (b Bullet) Retired() bool        { return b.RetiredAt != "" }
func (p Project) Retired() bool       { return p.RetiredAt != "" }
func (b ProjectBullet) Retired() bool { return b.RetiredAt != "" }
func (p Patent) Retired() bool        { return p.RetiredAt != "" }
func (s Skill) Retired() bool         { return s.RetiredAt != "" }

// FactKinds lists every retirable fact kind, for UI and validation.
var FactKinds = []string{KindRole, KindBullet, KindProject, KindProjectBullet, KindPatent, KindSkill}
