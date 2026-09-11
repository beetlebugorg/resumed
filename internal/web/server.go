// Package web serves the human-facing side of resumed: browse tracked jobs,
// read and answer the questions Claude raised, and open generated resumes.
//
// The UI is server-rendered HTML driven by htmx. Every mutating endpoint
// returns the fragment it just changed, so the same handler serves a full page
// on a cold load and a partial on an htmx request — there is no JSON API and no
// client-side state to keep in sync.
package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/beetlebugorg/resumed/internal/app"
	"github.com/beetlebugorg/resumed/internal/render"
	"github.com/beetlebugorg/resumed/internal/store"
)

//go:embed templates/*.html static/*
var assets embed.FS

type server struct {
	app      *app.App
	pages    map[string]*template.Template
	partials *template.Template
}

var funcs = template.FuncMap{
	"statuses": func() []string { return store.JobStatuses },
	// highlight applies the resume's emphasis terms, so the screen and the PDF
	// agree about what is bold.
	"highlight": render.HighlightHTML,
	"truncate": func(n int, s string) string {
		if len(s) <= n {
			return s
		}
		return s[:n] + "…"
	},
	// shortDate turns "2026-07-31 14:02:11" into "2026-07-31".
	"shortDate": func(s string) string {
		if i := strings.IndexByte(s, ' '); i > 0 {
			return s[:i]
		}
		return s
	},
}

// pageFiles lists each full page; every one is parsed with the layout and the
// partials so it can render fragments inline on a cold load.
var pageFiles = []string{"jobs.html", "job.html", "resume.html", "cover.html", "questions.html", "facts.html"}

func newServer(a *app.App) (*server, error) {
	s := &server{app: a, pages: map[string]*template.Template{}}
	for _, name := range pageFiles {
		t, err := template.New("layout.html").Funcs(funcs).
			ParseFS(assets, "templates/layout.html", "templates/partials.html", "templates/"+name)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		s.pages[name] = t
	}
	p, err := template.New("partials").Funcs(funcs).ParseFS(assets, "templates/partials.html")
	if err != nil {
		return nil, fmt.Errorf("parse partials: %w", err)
	}
	s.partials = p
	return s, nil
}

func (s *server) render(w http.ResponseWriter, page string, data any) {
	t, ok := s.pages[page]
	if !ok {
		http.Error(w, "unknown page "+page, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Every page reflects mutable database state, so a cached copy is always
	// wrong. Without this a back-navigation can show a job as it looked before
	// a note or a status change, which reads as data loss.
	w.Header().Set("Cache-Control", "no-store")
	if err := t.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// partial writes one named fragment — the htmx response path.
func (s *server) partial(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.partials.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// redirect sends the browser to dest, using htmx's client-side redirect when
// the request came from htmx.
func redirect(w http.ResponseWriter, r *http.Request, dest string) {
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", dest)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func fail(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
	return true
}

// parseForm reads the request body up front and reports a malformed submission.
// Without this, ParseForm's error is swallowed and every FormValue returns "",
// which looks exactly like the user submitting an empty form.
func parseForm(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "malformed form data: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func pathID(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(r.PathValue(name), 10, 64)
}

// Serve starts the web UI.
func Serve(a *app.App, addr string) error {
	s, err := newServer(a)
	if err != nil {
		return err
	}
	static, err := fs.Sub(assets, "static")
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	assetHandler, err := staticHandler(static)
	if err != nil {
		return fmt.Errorf("static assets: %w", err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", assetHandler))

	mux.HandleFunc("GET /{$}", s.listJobs)
	mux.HandleFunc("POST /jobs", s.addJob)
	mux.HandleFunc("GET /jobs/{id}", s.showJob)
	mux.HandleFunc("POST /jobs/{id}/notes", s.addNote)
	mux.HandleFunc("POST /jobs/{id}/status", s.updateStatus)
	mux.HandleFunc("POST /jobs/{id}/description", s.updateDescription)
	mux.HandleFunc("POST /jobs/{id}/delete", s.deleteJob)
	mux.HandleFunc("POST /notes/{id}/delete", s.deleteNote)

	mux.HandleFunc("GET /questions", s.listQuestions)
	mux.HandleFunc("GET /questions/{id}", s.showQuestion)
	mux.HandleFunc("GET /questions/{id}/edit", s.editQuestion)
	mux.HandleFunc("POST /questions/{id}/answer", s.answerQuestion)
	mux.HandleFunc("POST /questions/{id}/dismiss", s.dismissQuestion)

	mux.HandleFunc("GET /facts", s.showFacts)
	mux.HandleFunc("POST /facts/{kind}/{id}/{verb}", s.setFactRetired)

	mux.HandleFunc("GET /resumes/{id}", s.showResume)
	mux.HandleFunc("GET /resumes/{id}/pdf", s.resumePDF)
	mux.HandleFunc("GET /resumes/{id}/sent.pdf", s.resumeSentPDF)
	mux.HandleFunc("GET /resumes/{id}/typst", s.resumeTypst)
	mux.HandleFunc("POST /resumes/{id}/render", s.renderResume)

	mux.HandleFunc("GET /cover-letters/{id}", s.showCover)
	mux.HandleFunc("GET /cover-letters/{id}/pdf", s.coverPDF)
	mux.HandleFunc("GET /cover-letters/{id}/typst", s.coverTypst)
	mux.HandleFunc("POST /cover-letters/{id}/render", s.renderCover)

	fmt.Printf("resumed: http://%s  (db %s)\n", addr, a.Store.Path)
	return http.ListenAndServe(addr, mux)
}

// ------------------------------------------------------------- view models

type jobListView struct {
	Jobs   []store.Job
	Filter string
}

type notesView struct {
	JobID int64
	Notes []store.JobNote
}

type statusView struct {
	Job *store.Job
}

type versionsView struct {
	Resumes   []store.Resume
	Doc       *store.Document
	Highlight []string
	// Frozen tracks the job status, not the snapshot. A job applied before
	// snapshots existed is frozen with nothing captured, and the UI must not
	// offer an action the server will refuse.
	Frozen bool
	Status string
}

// coverViewFor gathers what the letter template needs. The profile and
// contacts come from the fact base rather than the letter, the same way the
// print renderer builds them, so the screen and the PDF show one letterhead.
func (s *server) coverViewFor(letter *store.CoverLetter, job *store.Job) coverView {
	v := coverView{Letter: letter, Job: job}
	if letter == nil {
		return v
	}
	if p, err := s.app.Store.GetProfile(); err == nil {
		v.Profile = p
	}
	if c, err := s.app.Store.ListContacts(); err == nil {
		v.Contacts = c
	}
	v.Date = render.LetterDate(letter.UpdatedAt)
	if job != nil {
		v.Frozen, _ = s.frozen(job.ID)
	}
	return v
}

// coverPage renders a cover letter on its own, away from the job's other
// material.
type coverPage struct {
	Title     string
	Nav       string
	Cover     coverView
	OpenCount int
	Flash     string
}

func (s *server) showCover(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	letter, err := s.app.Store.GetCoverLetter(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	job, err := s.app.Store.GetJob(letter.JobID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, "cover.html", coverPage{
		Title: job.Label(), Nav: "jobs",
		Cover: s.coverViewFor(letter, job), OpenCount: s.openCount(),
	})
}

// frozen reports whether a job's resume may still be re-rendered.
func (s *server) frozen(jobID int64) (bool, string) {
	f, status, err := s.app.Store.ResumeFrozen(jobID)
	if err != nil {
		return false, ""
	}
	return f, status
}

// latestDoc assembles a job's current resume for inline display. A job with no
// resume is the normal empty case, not an error, so both returns are nil.
func (s *server) latestDoc(resumes []store.Resume) (*store.Document, []string) {
	if len(resumes) == 0 {
		return nil, nil
	}
	doc, err := s.app.Store.Assemble(resumes[0].ID)
	if err != nil {
		return nil, nil
	}
	var terms []string
	if doc.Resume != nil {
		terms = doc.Resume.Highlight
	}
	return doc, terms
}

// coverView carries a job's letter, or nil when none has been written. Error
// is set when a re-render failed, so the row can say so in place.
type coverView struct {
	Letter   *store.CoverLetter
	Profile  store.Profile
	Contacts []store.Contact
	Job      *store.Job
	Date     string
	Frozen   bool
	Error    string
}

type questionView struct {
	Q        store.Question
	JobLabel string
	ReturnTo string
	// Editing renders an answered question back as a prefilled form, so a typo
	// can be fixed without dismissing and re-answering.
	Editing bool
}

// openCount is the questions badge in the nav.
func (s *server) openCount() int {
	open, err := s.app.Store.ListQuestions("open", nil)
	if err != nil {
		return 0
	}
	return len(open)
}

// ------------------------------------------------------------------- pages

type jobsPage struct {
	Title     string
	Nav       string
	List      jobListView
	OpenCount int
	Flash     string
}

func (s *server) listJobs(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("status")
	jobs, err := s.app.Store.ListJobs(filter)
	if fail(w, err) {
		return
	}
	list := jobListView{Jobs: jobs, Filter: filter}
	if isHTMX(r) {
		s.partial(w, "job-list", list)
		return
	}
	s.render(w, "jobs.html", jobsPage{
		Title: "Jobs", Nav: "jobs", List: list,
		OpenCount: s.openCount(), Flash: r.URL.Query().Get("flash"),
	})
}

func (s *server) addJob(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	url := strings.TrimSpace(r.FormValue("url"))
	desc := strings.TrimSpace(r.FormValue("description"))
	res, err := s.app.AddJob(r.Context(), url, desc, "", "", "")
	if err != nil {
		redirect(w, r, "/?flash="+urlEscape(err.Error()))
		return
	}
	dest := fmt.Sprintf("/jobs/%d", res.Job.ID)
	if res.FetchNote != "" {
		dest += "?flash=" + urlEscape(res.FetchNote)
	}
	redirect(w, r, dest)
}

type jobPage struct {
	Title     string
	Nav       string
	Job       *store.Job
	Status    statusView
	Notes     notesView
	Questions []questionView
	Versions  versionsView
	Cover     coverView
	OpenCount int
	Flash     string
}

func (s *server) showJob(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	job, err := s.app.Store.GetJob(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	notes, err := s.app.Store.ListJobNotes(id)
	if fail(w, err) {
		return
	}
	questions, err := s.app.Store.ListQuestions("", &id)
	if fail(w, err) {
		return
	}
	resumes, err := s.app.Store.ListResumes(id)
	if fail(w, err) {
		return
	}
	letter, err := s.app.Store.CoverLetterByJob(id)
	if fail(w, err) {
		return
	}

	jobDoc, jobTerms := s.latestDoc(resumes)
	jobFrozen, jobStatus := s.frozen(id)

	returnTo := fmt.Sprintf("/jobs/%d", id)
	var qviews []questionView
	for _, q := range questions {
		if q.Status == "dismissed" {
			continue
		}
		qviews = append(qviews, questionView{Q: q, ReturnTo: returnTo})
	}

	s.render(w, "job.html", jobPage{
		Title:     job.Label(),
		Nav:       "jobs",
		Job:       job,
		Status:    statusView{Job: job},
		Notes:     notesView{JobID: id, Notes: notes},
		Questions: qviews,
		Versions:  versionsView{Resumes: resumes, Doc: jobDoc, Highlight: jobTerms, Frozen: jobFrozen, Status: jobStatus},
		Cover:     s.coverViewFor(letter, job),
		OpenCount: s.openCount(),
		Flash:     r.URL.Query().Get("flash"),
	})
}

func (s *server) addNote(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	if body := strings.TrimSpace(r.FormValue("body")); body != "" {
		if _, err := s.app.Store.AddJobNote(id, body, "user"); fail(w, err) {
			return
		}
	}
	notes, err := s.app.Store.ListJobNotes(id)
	if fail(w, err) {
		return
	}
	view := notesView{JobID: id, Notes: notes}
	if isHTMX(r) {
		s.partial(w, "notes", view)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/jobs/%d#notes", id), http.StatusSeeOther)
}

func (s *server) deleteNote(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	if err := s.app.Store.DeleteJobNote(id); fail(w, err) {
		return
	}
	if isHTMX(r) {
		// Swapping in nothing removes the note from the list.
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, returnTo(r, "/"), http.StatusSeeOther)
}

func (s *server) updateStatus(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	status := strings.TrimSpace(r.FormValue("status"))
	if !validStatus(status) {
		http.Error(w, "invalid status "+status, http.StatusBadRequest)
		return
	}
	// Through the app, not the store: moving into applied freezes a copy of the
	// resume as sent, and that needs the file copy the app layer does.
	if err := s.app.SetJobStatus(id, status); fail(w, err) {
		return
	}
	job, err := s.app.Store.GetJob(id)
	if fail(w, err) {
		return
	}
	if isHTMX(r) {
		s.partial(w, "status-control", statusView{Job: job})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/jobs/%d", id), http.StatusSeeOther)
}

func validStatus(s string) bool {
	for _, v := range store.JobStatuses {
		if v == s {
			return true
		}
	}
	return false
}

func (s *server) updateDescription(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	fields := map[string]any{"description": strings.TrimSpace(r.FormValue("description"))}
	for _, k := range []string{"company", "title", "location"} {
		if v, present := r.Form[k]; present && len(v) > 0 {
			fields[k] = strings.TrimSpace(v[0])
		}
	}
	if err := s.app.Store.UpdateJobFields(id, fields); fail(w, err) {
		return
	}
	redirect(w, r, fmt.Sprintf("/jobs/%d?flash=%s", id, urlEscape("posting updated")))
}

func (s *server) deleteJob(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	if err := s.app.Store.DeleteJob(id); fail(w, err) {
		return
	}
	redirect(w, r, "/")
}

type questionsPage struct {
	Title     string
	Nav       string
	Open      []questionView
	Answered  []questionView
	OpenCount int
	Flash     string
}

func (s *server) listQuestions(w http.ResponseWriter, r *http.Request) {
	all, err := s.app.Store.ListQuestions("", nil)
	if fail(w, err) {
		return
	}
	jobs, err := s.app.Store.ListJobs("")
	if fail(w, err) {
		return
	}
	labels := map[int64]string{}
	for _, j := range jobs {
		labels[j.ID] = j.Label()
	}

	var open, answered []questionView
	for _, q := range all {
		v := questionView{Q: q, ReturnTo: "/questions"}
		if q.JobID != nil {
			v.JobLabel = labels[*q.JobID]
		}
		switch q.Status {
		case "open":
			open = append(open, v)
		case "answered":
			answered = append(answered, v)
		}
	}
	s.render(w, "questions.html", questionsPage{
		Title: "Questions", Nav: "questions",
		Open: open, Answered: answered, OpenCount: len(open),
	})
}

// questionFragment re-renders one question card, in view or edit mode.
func (s *server) questionFragment(w http.ResponseWriter, r *http.Request, id int64, editing bool) {
	qs, err := s.app.Store.ListQuestions("", nil)
	if fail(w, err) {
		return
	}
	for _, q := range qs {
		if q.ID != id {
			continue
		}
		v := questionView{Q: q, ReturnTo: returnTo(r, "/questions"), Editing: editing}
		if q.JobID != nil {
			if job, err := s.app.Store.GetJob(*q.JobID); err == nil {
				v.JobLabel = job.Label()
			}
		}
		s.partial(w, "question", v)
		return
	}
	http.NotFound(w, r)
}

// showQuestion renders a single card — used to cancel out of editing.
func (s *server) showQuestion(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.questionFragment(w, r, id, false)
}

// editQuestion swaps a card into a prefilled form.
func (s *server) editQuestion(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.questionFragment(w, r, id, true)
}

func (s *server) answerQuestion(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	if answer := strings.TrimSpace(r.FormValue("answer")); answer != "" {
		if err := s.app.Store.AnswerQuestion(id, answer); fail(w, err) {
			return
		}
	}
	if isHTMX(r) {
		s.questionFragment(w, r, id, false)
		return
	}
	http.Redirect(w, r, returnTo(r, "/questions"), http.StatusSeeOther)
}

func (s *server) dismissQuestion(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	if err := s.app.Store.SetQuestionStatus(id, "dismissed"); fail(w, err) {
		return
	}
	if isHTMX(r) {
		// Nothing swapped back: the card disappears.
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, returnTo(r, "/questions"), http.StatusSeeOther)
}

// factRow is one retirable fact as the UI shows it.
type factRow struct {
	Kind    string
	ID      int64
	Text    string
	Note    string // provenance or category
	Retired bool
}

// factGroup is a container fact (a role or project) plus its child rows. The
// container is retirable in its own right.
type factGroup struct {
	factRow
	Meta string // dates
	Sub  string // role or project summary line
	Rows []factRow
}

type skillGroup struct {
	Category string
	Rows     []factRow
}

type factsPage struct {
	Title        string
	Nav          string
	Profile      store.Profile
	Contacts     []store.Contact
	Experience   []factGroup
	Projects     []factGroup
	Skills       []skillGroup
	Patents      []factRow
	OpenCount    int
	Flash        string
	RetiredCount int
}

func (s *server) showFacts(w http.ResponseWriter, r *http.Request) {
	// FactBaseAll: the facts page is where you retire and restore, so it has to
	// show what is already retired.
	fb, err := s.app.Store.FactBaseAll()
	if fail(w, err) {
		return
	}
	page := factsPage{
		Title: "Facts", Nav: "facts", OpenCount: s.openCount(),
		Profile: fb.Profile, Contacts: fb.Contacts,
		Flash: r.URL.Query().Get("flash"),
	}

	count := func(retired bool) {
		if retired {
			page.RetiredCount++
		}
	}

	for _, role := range fb.Roles {
		g := factGroup{
			factRow: factRow{Kind: store.KindRole, ID: role.ID,
				Text: role.Company + " — " + role.Title, Retired: role.Retired()},
			Meta: role.Dates(), Sub: role.Summary,
		}
		count(role.Retired())
		for _, b := range role.Bullets {
			note := ""
			if b.Source == "interview" {
				note = "from interview"
			}
			g.Rows = append(g.Rows, factRow{Kind: store.KindBullet, ID: b.ID,
				Text: b.Text, Note: note, Retired: b.Retired()})
			count(b.Retired())
		}
		page.Experience = append(page.Experience, g)
	}

	for _, p := range fb.Projects {
		g := factGroup{
			factRow: factRow{Kind: store.KindProject, ID: p.ID, Text: p.Name, Retired: p.Retired()},
			Meta:    p.Date, Sub: p.Summary,
		}
		count(p.Retired())
		for _, b := range p.Bullets {
			g.Rows = append(g.Rows, factRow{Kind: store.KindProjectBullet, ID: b.ID,
				Text: b.Text, Retired: b.Retired()})
			count(b.Retired())
		}
		page.Projects = append(page.Projects, g)
	}

	var order []string
	byCat := map[string][]factRow{}
	for _, sk := range fb.Skills {
		if _, seen := byCat[sk.Category]; !seen {
			order = append(order, sk.Category)
		}
		byCat[sk.Category] = append(byCat[sk.Category], factRow{
			Kind: store.KindSkill, ID: sk.ID, Text: sk.Name, Retired: sk.Retired()})
		count(sk.Retired())
	}
	for _, cat := range order {
		page.Skills = append(page.Skills, skillGroup{Category: cat, Rows: byCat[cat]})
	}

	for _, p := range fb.Patents {
		page.Patents = append(page.Patents, factRow{Kind: store.KindPatent, ID: p.ID,
			Text: p.PatentID + " — " + p.Summary, Retired: p.Retired()})
		count(p.Retired())
	}

	s.render(w, "facts.html", page)
}

// setFactRetired handles both retire and restore, keyed off the URL verb.
func (s *server) setFactRetired(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	retire := r.PathValue("verb") == "retire"
	if err := s.app.Store.SetFactRetired(kind, id, retire); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if isHTMX(r) {
		// Re-render just this row in its new state.
		row, err := s.factRowByID(kind, id)
		if fail(w, err) {
			return
		}
		s.partial(w, "fact-row", row)
		return
	}
	http.Redirect(w, r, "/facts", http.StatusSeeOther)
}

// factRowByID rebuilds one row after its retired state changes.
func (s *server) factRowByID(kind string, id int64) (factRow, error) {
	fb, err := s.app.Store.FactBaseAll()
	if err != nil {
		return factRow{}, err
	}
	switch kind {
	case store.KindRole:
		for _, r := range fb.Roles {
			if r.ID == id {
				return factRow{Kind: kind, ID: id, Text: r.Company + " — " + r.Title, Retired: r.Retired()}, nil
			}
		}
	case store.KindBullet:
		for _, r := range fb.Roles {
			for _, b := range r.Bullets {
				if b.ID == id {
					note := ""
					if b.Source == "interview" {
						note = "from interview"
					}
					return factRow{Kind: kind, ID: id, Text: b.Text, Note: note, Retired: b.Retired()}, nil
				}
			}
		}
	case store.KindProject:
		for _, p := range fb.Projects {
			if p.ID == id {
				return factRow{Kind: kind, ID: id, Text: p.Name, Retired: p.Retired()}, nil
			}
		}
	case store.KindProjectBullet:
		for _, p := range fb.Projects {
			for _, b := range p.Bullets {
				if b.ID == id {
					return factRow{Kind: kind, ID: id, Text: b.Text, Retired: b.Retired()}, nil
				}
			}
		}
	case store.KindSkill:
		for _, sk := range fb.Skills {
			if sk.ID == id {
				return factRow{Kind: kind, ID: id, Text: sk.Name, Retired: sk.Retired()}, nil
			}
		}
	case store.KindPatent:
		for _, p := range fb.Patents {
			if p.ID == id {
				return factRow{Kind: kind, ID: id, Text: p.PatentID + " — " + p.Summary, Retired: p.Retired()}, nil
			}
		}
	}
	return factRow{}, fmt.Errorf("%s %d not found", kind, id)
}

// ---------------------------------------------------------------- artifacts

// resumePage renders the assembled resume as HTML. It is the reading view: the
// same selection the PDF is built from, laid out for a screen, so a resume can
// be reviewed without opening a file.
type resumePage struct {
	Title     string
	Nav       string
	Doc       *store.Document
	Highlight []string
	Frozen    bool
	Status    string
	OpenCount int
	Flash     string
}

func (s *server) showResume(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	doc, err := s.app.Store.Assemble(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	title := doc.Profile.Name
	if doc.Job != nil {
		title = doc.Job.Label()
	}
	var terms []string
	if doc.Resume != nil {
		terms = doc.Resume.Highlight
	}
	var frozen bool
	var status string
	if doc.Job != nil {
		frozen, status = s.frozen(doc.Job.ID)
	}
	s.render(w, "resume.html", resumePage{
		Title: title, Nav: "jobs", Doc: doc, Highlight: terms,
		Frozen: frozen, Status: status, OpenCount: s.openCount(),
	})
}

// downloadName builds the filename a browser offers in its save dialog. The
// default is the URL's last segment, which for these routes is "pdf", so every
// saved application arrives as pdf.pdf with no way to tell one from another.
// Naming it for the candidate and the job means a folder of downloads is
// readable without opening anything.
func downloadName(profileName, kind string, job *store.Job) string {
	parts := []string{app.Slug(profileName), kind}
	if job != nil {
		if job.Company != "" {
			parts = append(parts, app.Slug(job.Company))
		}
		if job.Title != "" {
			parts = append(parts, app.Slug(job.Title))
		}
	}
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, "-") + ".pdf"
}

// servePDF writes a PDF with a filename the browser will use. "inline" keeps
// the browser preview; "attachment" would force a download instead.
func (s *server) servePDF(w http.ResponseWriter, r *http.Request, path, name string) {
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "PDF missing on disk: "+err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if fail(w, err) {
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", name))
	http.ServeContent(w, r, name, st.ModTime(), f)
}

func (s *server) resumePDF(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	res, err := s.app.Store.GetResume(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if res.PDFPath == "" {
		http.Error(w, "no PDF for this version — render it first", http.StatusNotFound)
		return
	}
	job, _ := s.app.Store.GetJob(res.JobID)
	profile, _ := s.app.Store.GetProfile()
	s.servePDF(w, r, res.PDFPath, downloadName(profile.Name, "resume", job))
}

func (s *server) coverPDF(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	c, err := s.app.Store.GetCoverLetter(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if c.PDFPath == "" {
		http.Error(w, "no PDF for this cover letter — render it first", http.StatusNotFound)
		return
	}
	job, _ := s.app.Store.GetJob(c.JobID)
	profile, _ := s.app.Store.GetProfile()
	s.servePDF(w, r, c.PDFPath, downloadName(profile.Name, "cover-letter", job))
}

func (s *server) coverTypst(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	c, err := s.app.Store.GetCoverLetter(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, c.Typst)
}

func (s *server) renderCover(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	c, err := s.app.Store.GetCoverLetter(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_, renderErr := s.app.RenderCover(id)
	if isHTMX(r) {
		fresh, err := s.app.Store.CoverLetterByJob(c.JobID)
		if fail(w, err) {
			return
		}
		job, _ := s.app.Store.GetJob(fresh.JobID)
		view := s.coverViewFor(fresh, job)
		if renderErr != nil {
			view.Error = renderErr.Error()
		}
		s.partial(w, "cover", view)
		return
	}
	redirect(w, r, fmt.Sprintf("/jobs/%d", c.JobID))
}

// resumeSentPDF serves the frozen copy: the document as it went out, not as it
// would render today.
func (s *server) resumeSentPDF(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	res, err := s.app.Store.GetResume(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if res.SentPDFPath == "" {
		http.Error(w, "no sent copy was captured for this resume", http.StatusNotFound)
		return
	}
	job, _ := s.app.Store.GetJob(res.JobID)
	profile, _ := s.app.Store.GetProfile()
	s.servePDF(w, r, res.SentPDFPath, downloadName(profile.Name, "resume-as-sent", job))
}

func (s *server) resumeTypst(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	res, err := s.app.Store.GetResume(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, res.Typst)
}

func (s *server) renderResume(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	res, err := s.app.Store.GetResume(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	out, renderErr := s.app.Render(id)

	if isHTMX(r) {
		resumes, err := s.app.Store.ListResumes(res.JobID)
		if fail(w, err) {
			return
		}
		switch {
		case renderErr != nil:
			w.Header().Set("HX-Trigger", trigger("render failed: "+renderErr.Error()))
		case out.Warning != "":
			w.Header().Set("HX-Trigger", trigger(out.Warning))
		default:
			w.Header().Set("HX-Trigger", trigger(fmt.Sprintf("rendered v%d", out.Version)))
		}
		rdoc, rterms := s.latestDoc(resumes)
		rf, rs := s.frozen(res.JobID)
		s.partial(w, "versions", versionsView{Resumes: resumes, Doc: rdoc, Highlight: rterms, Frozen: rf, Status: rs})
		return
	}

	dest := fmt.Sprintf("/jobs/%d", res.JobID)
	switch {
	case renderErr != nil:
		dest += "?flash=" + urlEscape("render failed: "+renderErr.Error())
	case out.Warning != "":
		dest += "?flash=" + urlEscape(out.Warning)
	default:
		dest += "?flash=" + urlEscape(fmt.Sprintf("rendered v%d", out.Version))
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// ------------------------------------------------------------------ helpers

// trigger builds an HX-Trigger header value that fires the client-side "flash"
// event with a message.
func trigger(msg string) string {
	msg = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", " ", "\r", "").Replace(msg)
	return `{"flash":"` + msg + `"}`
}

func urlEscape(s string) string {
	return strings.NewReplacer(" ", "%20", "\n", " ", "#", "%23", "&", "%26", "?", "%3F").Replace(s)
}

// returnTo keeps redirects on the site: only same-origin absolute paths are
// honoured, anything else falls back.
func returnTo(r *http.Request, fallback string) string {
	v := r.FormValue("return_to")
	if strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//") {
		return v
	}
	return fallback
}

// ---------------------------------------------------------------- static assets

// staticHandler serves the embedded assets with a content-derived ETag.
//
// This exists because http.FileServer over an embed.FS emits no validator at
// all: embedded files carry a zero modification time, so there is no
// Last-Modified, and FileServer does not compute an ETag. With nothing to
// revalidate against, browsers fall back to heuristic caching and will happily
// keep serving a stylesheet from a previous build — the UI silently stops
// matching the binary, which is maddening to debug because the server is
// serving the right bytes the whole time.
//
// Hashing each asset at startup fixes it without giving up caching. "no-cache"
// does not mean "do not cache"; it means "revalidate before use", and the ETag
// makes that revalidation a 304 with no body.
func staticHandler(fsys fs.FS) (http.Handler, error) {
	type asset struct {
		body  []byte
		etag  string
		ctype string
	}
	files := map[string]asset{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		ctype := mime.TypeByExtension(path.Ext(p))
		if ctype == "" {
			ctype = http.DetectContentType(b)
		}
		files[p] = asset{body: b, etag: `"` + hex.EncodeToString(sum[:8]) + `"`, ctype: ctype}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", a.etag)
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", a.ctype)
		// ServeContent handles If-None-Match and range requests. The zero time
		// suppresses Last-Modified, which is correct: we have no real one.
		http.ServeContent(w, r, path.Base(r.URL.Path), time.Time{}, bytes.NewReader(a.body))
	}), nil
}
