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
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
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
	reopen   func() (*store.Store, error)
	outRoot  string
	pages    map[string]*template.Template
	partials *template.Template
}

// appKey carries the request's App. Each request opens its own database
// connection, so nothing is shared between them and no lock is needed.
type appKey struct{}

// appOf returns the App built for this request.
func appOf(r *http.Request) *app.App { return r.Context().Value(appKey{}).(*app.App) }

// withStore opens the database for one request and closes it afterwards.
// SQLite cannot checkpoint its write-ahead log into the database file while a
// connection is open, so a connection held for the life of the server leaves
// the file on disk behind the data.
func (s *server) withStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st, err := s.reopen()
		if err != nil {
			http.Error(w, "open database: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer st.Close()
		ctx := context.WithValue(r.Context(), appKey{}, app.New(st, s.outRoot))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

var funcs = template.FuncMap{
	"statuses": func() []string { return store.JobStatuses },
	// highlight applies the resume's emphasis terms, so the screen and the PDF
	// agree about what is bold.
	"highlight": render.HighlightHTML,
	"shortDate": shortDate,
	"briefDate": briefDate,
	"relTime":   relTime,
	"fullTime":  fullTime,
}

// SQLite's datetime('now') writes UTC.
const stampLayout = "2006-01-02 15:04:05"

// relTime says how long ago something happened. A note log is read as a
// sequence of events, and "3 days ago" places one faster than a date does.
func relTime(s string) string {
	t, err := time.ParseInLocation(stampLayout, s, time.UTC)
	if err != nil {
		// Show whatever was stored. shortDate cuts at the first space and
		// turns an unparseable value into a shorter unparseable value.
		return s
	}
	d := time.Since(t)
	switch {
	case d < 2*time.Minute:
		// Also covers a clock that is slightly behind the database's.
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 2*time.Hour:
		return "an hour ago"
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	default:
		return t.Local().Format("2 Jan 2006")
	}
}

// fullTime is the exact local time, for the tooltip behind relTime.
func fullTime(s string) string {
	t, err := time.ParseInLocation(stampLayout, s, time.UTC)
	if err != nil {
		return s
	}
	return t.Local().Format("Mon 2 Jan 2006, 15:04")
}

// shortDate turns "2026-07-31 14:02:11" into "2026-07-31".
func shortDate(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}

// briefDate turns the same stamp into "Jul 31". Every sidebar row repeats the
// same year, so the year is dropped.
func briefDate(s string) string {
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		return shortDate(s)
	}
	return t.Format("Jan 2")
}

// pageFiles lists each full page; every one is parsed with the layout and the
// partials so it can render fragments inline on a cold load.
var pageFiles = []string{"workspace.html", "resume.html", "cover.html", "questions.html", "facts.html"}

func newServer(reopen func() (*store.Store, error), outRoot string) (*server, error) {
	s := &server{reopen: reopen, outRoot: outRoot, pages: map[string]*template.Template{}}
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
// Serve starts the web UI. reopen is called once per request, so the server
// holds no database connection between them.
func Serve(reopen func() (*store.Store, error), outRoot, addr string) error {
	s, err := newServer(reopen, outRoot)
	if err != nil {
		return err
	}
	static, err := fs.Sub(assets, "static")
	if err != nil {
		return err
	}

	assetHandler, err := staticHandler(static)
	if err != nil {
		return fmt.Errorf("static assets: %w", err)
	}

	// Routes that read or write the database. Everything registered here is
	// wrapped so the connection lives only as long as the request.
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.listJobs)
	mux.HandleFunc("POST /jobs", s.addJob)
	// A literal segment beats the wildcard below it, so this never shadows a job.
	mux.HandleFunc("GET /jobs/list", s.jobList)
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

	// The embedded assets need no database.
	top := http.NewServeMux()
	top.Handle("GET /static/", http.StripPrefix("/static/", assetHandler))
	top.Handle("/", s.withStore(mux))

	fmt.Printf("resumed: http://%s\n", addr)
	return http.ListenAndServe(addr, top)
}

// ------------------------------------------------------------- view models

// The workspace is the jobs screen: a filtered list on the left, one job on
// the right. Both halves are addressable, so the browser's URL always says
// which job is open and under which filter.

const (
	sortRecent  = "recent"
	sortCompany = "company"
	sortStatus  = "status"
)

const (
	tabResume    = "resume"
	tabCover     = "cover"
	tabPosting   = "posting"
	tabNotes     = "notes"
	tabQuestions = "questions"
)

// listQuery is the state of the sidebar's three controls. Every link the
// workspace draws includes it, so opening a job or switching a tab keeps the
// current filter.
type listQuery struct {
	Q      string
	Status string
	Sort   string
}

func parseListQuery(v url.Values) listQuery {
	lq := listQuery{
		Q:      strings.TrimSpace(v.Get("q")),
		Status: v.Get("status"),
		Sort:   v.Get("sort"),
	}
	if !validStatus(lq.Status) {
		lq.Status = ""
	}
	switch lq.Sort {
	case sortCompany, sortStatus:
	default:
		lq.Sort = sortRecent
	}
	return lq
}

// href builds a workspace address from a path, the tab to open, and the list
// state to keep. Defaults are omitted, so an untouched workspace has the bare
// "/jobs/12" address.
func (lq listQuery) href(path, tab string) string {
	v := url.Values{}
	if tab != "" && tab != tabResume {
		v.Set("tab", tab)
	}
	if lq.Q != "" {
		v.Set("q", lq.Q)
	}
	if lq.Status != "" {
		v.Set("status", lq.Status)
	}
	if lq.Sort != sortRecent {
		v.Set("sort", lq.Sort)
	}
	if len(v) == 0 {
		return path
	}
	return path + "?" + v.Encode()
}

func validTab(t string) string {
	switch t {
	case tabCover, tabPosting, tabNotes, tabQuestions:
		return t
	}
	return tabResume
}

type jobRow struct {
	Job      store.Job
	Href     string
	Selected bool
}

// jobGroup holds the rows for one status, used only when sorting by status.
type jobGroup struct {
	Name string
	Rows []jobRow
}

type jobListView struct {
	Rows   []jobRow
	Groups []jobGroup
	Query  listQuery
	Count  string
	// OOB marks the list as an out-of-band swap, so a response whose real
	// subject is the right pane can still correct the sidebar underneath it.
	OOB bool
}

// buildJobList applies the sidebar's controls. Filtering and ordering happen
// here rather than in SQL because the list is small, and because the count
// line needs to know how many jobs exist as well as how many matched.
func buildJobList(jobs []store.Job, lq listQuery, selected int64) jobListView {
	v := jobListView{Query: lq}
	needle := strings.ToLower(lq.Q)
	var matched []store.Job
	for _, j := range jobs {
		if lq.Status != "" && j.Status != lq.Status {
			continue
		}
		if needle != "" && !jobMatches(j, needle) {
			continue
		}
		matched = append(matched, j)
	}

	row := func(j store.Job) jobRow {
		path := fmt.Sprintf("/jobs/%d", j.ID)
		return jobRow{Job: j, Href: lq.href(path, ""), Selected: j.ID == selected}
	}

	switch lq.Sort {
	case sortCompany:
		sort.SliceStable(matched, func(a, b int) bool {
			ca, cb := strings.ToLower(matched[a].Company), strings.ToLower(matched[b].Company)
			if ca != cb {
				return ca < cb
			}
			return strings.ToLower(matched[a].Title) < strings.ToLower(matched[b].Title)
		})
	case sortStatus:
		// Pipeline order rather than alphabetical: the reason to group by
		// status is to see where the applications are piling up.
		byStatus := map[string][]store.Job{}
		for _, j := range matched {
			byStatus[j.Status] = append(byStatus[j.Status], j)
		}
		for _, st := range store.JobStatuses {
			g := jobGroup{Name: st}
			for _, j := range byStatus[st] {
				g.Rows = append(g.Rows, row(j))
			}
			if len(g.Rows) > 0 {
				v.Groups = append(v.Groups, g)
			}
		}
	}
	if lq.Sort != sortStatus {
		for _, j := range matched {
			v.Rows = append(v.Rows, row(j))
		}
	}

	v.Count = countLine(len(matched), len(jobs))
	return v
}

// jobMatches searches the fields a person remembers a job by. The description
// is excluded because it matches almost every query.
func jobMatches(j store.Job, needle string) bool {
	for _, f := range []string{j.Company, j.Title, j.Location, j.Status} {
		if strings.Contains(strings.ToLower(f), needle) {
			return true
		}
	}
	return false
}

func countLine(shown, total int) string {
	switch {
	case shown != total:
		return fmt.Sprintf("%d of %d jobs", shown, total)
	case total == 1:
		return "1 job"
	default:
		return fmt.Sprintf("%d jobs", total)
	}
}

// tabLink is one entry on the strip above the pane body.
type tabLink struct {
	Key   string
	Label string
	Count int
	Href  string
	On    bool
}

// tabsView is the strip on its own. Counts on it change from inside the pane,
// when a note is added or a question answered, so a response that redraws one
// card redraws the strip as well.
type tabsView struct {
	Tabs []tabLink
	OOB  bool
}

func buildTabs(jobID int64, lq listQuery, tab string, notes, questions, open int) tabsView {
	v := tabsView{Tabs: []tabLink{
		{Key: tabResume, Label: "Resume"},
		{Key: tabCover, Label: "Cover"},
		{Key: tabPosting, Label: "Posting"},
		{Key: tabNotes, Label: "Notes", Count: notes},
	}}
	// The questions tab appears only once there is something to read there.
	if questions > 0 {
		v.Tabs = append(v.Tabs, tabLink{Key: tabQuestions, Label: "Questions", Count: open})
	}
	path := fmt.Sprintf("/jobs/%d", jobID)
	for i := range v.Tabs {
		v.Tabs[i].On = v.Tabs[i].Key == tab
		v.Tabs[i].Href = lq.href(path, v.Tabs[i].Key)
	}
	return v
}

// jobPaneView is the right-hand pane: one job, one tab at a time. A zero value
// is the no-job-selected state, which renders the add-a-job form instead.
type jobPaneView struct {
	Job       *store.Job
	Status    statusView
	Tab       string
	Tabs      tabsView
	Notes     notesView
	Questions []questionView
	Versions  versionsView
	Cover     coverView
}

type notesView struct {
	JobID int64
	Notes []noteView
}

// noteView is one note prepared for reading. Claude's notes are reasoning
// records that run to several paragraphs. Three of them at full length fill
// the tab, so a long note shows its opening paragraph and expands on request.
type noteView struct {
	Note store.JobNote
	Lead string
	Rest string
	// Mine marks a note the user wrote, as opposed to one Claude left behind.
	Mine bool
}

// noteFold is the length past which a note folds. Below it the control costs
// more attention than the text it hides.
const noteFold = 400

// splitNote separates the opening paragraph from the rest. The split uses the
// author's blank line rather than a guessed sentence boundary.
func splitNote(body string) (lead, rest string) {
	body = strings.TrimSpace(body)
	if len(body) <= noteFold {
		return body, ""
	}
	for _, sep := range []string{"\r\n\r\n", "\n\n"} {
		if i := strings.Index(body, sep); i > 0 {
			return strings.TrimSpace(body[:i]), strings.TrimSpace(body[i+len(sep):])
		}
	}
	// One long paragraph has no break to fold on, so let it run.
	return body, ""
}

func buildNotes(notes []store.JobNote) []noteView {
	out := make([]noteView, 0, len(notes))
	for _, n := range notes {
		lead, rest := splitNote(n.Body)
		out = append(out, noteView{Note: n, Lead: lead, Rest: rest, Mine: n.Author != "claude"})
	}
	return out
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
func (s *server) coverViewFor(r *http.Request, letter *store.CoverLetter, job *store.Job) coverView {
	v := coverView{Letter: letter, Job: job}
	if letter == nil {
		return v
	}
	if p, err := appOf(r).Store.GetProfile(); err == nil {
		v.Profile = p
	}
	if c, err := appOf(r).Store.ListContacts(); err == nil {
		v.Contacts = c
	}
	v.Date = render.LetterDate(letter.UpdatedAt)
	if job != nil {
		v.Frozen, _ = s.frozen(r, job.ID)
	}
	return v
}

// coverPage renders a cover letter on its own, away from the job's other
// material.
type coverPage struct {
	chrome
	Cover coverView
}

func (s *server) showCover(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	letter, err := appOf(r).Store.GetCoverLetter(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	job, err := appOf(r).Store.GetJob(letter.JobID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, "cover.html", coverPage{
		chrome: chrome{Title: job.Label(), Nav: "jobs", OpenCount: s.openCount(r)},
		Cover:  s.coverViewFor(r, letter, job),
	})
}

// frozen reports whether a job's resume may still be re-rendered.
func (s *server) frozen(r *http.Request, jobID int64) (bool, string) {
	f, status, err := appOf(r).Store.ResumeFrozen(jobID)
	if err != nil {
		return false, ""
	}
	return f, status
}

// latestDoc assembles a job's current resume for inline display. A job with no
// resume is the normal empty case, not an error, so both returns are nil.
func (s *server) latestDoc(r *http.Request, resumes []store.Resume) (*store.Document, []string) {
	if len(resumes) == 0 {
		return nil, nil
	}
	doc, err := appOf(r).Store.Assemble(resumes[0].ID)
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
func (s *server) openCount(r *http.Request) int {
	open, err := appOf(r).Store.ListQuestions("open", nil)
	if err != nil {
		return 0
	}
	return len(open)
}

// ------------------------------------------------------------------- pages

// chrome is what layout.html needs from every page, embedded rather than
// repeated so the layout can grow a field without six structs following it.
type chrome struct {
	Title     string
	Nav       string
	OpenCount int
	Flash     string
	// Wide drops the centred column. The workspace fills the window and
	// scrolls its two panes separately.
	Wide bool
}

type workspacePage struct {
	chrome
	List jobListView
	Pane jobPaneView
}

func (s *server) listJobs(w http.ResponseWriter, r *http.Request) {
	lq := parseListQuery(r.URL.Query())
	s.render(w, "workspace.html", s.workspacePage(r, lq, 0, jobPaneView{}))
}

// workspacePage assembles both panes for a cold load.
func (s *server) workspacePage(r *http.Request, lq listQuery, selected int64, pane jobPaneView) workspacePage {
	title := "Jobs"
	if pane.Job != nil {
		title = pane.Job.Label()
	}
	return workspacePage{
		chrome: chrome{Title: title, Nav: "jobs", OpenCount: s.openCount(r),
			Flash: r.URL.Query().Get("flash"), Wide: true},
		List: s.listView(r, lq, selected),
		Pane: pane,
	}
}

func (s *server) listView(r *http.Request, lq listQuery, selected int64) jobListView {
	jobs, err := appOf(r).Store.ListJobs("")
	if err != nil {
		return jobListView{Query: lq}
	}
	return buildJobList(jobs, lq, selected)
}

// jobList serves the sidebar alone, for the search box and the two selects.
// Which job is open is read back from the address bar rather than carried in a
// hidden field, so the controls never have to know about the pane beside them.
func (s *server) jobList(w http.ResponseWriter, r *http.Request) {
	lq := parseListQuery(r.URL.Query())
	selected := currentJobID(r)
	if !isHTMX(r) {
		redirect(w, r, lq.href("/", ""))
		return
	}
	dest := "/"
	if selected > 0 {
		dest = fmt.Sprintf("/jobs/%d", selected)
	}
	// Keep the address bar in step: a reload comes back to the same filtered
	// list with the same job and tab open.
	w.Header().Set("HX-Push-Url", lq.href(dest, currentTab(r)))
	s.partial(w, "job-list", s.listView(r, lq, selected))
}

// oobList redraws the sidebar out of band. Used by handlers whose response is
// really about the right pane but whose change shows on the left as well.
func (s *server) oobList(w http.ResponseWriter, r *http.Request, lq listQuery, selected int64) {
	view := s.listView(r, lq, selected)
	view.OOB = true
	s.partial(w, "job-list", view)
}

func (s *server) addJob(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	url := strings.TrimSpace(r.FormValue("url"))
	desc := strings.TrimSpace(r.FormValue("description"))
	res, err := appOf(r).AddJob(r.Context(), url, desc, "", "", "")
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

func (s *server) showJob(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	job, err := appOf(r).Store.GetJob(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	lq := parseListQuery(r.URL.Query())
	pane, err := s.paneFor(r, job, lq, validTab(r.URL.Query().Get("tab")))
	if fail(w, err) {
		return
	}
	if isHTMX(r) {
		s.partial(w, "job-pane", pane)
		// Switching tabs within the job already open leaves the sidebar
		// correct, and skips redrawing it.
		if currentJobID(r) != id {
			s.oobList(w, r, lq, id)
		}
		return
	}
	s.render(w, "workspace.html", s.workspacePage(r, lq, id, pane))
}

// paneFor gathers what one tab needs. The counts on the tab strip are cheap,
// but assembling a resume or a letter is not, so each is read only for its own
// tab.
func (s *server) paneFor(r *http.Request, job *store.Job, lq listQuery, tab string) (jobPaneView, error) {
	st := appOf(r).Store
	notes, err := st.ListJobNotes(job.ID)
	if err != nil {
		return jobPaneView{}, err
	}
	questions, err := st.ListQuestions("", &job.ID)
	if err != nil {
		return jobPaneView{}, err
	}

	back := lq.href(fmt.Sprintf("/jobs/%d", job.ID), tabQuestions)
	var qviews []questionView
	open := 0
	for _, q := range questions {
		if q.Status == "dismissed" {
			continue
		}
		if q.Status == "open" {
			open++
		}
		qviews = append(qviews, questionView{Q: q, ReturnTo: back})
	}

	v := jobPaneView{
		Job:       job,
		Status:    statusView{Job: job},
		Tab:       tab,
		Notes:     notesView{JobID: job.ID, Notes: buildNotes(notes)},
		Questions: qviews,
	}

	switch tab {
	case tabResume:
		resumes, err := st.ListResumes(job.ID)
		if err != nil {
			return jobPaneView{}, err
		}
		doc, terms := s.latestDoc(r, resumes)
		frozen, status := s.frozen(r, job.ID)
		v.Versions = versionsView{Resumes: resumes, Doc: doc, Highlight: terms, Frozen: frozen, Status: status}
	case tabCover:
		letter, err := st.CoverLetterByJob(job.ID)
		if err != nil {
			return jobPaneView{}, err
		}
		v.Cover = s.coverViewFor(r, letter, job)
	}

	v.Tabs = buildTabs(job.ID, lq, tab, len(notes), len(qviews), open)
	return v, nil
}

// oobTabs redraws the tab strip out of band, for mutations whose response is
// one card rather than the whole pane. It stays quiet when the browser is not
// on a job page, because then there is no strip on screen to correct.
func (s *server) oobTabs(w http.ResponseWriter, r *http.Request) {
	jobID := currentJobID(r)
	if jobID == 0 {
		return
	}
	st := appOf(r).Store
	notes, err := st.ListJobNotes(jobID)
	if err != nil {
		return
	}
	questions, err := st.ListQuestions("", &jobID)
	if err != nil {
		return
	}
	shown, open := 0, 0
	for _, q := range questions {
		if q.Status == "dismissed" {
			continue
		}
		shown++
		if q.Status == "open" {
			open++
		}
	}
	u := currentURL(r)
	view := buildTabs(jobID, parseListQuery(u.Query()), validTab(u.Query().Get("tab")),
		len(notes), shown, open)
	view.OOB = true
	s.partial(w, "job-tabs", view)
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
		if _, err := appOf(r).Store.AddJobNote(id, body, "user"); fail(w, err) {
			return
		}
	}
	notes, err := appOf(r).Store.ListJobNotes(id)
	if fail(w, err) {
		return
	}
	view := notesView{JobID: id, Notes: buildNotes(notes)}
	if isHTMX(r) {
		s.partial(w, "notes", view)
		s.oobTabs(w, r)
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
	// Read the note's job before the row is gone.
	jobID, err := appOf(r).Store.JobNoteOwner(id)
	if fail(w, err) {
		return
	}
	if err := appOf(r).Store.DeleteJobNote(id); fail(w, err) {
		return
	}
	if isHTMX(r) {
		// The whole list comes back rather than just the removed card, so
		// the empty state appears after the last note is deleted.
		notes, err := appOf(r).Store.ListJobNotes(jobID)
		if fail(w, err) {
			return
		}
		s.partial(w, "notes", notesView{JobID: jobID, Notes: buildNotes(notes)})
		s.oobTabs(w, r)
		return
	}
	http.Redirect(w, r, returnTo(r, fmt.Sprintf("/jobs/%d?tab=notes", jobID)), http.StatusSeeOther)
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
	if err := appOf(r).SetJobStatus(id, status); fail(w, err) {
		return
	}
	job, err := appOf(r).Store.GetJob(id)
	if fail(w, err) {
		return
	}
	if isHTMX(r) {
		s.partial(w, "status-control", statusView{Job: job})
		// The sidebar prints the status too, and groups by it when sorted
		// that way, so it has to be redrawn alongside the control.
		s.oobList(w, r, parseListQuery(currentURL(r).Query()), id)
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
	if err := appOf(r).Store.UpdateJobFields(id, fields); fail(w, err) {
		return
	}
	redirect(w, r, fmt.Sprintf("/jobs/%d?tab=posting&flash=%s", id, urlEscape("posting updated")))
}

func (s *server) deleteJob(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	if err := appOf(r).Store.DeleteJob(id); fail(w, err) {
		return
	}
	redirect(w, r, "/")
}

type questionsPage struct {
	chrome
	Open     []questionView
	Answered []questionView
}

func (s *server) listQuestions(w http.ResponseWriter, r *http.Request) {
	all, err := appOf(r).Store.ListQuestions("", nil)
	if fail(w, err) {
		return
	}
	jobs, err := appOf(r).Store.ListJobs("")
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
		chrome: chrome{Title: "Questions", Nav: "questions", OpenCount: len(open)},
		Open:   open, Answered: answered,
	})
}

// questionFragment re-renders one question card, in view or edit mode.
func (s *server) questionFragment(w http.ResponseWriter, r *http.Request, id int64, editing bool) {
	qs, err := appOf(r).Store.ListQuestions("", nil)
	if fail(w, err) {
		return
	}
	for _, q := range qs {
		if q.ID != id {
			continue
		}
		v := questionView{Q: q, ReturnTo: returnTo(r, "/questions"), Editing: editing}
		if q.JobID != nil {
			if job, err := appOf(r).Store.GetJob(*q.JobID); err == nil {
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
		if err := appOf(r).Store.AnswerQuestion(id, answer); fail(w, err) {
			return
		}
	}
	if isHTMX(r) {
		s.questionFragment(w, r, id, false)
		s.oobTabs(w, r)
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
	if err := appOf(r).Store.SetQuestionStatus(id, "dismissed"); fail(w, err) {
		return
	}
	if isHTMX(r) {
		// Nothing swapped back but the strip: the card disappears.
		s.oobTabs(w, r)
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
	chrome
	Profile      store.Profile
	Contacts     []store.Contact
	Experience   []factGroup
	Projects     []factGroup
	Skills       []skillGroup
	Patents      []factRow
	RetiredCount int
}

func (s *server) showFacts(w http.ResponseWriter, r *http.Request) {
	// FactBaseAll: the facts page is where you retire and restore, so it has to
	// show what is already retired.
	fb, err := appOf(r).Store.FactBaseAll()
	if fail(w, err) {
		return
	}
	page := factsPage{
		chrome: chrome{Title: "Facts", Nav: "facts", OpenCount: s.openCount(r),
			Flash: r.URL.Query().Get("flash")},
		Profile: fb.Profile, Contacts: fb.Contacts,
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
	if err := appOf(r).Store.SetFactRetired(kind, id, retire); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if isHTMX(r) {
		// Re-render just this row in its new state.
		row, err := s.factRowByID(r, kind, id)
		if fail(w, err) {
			return
		}
		s.partial(w, "fact-row", row)
		return
	}
	http.Redirect(w, r, "/facts", http.StatusSeeOther)
}

// factRowByID rebuilds one row after its retired state changes.
func (s *server) factRowByID(r *http.Request, kind string, id int64) (factRow, error) {
	fb, err := appOf(r).Store.FactBaseAll()
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
	chrome
	Doc       *store.Document
	Highlight []string
	Frozen    bool
	Status    string
}

func (s *server) showResume(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	doc, err := appOf(r).Store.Assemble(id)
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
		frozen, status = s.frozen(r, doc.Job.ID)
	}
	s.render(w, "resume.html", resumePage{
		chrome: chrome{Title: title, Nav: "jobs", OpenCount: s.openCount(r)},
		Doc:    doc, Highlight: terms, Frozen: frozen, Status: status,
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
	res, err := appOf(r).Store.GetResume(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if res.PDFPath == "" {
		http.Error(w, "no PDF for this version — render it first", http.StatusNotFound)
		return
	}
	job, _ := appOf(r).Store.GetJob(res.JobID)
	profile, _ := appOf(r).Store.GetProfile()
	s.servePDF(w, r, res.PDFPath, downloadName(profile.Name, "resume", job))
}

func (s *server) coverPDF(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	c, err := appOf(r).Store.GetCoverLetter(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if c.PDFPath == "" {
		http.Error(w, "no PDF for this cover letter — render it first", http.StatusNotFound)
		return
	}
	job, _ := appOf(r).Store.GetJob(c.JobID)
	profile, _ := appOf(r).Store.GetProfile()
	s.servePDF(w, r, c.PDFPath, downloadName(profile.Name, "cover-letter", job))
}

func (s *server) coverTypst(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	c, err := appOf(r).Store.GetCoverLetter(id)
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
	c, err := appOf(r).Store.GetCoverLetter(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_, renderErr := appOf(r).RenderCover(id)
	if isHTMX(r) {
		fresh, err := appOf(r).Store.CoverLetterByJob(c.JobID)
		if fail(w, err) {
			return
		}
		job, _ := appOf(r).Store.GetJob(fresh.JobID)
		view := s.coverViewFor(r, fresh, job)
		if renderErr != nil {
			view.Error = renderErr.Error()
		}
		s.partial(w, "cover", view)
		return
	}
	redirect(w, r, fmt.Sprintf("/jobs/%d?tab=cover", c.JobID))
}

// resumeSentPDF serves the frozen copy: the document as it went out, not as it
// would render today.
func (s *server) resumeSentPDF(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	res, err := appOf(r).Store.GetResume(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if res.SentPDFPath == "" {
		http.Error(w, "no sent copy was captured for this resume", http.StatusNotFound)
		return
	}
	job, _ := appOf(r).Store.GetJob(res.JobID)
	profile, _ := appOf(r).Store.GetProfile()
	s.servePDF(w, r, res.SentPDFPath, downloadName(profile.Name, "resume-as-sent", job))
}

func (s *server) resumeTypst(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if fail(w, err) {
		return
	}
	res, err := appOf(r).Store.GetResume(id)
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
	res, err := appOf(r).Store.GetResume(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	out, renderErr := appOf(r).Render(id)

	if isHTMX(r) {
		resumes, err := appOf(r).Store.ListResumes(res.JobID)
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
		rdoc, rterms := s.latestDoc(r, resumes)
		rf, rs := s.frozen(r, res.JobID)
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

// currentURL is the page the browser is showing, as htmx reports it. A
// fragment response has no other source for the context it is swapped into.
func currentURL(r *http.Request) *url.URL {
	u, err := url.Parse(r.Header.Get("HX-Current-URL"))
	if err != nil {
		return &url.URL{}
	}
	return u
}

// currentJobID is the job open in the right pane, or 0 for none.
func currentJobID(r *http.Request) int64 {
	p := currentURL(r).Path
	if !strings.HasPrefix(p, "/jobs/") {
		return 0
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(p, "/jobs/"), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

func currentTab(r *http.Request) string {
	return validTab(currentURL(r).Query().Get("tab"))
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
