// Package app holds the operations shared by the MCP server and the web UI, so
// both surfaces behave identically.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/beetlebugorg/resumed/internal/fetch"
	"github.com/beetlebugorg/resumed/internal/render"
	"github.com/beetlebugorg/resumed/internal/store"
)

type App struct {
	Store *store.Store
	// OutRoot is where generated resumes land, one directory per job, following
	// the existing jobs/<company>/<title>/ convention in this repo.
	OutRoot string
}

// New builds the app. OutRoot is resolved to an absolute path here because the
// rendered artifact's location is *stored* in the database: a relative --out
// would record a path that only resolves from the directory the render happened
// to run in, so `resumed render --out jobs` followed by `resumed serve --out
// ../jobs` would leave the web UI unable to find its own PDF.
func New(s *store.Store, outRoot string) *App {
	if abs, err := filepath.Abs(outRoot); err == nil {
		outRoot = abs
	}
	return &App{Store: s, OutRoot: outRoot}
}

// Slug reduces text to a filesystem-safe token.
func Slug(s string) string {
	var b strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// JobDir is the output directory for a job's generated resumes.
func (a *App) JobDir(j *store.Job) string {
	company := Slug(j.Company)
	title := Slug(j.Title)
	switch {
	case company != "" && title != "":
		return filepath.Join(a.OutRoot, company, title)
	case company != "":
		return filepath.Join(a.OutRoot, company, fmt.Sprintf("job-%d", j.ID))
	default:
		return filepath.Join(a.OutRoot, fmt.Sprintf("job-%d", j.ID))
	}
}

// AddJobResult reports what happened when a job was added.
type AddJobResult struct {
	Job       *store.Job `json:"job"`
	Existing  bool       `json:"existing"`
	FetchNote string     `json:"fetch_note,omitempty"`
}

// AddJob stores a job posting. When a URL is given it is fetched and parsed;
// a fetch failure is reported but does not block creation, because plenty of
// job pages are JavaScript-rendered and the description can be pasted in later.
func (a *App) AddJob(ctx context.Context, url, description, company, title, location string) (*AddJobResult, error) {
	url = strings.TrimSpace(url)
	if url == "" && strings.TrimSpace(description) == "" {
		return nil, errors.New("provide a url, a description, or both")
	}

	if existing, err := a.Store.JobByURL(url); err == nil && existing != nil {
		return &AddJobResult{Job: existing, Existing: true,
			FetchNote: "a job with this URL already exists; not creating a duplicate"}, nil
	}

	var note string
	if url != "" && strings.TrimSpace(description) == "" {
		res, err := fetch.Fetch(ctx, url)
		switch {
		case err != nil && (res == nil || strings.TrimSpace(res.Description) == ""):
			note = "could not fetch the posting: " + err.Error() +
				". Job saved with the URL only — pass description text to update it."
		case err != nil:
			note = "partial fetch: " + err.Error()
			fallthrough
		default:
			if res != nil {
				description = res.Description
				if company == "" {
					company = res.Company
				}
				if title == "" {
					title = res.Title
				}
				if location == "" {
					location = res.Location
				}
				if note == "" {
					note = "fetched via " + res.Source
				}
			}
		}
	}

	j := store.Job{
		URL: url, Company: company, Title: title,
		Location: location, Description: description, Status: "saved",
	}
	id, err := a.Store.AddJob(j)
	if err != nil {
		return nil, err
	}
	saved, err := a.Store.GetJob(id)
	if err != nil {
		return nil, err
	}
	return &AddJobResult{Job: saved, FetchNote: note}, nil
}

// RenderResult reports the artifacts written for a resume version.
type RenderResult struct {
	ResumeID  int64  `json:"resume_id"`
	Version   int    `json:"version"`
	TypstPath string `json:"typst_path"`
	PDFPath   string `json:"pdf_path,omitempty"`
	Warning   string `json:"warning,omitempty"`
}

// Render assembles, writes, and compiles a stored resume. A missing typst
// binary is a warning, not an error — the .typ source is still produced.
func (a *App) Render(resumeID int64) (*RenderResult, error) {
	doc, err := a.Store.Assemble(resumeID)
	if err != nil {
		return nil, err
	}
	// Re-rendering a sent resume would overwrite the file an employer is
	// holding. The fact base and the template move on; the sent document must
	// not. This is the guard that makes "cannot change after applied" true in
	// practice, because most drift arrives through a re-render rather than
	// through a deliberate edit.
	if doc.Job != nil {
		frozen, status, err := a.Store.ResumeFrozen(doc.Job.ID)
		if err != nil {
			return nil, err
		}
		if frozen {
			return nil, fmt.Errorf("job %d is %s: its resume is frozen, and re-rendering would replace the copy already sent. Move the job back to tailoring to change it", doc.Job.ID, status)
		}
	}
	src := render.Typst(doc)

	// One resume per job, so the filename is stable: re-tailoring overwrites in
	// place instead of littering the directory with versions.
	dir := a.JobDir(doc.Job)
	typPath, err := render.Write(dir, "resume", src)
	if err != nil {
		return nil, err
	}

	out := &RenderResult{ResumeID: resumeID, Version: doc.Resume.Version, TypstPath: typPath}
	pdfPath, err := render.PDF(typPath)
	if err != nil {
		if errors.Is(err, render.ErrNoTypst) {
			out.Warning = err.Error()
		} else {
			// A compile error is worth surfacing loudly; the source is on disk
			// so the caller can inspect it.
			return out, err
		}
	} else {
		out.PDFPath = pdfPath
	}

	if err := a.Store.SetResumeOutput(resumeID, src, out.PDFPath); err != nil {
		return out, err
	}
	return out, nil
}

// CoverRenderResult reports the artifacts written for a cover letter.
type CoverRenderResult struct {
	CoverLetterID int64  `json:"cover_letter_id"`
	Version       int    `json:"version"`
	TypstPath     string `json:"typst_path"`
	PDFPath       string `json:"pdf_path,omitempty"`
	Warning       string `json:"warning,omitempty"`
}

// RenderCover writes and compiles a stored cover letter. It lands in the same
// per-job directory as the resume, so an application is one folder.
func (a *App) RenderCover(coverID int64) (*CoverRenderResult, error) {
	c, err := a.Store.GetCoverLetter(coverID)
	if err != nil {
		return nil, err
	}
	job, err := a.Store.GetJob(c.JobID)
	if err != nil {
		return nil, err
	}
	profile, err := a.Store.GetProfile()
	if err != nil {
		return nil, err
	}
	contacts, err := a.Store.ListContacts()
	if err != nil {
		return nil, err
	}

	src := render.CoverLetterTypst(profile, contacts, job, c)
	typPath, err := render.WriteWith(a.JobDir(job), "cover-letter", src,
		render.CoverTemplateName, render.CoverTemplate())
	if err != nil {
		return nil, err
	}

	out := &CoverRenderResult{CoverLetterID: coverID, Version: c.Version, TypstPath: typPath}
	pdfPath, err := render.PDF(typPath)
	if err != nil {
		if errors.Is(err, render.ErrNoTypst) {
			out.Warning = err.Error()
		} else {
			return out, err
		}
	} else {
		out.PDFPath = pdfPath
	}

	if err := a.Store.SetCoverLetterOutput(coverID, src, out.PDFPath); err != nil {
		return out, err
	}
	return out, nil
}

// SetJobStatus changes a job's status and, on the move into a status that means
// an application is out, freezes a copy of the resume as sent.
//
// The snapshot is taken here rather than in the store because it copies a file:
// the live PDF is overwritten in place by later renders, so preserving the
// bytes needs a second file on disk, not just a database row.
func (a *App) SetJobStatus(jobID int64, status string) error {
	before, err := a.Store.GetJob(jobID)
	if err != nil {
		return err
	}
	if err := a.Store.UpdateJobFields(jobID, map[string]any{"status": status}); err != nil {
		return err
	}
	// Only on the transition in. Re-saving "applied" over "applied" must not
	// re-snapshot, or a render slipped in between would become the record.
	if !store.IsSentStatus(status) || store.IsSentStatus(before.Status) {
		return nil
	}
	return a.SnapshotSentResume(jobID)
}

// SnapshotSentResume freezes the current resume for a job as the sent copy. It
// is safe to call when there is no resume, and safe to call twice: the store
// write is a no-op once a snapshot exists.
func (a *App) SnapshotSentResume(jobID int64) error {
	res, err := a.Store.LatestResume(jobID)
	if err != nil || res == nil || res.Sent() {
		return err
	}
	sentPDF := ""
	if res.PDFPath != "" {
		dir := filepath.Dir(res.PDFPath)
		base := strings.TrimSuffix(filepath.Base(res.PDFPath), filepath.Ext(res.PDFPath))
		sentPDF = filepath.Join(dir, base+".sent.pdf")
		if err := copyFile(res.PDFPath, sentPDF); err != nil {
			// A missing PDF should not block the status change; the Typst
			// source is the more important half of the record and is stored
			// in the database either way.
			sentPDF = ""
		}
	}
	return a.Store.MarkResumeSent(res.ID, res.Typst, sentPDF)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
