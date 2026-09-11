// Package mcpsrv exposes the resume tracker to Claude over the Model Context
// Protocol.
//
// The tool surface is deliberately shaped around one rule: Claude may select,
// order, and reword facts, but it may not invent them. Tailoring therefore
// happens through save_resume, which takes ids from the fact base rather than
// prose. When Claude wants to say something the fact base cannot support, the
// path is ask_questions -> the user answers in the web UI -> add_facts.
package mcpsrv

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/beetlebugorg/resumed/internal/app"
	"github.com/beetlebugorg/resumed/internal/store"
)

const instructions = `Tailor resumes from a fact base, never from imagination.

Workflow for a new job posting:
  1. add_job with the posting URL. If the fetch fails, ask the user to paste the
     description and call update_job with it.
  2. get_fact_base to see every true statement available, each with an id.
  3. Compare posting to facts. Where the posting asks for something the fact base
     does not cover but the user plausibly has, call ask_questions. Answers show
     up in the web UI; poll list_questions with status "answered", then promote
     good answers into the fact base with add_facts.
  4. save_resume, selecting the role/bullet/skill/project/patent ids that matter
     for this posting, ordered most relevant first. Rewordings go in the "text"
     field of a bullet: reframe emphasis and vocabulary to match the posting, but
     the claim must stay true to the underlying fact.
  5. Record reasoning with add_job_note so the choice is reviewable later.
  6. save_cover_letter when the application wants one, or when the resume alone
     leaves something important unsaid.

Keep resumes to the strongest material; dropping a weak role or bullet is
normal and expected.

Cover letters are prose, not a selection of ids, so the honesty rule has to be
carried by you rather than by the schema: every claim must be traceable to the
fact base, and a hiring manager's name is never to be invented. A letter earns
its place by saying what the resume cannot — why this candidate wants this job,
or how to read a career move the bullets leave ambiguous. Do not restate the
resume in paragraph form.`

// New builds the MCP server with every tool registered.
func New(a *app.App, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:        "resumed",
		Title:       "Resume Tailor",
		Description: "Track job applications and tailor resumes from a factual base.",
		Version:     version,
	}, &mcp.ServerOptions{Instructions: instructions})

	registerFactTools(s, a)
	registerJobTools(s, a)
	registerQuestionTools(s, a)
	registerResumeTools(s, a)
	registerCoverLetterTools(s, a)
	return s
}

// ok wraps any value as a tool result. Structured JSON keeps the model from
// having to parse prose.
func ok[T any](v T) (*mcp.CallToolResult, T, error) {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		var zero T
		return nil, zero, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}, v, nil
}

func fail[T any](format string, args ...any) (*mcp.CallToolResult, T, error) {
	var zero T
	return nil, zero, fmt.Errorf(format, args...)
}

// ------------------------------------------------------------------ facts

type emptyInput struct{}

type factBaseOutput struct {
	FactBase *store.FactBase `json:"fact_base"`
	Note     string          `json:"note"`
}

func registerFactTools(s *mcp.Server, a *app.App) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_fact_base",
		Title:       "Get fact base",
		Description: "Return every factual item available for resume building — profile, contacts, roles with bullets, projects, patents, and skills — each with the id needed by save_resume. Call this before tailoring.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in emptyInput) (*mcp.CallToolResult, factBaseOutput, error) {
		fb, err := a.Store.FactBase()
		if err != nil {
			return fail[factBaseOutput]("read fact base: %w", err)
		}
		return ok(factBaseOutput{
			FactBase: fb,
			Note:     "Use these ids in save_resume. Bullet text may be reworded for a posting, but the underlying claim must stay true.",
		})
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "add_facts",
		Title: "Add facts",
		Description: "Add newly confirmed facts to the fact base — typically after the user answers a question. " +
			"Only record things the user actually stated. Returns the ids of everything created.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in AddFactsInput) (*mcp.CallToolResult, AddFactsOutput, error) {
		return addFacts(a, in)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "retire_facts",
		Title: "Retire or restore facts",
		Description: "Retire a fact so new resumes stop using it, without deleting it. Use this when a fact is " +
			"superseded by a better-worded one, no longer worth claiming, or was recorded wrongly. " +
			"Retired facts disappear from get_fact_base and are rejected by save_resume, but resumes already " +
			"generated keep rendering exactly as they were, because they still reference the row. " +
			"Pass restore: true to bring a fact back.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in RetireFactsInput) (*mcp.CallToolResult, RetireFactsOutput, error) {
		if len(in.Facts) == 0 {
			return fail[RetireFactsOutput]("no facts supplied")
		}
		out := RetireFactsOutput{Restored: in.Restore}
		for _, f := range in.Facts {
			if err := a.Store.SetFactRetired(f.Kind, f.ID, !in.Restore); err != nil {
				return fail[RetireFactsOutput]("%w", err)
			}
			out.Changed = append(out.Changed, fmt.Sprintf("%s:%d", f.Kind, f.ID))
		}
		verb := "retired"
		if in.Restore {
			verb = "restored"
		}
		out.Note = fmt.Sprintf("%d fact(s) %s. Existing resume versions are unaffected and still render.", len(out.Changed), verb)
		return ok(out)
	})
}

type FactRef struct {
	Kind string `json:"kind" jsonschema:"one of: role, bullet, project, project_bullet, patent, skill"`
	ID   int64  `json:"id" jsonschema:"the fact id from get_fact_base"`
}

type RetireFactsInput struct {
	Facts   []FactRef `json:"facts"`
	Restore bool      `json:"restore,omitempty" jsonschema:"set true to un-retire these facts instead"`
}

type RetireFactsOutput struct {
	Changed  []string `json:"changed"`
	Restored bool     `json:"restored"`
	Note     string   `json:"note"`
}

type NewBullet struct {
	RoleID int64  `json:"role_id" jsonschema:"id of the role this bullet belongs to"`
	Text   string `json:"text" jsonschema:"the accomplishment, one sentence, factual"`
	Tags   string `json:"tags,omitempty" jsonschema:"comma-separated keywords for later matching"`
}

type NewSkill struct {
	Category string `json:"category" jsonschema:"skill group, e.g. Languages or Cloud Platforms"`
	Name     string `json:"name" jsonschema:"the skill itself"`
}

type NewProject struct {
	Name    string `json:"name"`
	URL     string `json:"url,omitempty"`
	Date    string `json:"date,omitempty"`
	Summary string `json:"summary,omitempty"`
	Tags    string `json:"tags,omitempty"`
}

type NewProjectBullet struct {
	ProjectID int64  `json:"project_id"`
	Text      string `json:"text"`
	Tags      string `json:"tags,omitempty"`
}

type NewRole struct {
	Company   string `json:"company"`
	Location  string `json:"location,omitempty"`
	Title     string `json:"title"`
	StartDate string `json:"start_date,omitempty" jsonschema:"e.g. Oct 2018"`
	EndDate   string `json:"end_date,omitempty" jsonschema:"e.g. Aug 2023 or Present"`
	Summary   string `json:"summary,omitempty"`
}

type AddFactsInput struct {
	Roles          []NewRole          `json:"roles,omitempty"`
	Bullets        []NewBullet        `json:"bullets,omitempty"`
	Projects       []NewProject       `json:"projects,omitempty"`
	ProjectBullets []NewProjectBullet `json:"project_bullets,omitempty"`
	Skills         []NewSkill         `json:"skills,omitempty"`
	Source         string             `json:"source,omitempty" jsonschema:"where this came from: interview (user answered a question) or resume"`
}

type AddFactsOutput struct {
	RoleIDs          []int64 `json:"role_ids,omitempty"`
	BulletIDs        []int64 `json:"bullet_ids,omitempty"`
	ProjectIDs       []int64 `json:"project_ids,omitempty"`
	ProjectBulletIDs []int64 `json:"project_bullet_ids,omitempty"`
	SkillIDs         []int64 `json:"skill_ids,omitempty"`
}

func addFacts(a *app.App, in AddFactsInput) (*mcp.CallToolResult, AddFactsOutput, error) {
	source := in.Source
	if source == "" {
		source = "interview"
	}
	var out AddFactsOutput
	for _, r := range in.Roles {
		id, err := a.Store.AddRole(store.Role{
			Company: r.Company, Location: r.Location, Title: r.Title,
			StartDate: r.StartDate, EndDate: r.EndDate, Summary: r.Summary,
		})
		if err != nil {
			return fail[AddFactsOutput]("add role: %w", err)
		}
		out.RoleIDs = append(out.RoleIDs, id)
	}
	for _, b := range in.Bullets {
		id, err := a.Store.AddBullet(store.Bullet{
			RoleID: b.RoleID, Text: b.Text, Tags: b.Tags, Source: source,
		})
		if err != nil {
			return fail[AddFactsOutput]("add bullet: %w", err)
		}
		out.BulletIDs = append(out.BulletIDs, id)
	}
	for _, p := range in.Projects {
		id, err := a.Store.AddProject(store.Project{
			Name: p.Name, URL: p.URL, Date: p.Date, Summary: p.Summary, Tags: p.Tags,
		})
		if err != nil {
			return fail[AddFactsOutput]("add project: %w", err)
		}
		out.ProjectIDs = append(out.ProjectIDs, id)
	}
	for _, b := range in.ProjectBullets {
		id, err := a.Store.AddProjectBullet(store.ProjectBullet{
			ProjectID: b.ProjectID, Text: b.Text, Tags: b.Tags, Source: source,
		})
		if err != nil {
			return fail[AddFactsOutput]("add project bullet: %w", err)
		}
		out.ProjectBulletIDs = append(out.ProjectBulletIDs, id)
	}
	for _, sk := range in.Skills {
		id, err := a.Store.AddSkill(store.Skill{Category: sk.Category, Name: sk.Name})
		if err != nil {
			return fail[AddFactsOutput]("add skill: %w", err)
		}
		out.SkillIDs = append(out.SkillIDs, id)
	}
	return ok(out)
}

// ------------------------------------------------------------------- jobs

type AddJobInput struct {
	URL         string `json:"url,omitempty" jsonschema:"link to the job posting"`
	Description string `json:"description,omitempty" jsonschema:"posting text; supply this when the URL cannot be fetched"`
	Company     string `json:"company,omitempty"`
	Title       string `json:"title,omitempty"`
	Location    string `json:"location,omitempty"`
	Notes       string `json:"notes,omitempty" jsonschema:"an initial note about this opportunity"`
}

type ListJobsInput struct {
	Status string `json:"status,omitempty" jsonschema:"filter by status: saved, tailoring, applied, interviewing, offer, rejected, closed"`
}

type ListJobsOutput struct {
	Jobs []store.Job `json:"jobs"`
}

type JobIDInput struct {
	JobID int64 `json:"job_id"`
}

type JobDetail struct {
	Job       *store.Job       `json:"job"`
	Notes     []store.JobNote  `json:"notes,omitempty"`
	Questions []store.Question `json:"questions,omitempty"`
	Resumes   []store.Resume   `json:"resumes,omitempty"`
}

type UpdateJobInput struct {
	JobID       int64  `json:"job_id"`
	Status      string `json:"status,omitempty" jsonschema:"saved, tailoring, applied, interviewing, offer, rejected, or closed"`
	Company     string `json:"company,omitempty"`
	Title       string `json:"title,omitempty"`
	Location    string `json:"location,omitempty"`
	Description string `json:"description,omitempty"`
}

type AddNoteInput struct {
	JobID  int64  `json:"job_id"`
	Note   string `json:"note"`
	Author string `json:"author,omitempty" jsonschema:"claude or user; defaults to claude"`
}

func registerJobTools(s *mcp.Server, a *app.App) {
	mcp.AddTool(s, &mcp.Tool{
		Name:  "add_job",
		Title: "Add job",
		Description: "Save a job posting. Give a URL and the posting is fetched and parsed automatically; " +
			"if fetching fails the job is still saved and you can supply the text later via update_job.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in AddJobInput) (*mcp.CallToolResult, app.AddJobResult, error) {
		res, err := a.AddJob(ctx, in.URL, in.Description, in.Company, in.Title, in.Location)
		if err != nil {
			return fail[app.AddJobResult]("add job: %w", err)
		}
		if strings.TrimSpace(in.Notes) != "" {
			if _, err := a.Store.AddJobNote(res.Job.ID, in.Notes, "claude"); err != nil {
				return fail[app.AddJobResult]("add note: %w", err)
			}
		}
		return ok(*res)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_jobs",
		Title:       "List jobs",
		Description: "List tracked jobs, most recently touched first. Descriptions are truncated; use get_job for the full posting.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ListJobsInput) (*mcp.CallToolResult, ListJobsOutput, error) {
		jobs, err := a.Store.ListJobs(in.Status)
		if err != nil {
			return fail[ListJobsOutput]("list jobs: %w", err)
		}
		for i := range jobs {
			jobs[i].Description = truncate(jobs[i].Description, 300)
		}
		return ok(ListJobsOutput{Jobs: jobs})
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_job",
		Title:       "Get job",
		Description: "Full detail for one job: the posting text, every note, its questions, and any resume versions already generated.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in JobIDInput) (*mcp.CallToolResult, JobDetail, error) {
		job, err := a.Store.GetJob(in.JobID)
		if err != nil {
			return fail[JobDetail]("%w", err)
		}
		notes, err := a.Store.ListJobNotes(in.JobID)
		if err != nil {
			return fail[JobDetail]("list notes: %w", err)
		}
		questions, err := a.Store.ListQuestions("", &in.JobID)
		if err != nil {
			return fail[JobDetail]("list questions: %w", err)
		}
		resumes, err := a.Store.ListResumes(in.JobID)
		if err != nil {
			return fail[JobDetail]("list resumes: %w", err)
		}
		return ok(JobDetail{Job: job, Notes: notes, Questions: questions, Resumes: resumes})
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "update_job",
		Title:       "Update job",
		Description: "Update a job's status or metadata. Only the fields you supply are changed.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in UpdateJobInput) (*mcp.CallToolResult, store.Job, error) {
		fields := map[string]any{}
		if in.Status != "" {
			if !validStatus(in.Status) {
				return fail[store.Job]("invalid status %q; want one of %s", in.Status, strings.Join(store.JobStatuses, ", "))
			}
			fields["status"] = in.Status
		}
		for k, v := range map[string]string{
			"company": in.Company, "title": in.Title,
			"location": in.Location, "description": in.Description,
		} {
			if v != "" {
				fields[k] = v
			}
		}
		// Status goes through the app, which freezes the sent copy on the move
		// into applied; the rest are plain field writes.
		if status, ok := fields["status"].(string); ok {
			delete(fields, "status")
			if err := a.SetJobStatus(in.JobID, status); err != nil {
				return fail[store.Job]("update job status: %w", err)
			}
		}
		if err := a.Store.UpdateJobFields(in.JobID, fields); err != nil {
			return fail[store.Job]("update job: %w", err)
		}
		job, err := a.Store.GetJob(in.JobID)
		if err != nil {
			return fail[store.Job]("%w", err)
		}
		return ok(*job)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "add_job_note",
		Title: "Add job note",
		Description: "Attach a timestamped note to a job — tailoring rationale, recruiter contact, interview feedback. " +
			"Notes are shown in the web UI and returned by get_job.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in AddNoteInput) (*mcp.CallToolResult, store.JobNote, error) {
		author := in.Author
		if author == "" {
			author = "claude"
		}
		id, err := a.Store.AddJobNote(in.JobID, in.Note, author)
		if err != nil {
			return fail[store.JobNote]("add note: %w", err)
		}
		return ok(store.JobNote{ID: id, JobID: in.JobID, Body: in.Note, Author: author})
	})
}

func validStatus(s string) bool {
	for _, v := range store.JobStatuses {
		if v == s {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// -------------------------------------------------------------- questions

type AskQuestion struct {
	Question  string `json:"question" jsonschema:"a specific question whose answer would strengthen this resume"`
	Rationale string `json:"rationale,omitempty" jsonschema:"what the posting asks for that the fact base does not yet cover"`
}

type AskQuestionsInput struct {
	JobID     *int64        `json:"job_id,omitempty" jsonschema:"the job that prompted these questions, if any"`
	Questions []AskQuestion `json:"questions"`
}

type AskQuestionsOutput struct {
	IDs  []int64 `json:"ids"`
	Note string  `json:"note"`
}

type ListQuestionsInput struct {
	Status string `json:"status,omitempty" jsonschema:"open, answered, or dismissed; omit for all"`
	JobID  *int64 `json:"job_id,omitempty"`
}

type ListQuestionsOutput struct {
	Questions []store.Question `json:"questions"`
}

func registerQuestionTools(s *mcp.Server, a *app.App) {
	mcp.AddTool(s, &mcp.Tool{
		Name:  "ask_questions",
		Title: "Ask questions",
		Description: "Queue questions for the user to answer in the web UI. Use this when a posting calls for experience " +
			"the fact base does not cover but the user plausibly has. Ask specific, answerable questions — " +
			"'what was the p99 latency of the registry API you built?' beats 'tell me about performance work'. " +
			"Answers do not arrive instantly; check back with list_questions.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in AskQuestionsInput) (*mcp.CallToolResult, AskQuestionsOutput, error) {
		if len(in.Questions) == 0 {
			return fail[AskQuestionsOutput]("no questions supplied")
		}
		var ids []int64
		for _, q := range in.Questions {
			id, err := a.Store.AddQuestion(store.Question{
				JobID: in.JobID, Question: q.Question, Rationale: q.Rationale,
			})
			if err != nil {
				return fail[AskQuestionsOutput]("add question: %w", err)
			}
			ids = append(ids, id)
		}
		return ok(AskQuestionsOutput{
			IDs:  ids,
			Note: "Queued. The user answers these in the web UI; call list_questions with status \"answered\" later, then promote the answers with add_facts.",
		})
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_questions",
		Title:       "List questions",
		Description: "List queued questions and any answers the user has given.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ListQuestionsInput) (*mcp.CallToolResult, ListQuestionsOutput, error) {
		qs, err := a.Store.ListQuestions(in.Status, in.JobID)
		if err != nil {
			return fail[ListQuestionsOutput]("list questions: %w", err)
		}
		return ok(ListQuestionsOutput{Questions: qs})
	})
}

// ---------------------------------------------------------------- resumes

type TailoredBullet struct {
	BulletID int64  `json:"bullet_id" jsonschema:"id from the fact base"`
	Text     string `json:"text,omitempty" jsonschema:"optional rewording for this posting; must remain true to the original fact"`
}

type TailoredRole struct {
	RoleID  int64            `json:"role_id"`
	Summary string           `json:"summary,omitempty" jsonschema:"optional one-line role summary tailored to the posting"`
	Bullets []TailoredBullet `json:"bullets,omitempty" jsonschema:"selected bullets, most relevant first"`
}

type TailoredProject struct {
	ProjectID int64            `json:"project_id"`
	Summary   string           `json:"summary,omitempty"`
	Bullets   []TailoredBullet `json:"bullets,omitempty" jsonschema:"project bullet ids, most relevant first"`
}

type SaveResumeInput struct {
	JobID     int64             `json:"job_id"`
	Summary   string            `json:"summary" jsonschema:"the summary paragraph at the top, written for this posting"`
	Rationale string            `json:"rationale,omitempty" jsonschema:"why you chose this material; stored for later review"`
	Roles     []TailoredRole    `json:"roles" jsonschema:"roles to include, most relevant first"`
	Projects  []TailoredProject `json:"projects,omitempty"`
	SkillIDs  []int64           `json:"skill_ids,omitempty" jsonschema:"skill ids to include, ordered; they are grouped by their category automatically"`
	PatentIDs []int64           `json:"patent_ids,omitempty"`
	Highlight []string          `json:"highlight,omitempty" jsonschema:"terms to bold wherever they appear in the summary, bullets, and project text (skills are left alone, since their category labels are already bold): the handful of things this posting most wants to see. Matching is case-insensitive and respects word boundaries. Emphasis is per-resume, so the same fact can bold different terms for different postings. Keep the list short; bolding half the page emphasises nothing"`
}

type SaveResumeOutput struct {
	app.RenderResult
	JobLabel string `json:"job_label"`
	Note     string `json:"note"`
}

type ResumeIDInput struct {
	ResumeID int64 `json:"resume_id"`
}

type GetResumeOutput struct {
	Resume *store.Resume `json:"resume"`
	Typst  string        `json:"typst"`
}

type SaveCoverLetterInput struct {
	JobID     int64             `json:"job_id"`
	Body      string            `json:"body" jsonschema:"the letter itself; separate paragraphs with a blank line. Three or four short paragraphs beats one long one."`
	Greeting  string            `json:"greeting,omitempty" jsonschema:"e.g. 'Dear Render Engineering Team,'. Do not invent a hiring manager's name."`
	Closing   string            `json:"closing,omitempty" jsonschema:"e.g. 'Sincerely,'. The name is added automatically."`
	Links     []store.CoverLink `json:"links,omitempty" jsonschema:"links printed under the signature. label is the link text, usually a project name; note is the description printed after it; url is the address. Use for a portfolio or repositories the letter refers to, rather than putting bare URLs in a paragraph."`
	Rationale string            `json:"rationale,omitempty" jsonschema:"which facts this letter leans on and why that argument suits this posting; stored for later review"`
}

type SaveCoverLetterOutput struct {
	app.CoverRenderResult
	JobLabel string `json:"job_label"`
	Note     string `json:"note"`
}

type CoverLetterIDInput struct {
	CoverLetterID int64 `json:"cover_letter_id"`
}

type GetCoverLetterOutput struct {
	CoverLetter *store.CoverLetter `json:"cover_letter"`
	Typst       string             `json:"typst,omitempty"`
	Note        string             `json:"note,omitempty"`
}

func registerResumeTools(s *mcp.Server, a *app.App) {
	mcp.AddTool(s, &mcp.Tool{
		Name:  "save_resume",
		Title: "Save tailored resume",
		Description: "Build a tailored resume for a job by selecting fact-base ids, then render it to Typst and PDF. " +
			"Every id must exist in the fact base — this is what keeps the resume honest. " +
			"Order matters: put the most relevant roles and bullets first. Saving creates a new version, " +
			"so earlier attempts are preserved.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in SaveResumeInput) (*mcp.CallToolResult, SaveResumeOutput, error) {
		job, err := a.Store.GetJob(in.JobID)
		if err != nil {
			return fail[SaveResumeOutput]("%w", err)
		}
		if len(in.Roles) == 0 {
			return fail[SaveResumeOutput]("select at least one role")
		}

		var items []store.ResumeItem
		pos := 0
		next := func() int { pos++; return pos }

		for _, r := range in.Roles {
			roleID := r.RoleID
			items = append(items, store.ResumeItem{
				Kind: store.KindRole, RefID: roleID,
				Position: next(), OverrideText: r.Summary,
			})
			for _, b := range r.Bullets {
				items = append(items, store.ResumeItem{
					Kind: store.KindBullet, RefID: b.BulletID, ParentRefID: &roleID,
					Position: next(), OverrideText: b.Text,
				})
			}
		}
		for _, p := range in.Projects {
			projID := p.ProjectID
			items = append(items, store.ResumeItem{
				Kind: store.KindProject, RefID: projID,
				Position: next(), OverrideText: p.Summary,
			})
			for _, b := range p.Bullets {
				items = append(items, store.ResumeItem{
					Kind: store.KindProjectBullet, RefID: b.BulletID, ParentRefID: &projID,
					Position: next(), OverrideText: b.Text,
				})
			}
		}
		for _, id := range in.SkillIDs {
			items = append(items, store.ResumeItem{Kind: store.KindSkill, RefID: id, Position: next()})
		}
		for _, id := range in.PatentIDs {
			items = append(items, store.ResumeItem{Kind: store.KindPatent, RefID: id, Position: next()})
		}

		resume, err := a.Store.CreateResume(in.JobID, in.Summary, in.Rationale, in.Highlight, items)
		if err != nil {
			return fail[SaveResumeOutput]("save resume: %w", err)
		}
		res, err := a.Render(resume.ID)
		if err != nil {
			return fail[SaveResumeOutput]("render resume %d: %w", resume.ID, err)
		}
		if job.Status == "saved" {
			_ = a.Store.UpdateJobFields(job.ID, map[string]any{"status": "tailoring"})
		}
		return ok(SaveResumeOutput{
			RenderResult: *res,
			JobLabel:     job.Label(),
			Note:         "Saved and rendered. Review it in the web UI; calling save_resume again replaces it.",
		})
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_resume",
		Title:       "Get resume",
		Description: "Fetch a job's stored resume including its generated Typst source.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ResumeIDInput) (*mcp.CallToolResult, GetResumeOutput, error) {
		r, err := a.Store.GetResume(in.ResumeID)
		if err != nil {
			return fail[GetResumeOutput]("%w", err)
		}
		return ok(GetResumeOutput{Resume: r, Typst: r.Typst})
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "render_resume",
		Title:       "Re-render resume",
		Description: "Regenerate the Typst and PDF for a job's resume. Useful after editing facts it references.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ResumeIDInput) (*mcp.CallToolResult, app.RenderResult, error) {
		res, err := a.Render(in.ResumeID)
		if err != nil {
			return fail[app.RenderResult]("render: %w", err)
		}
		return ok(*res)
	})
}

func registerCoverLetterTools(s *mcp.Server, a *app.App) {
	mcp.AddTool(s, &mcp.Tool{
		Name:  "save_cover_letter",
		Title: "Save cover letter",
		Description: "Write or replace a job's cover letter, then render it to Typst and PDF beside the resume. " +
			"Unlike a resume, a letter is prose rather than a selection of fact ids — but every claim in it must " +
			"still be traceable to the fact base. Do not state anything the facts do not support, and do not " +
			"invent a hiring manager's name. Record the argument you are making in `rationale`. " +
			"One letter per job: saving again replaces it.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in SaveCoverLetterInput) (*mcp.CallToolResult, SaveCoverLetterOutput, error) {
		job, err := a.Store.GetJob(in.JobID)
		if err != nil {
			return fail[SaveCoverLetterOutput]("%w", err)
		}
		if strings.TrimSpace(in.Body) == "" {
			return fail[SaveCoverLetterOutput]("the letter body is empty")
		}
		letter, err := a.Store.SaveCoverLetter(store.CoverLetter{
			JobID: in.JobID, Body: in.Body, Greeting: in.Greeting,
			Closing: in.Closing, Links: in.Links, Rationale: in.Rationale,
		})
		if err != nil {
			return fail[SaveCoverLetterOutput]("save cover letter: %w", err)
		}
		res, err := a.RenderCover(letter.ID)
		if err != nil {
			return fail[SaveCoverLetterOutput]("render cover letter %d: %w", letter.ID, err)
		}
		return ok(SaveCoverLetterOutput{
			CoverRenderResult: *res,
			JobLabel:          job.Label(),
			Note:              "Saved and rendered. Calling save_cover_letter again replaces it.",
		})
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_cover_letter",
		Title:       "Get cover letter",
		Description: "Fetch a job's cover letter and its generated Typst source. Returns an empty result when none has been written yet.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in JobIDInput) (*mcp.CallToolResult, GetCoverLetterOutput, error) {
		c, err := a.Store.CoverLetterByJob(in.JobID)
		if err != nil {
			return fail[GetCoverLetterOutput]("%w", err)
		}
		if c == nil {
			return ok(GetCoverLetterOutput{Note: "no cover letter written for this job yet"})
		}
		return ok(GetCoverLetterOutput{CoverLetter: c, Typst: c.Typst})
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "render_cover_letter",
		Title:       "Re-render cover letter",
		Description: "Regenerate the Typst and PDF for a cover letter. Useful after the profile or contacts change.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in CoverLetterIDInput) (*mcp.CallToolResult, app.CoverRenderResult, error) {
		res, err := a.RenderCover(in.CoverLetterID)
		if err != nil {
			return fail[app.CoverRenderResult]("render: %w", err)
		}
		return ok(*res)
	})
}
