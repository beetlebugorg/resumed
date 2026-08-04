package store

import "strings"

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
	ID        int64  `json:"id"`
	RoleID    int64  `json:"role_id"`
	Text      string `json:"text"`
	Tags      string `json:"tags,omitempty"`
	Source    string `json:"source,omitempty"`
	Position  int    `json:"-"`
	RetiredAt string `json:"retired_at,omitempty"`
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
	Typst     string `json:"-"`
	PDFPath   string `json:"pdf_path,omitempty"`
	CreatedAt string `json:"created_at"`
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
	Typst     string `json:"-"`
	PDFPath   string `json:"pdf_path,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
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
