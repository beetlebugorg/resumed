-- resumed schema
--
-- Two halves:
--   1. The FACT BASE (profile, contacts, roles, bullets, projects, patents,
--      skills). This is the single source of truth about what is actually
--      true. Nothing else may invent content.
--   2. The APPLICATION TRACK (jobs, job_notes, questions, resumes,
--      resume_items). A tailored resume is a *selection* over the fact base:
--      resume_items reference fact rows by id. An item may carry an
--      override_text to reword a bullet for a specific job, but it can never
--      exist without a fact row behind it. That constraint is what keeps
--      generated resumes honest.
--
-- RETIREMENT: fact rows carry a nullable retired_at. A retired fact is kept
-- forever but excluded from new tailoring. Retirement is deliberately NOT a
-- delete: resumes already sent reference these rows by id, and they must keep
-- rendering exactly as they were sent. So retired facts stay visible to
-- Assemble (which rebuilds a stored version) and are hidden only from the
-- fact base that new tailoring selects from.

PRAGMA foreign_keys = ON;

-- ---------------------------------------------------------------- fact base

CREATE TABLE IF NOT EXISTS profile (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    name       TEXT NOT NULL,
    location   TEXT NOT NULL DEFAULT '',
    summary    TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS contacts (
    id       INTEGER PRIMARY KEY,
    kind     TEXT NOT NULL,            -- email | github | linkedin | website | phone
    value    TEXT NOT NULL,            -- display text, e.g. github.com/beetlebugorg
    url      TEXT NOT NULL DEFAULT '', -- href; empty means render as plain text
    position INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS roles (
    id         INTEGER PRIMARY KEY,
    company    TEXT NOT NULL,
    location   TEXT NOT NULL DEFAULT '',
    title      TEXT NOT NULL,
    start_date TEXT NOT NULL DEFAULT '', -- free text, e.g. "Oct 2018"
    end_date   TEXT NOT NULL DEFAULT '', -- free text, e.g. "Aug 2023" / "Present"
    summary    TEXT NOT NULL DEFAULT '',
    position   INTEGER NOT NULL DEFAULT 0,
    retired_at TEXT
);

CREATE TABLE IF NOT EXISTS bullets (
    id       INTEGER PRIMARY KEY,
    role_id  INTEGER NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    text     TEXT NOT NULL,
    tags     TEXT NOT NULL DEFAULT '',       -- comma-separated, for matching
    source   TEXT NOT NULL DEFAULT 'resume', -- resume | interview | note
    position INTEGER NOT NULL DEFAULT 0,
    retired_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_bullets_role ON bullets(role_id);

CREATE TABLE IF NOT EXISTS projects (
    id       INTEGER PRIMARY KEY,
    name     TEXT NOT NULL,
    url      TEXT NOT NULL DEFAULT '',
    date     TEXT NOT NULL DEFAULT '',
    summary  TEXT NOT NULL DEFAULT '',
    tags     TEXT NOT NULL DEFAULT '',
    position INTEGER NOT NULL DEFAULT 0,
    retired_at TEXT
);

CREATE TABLE IF NOT EXISTS project_bullets (
    id         INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    text       TEXT NOT NULL,
    tags       TEXT NOT NULL DEFAULT '',
    source     TEXT NOT NULL DEFAULT 'resume',
    position   INTEGER NOT NULL DEFAULT 0,
    retired_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_project_bullets_project ON project_bullets(project_id);

CREATE TABLE IF NOT EXISTS patents (
    id        INTEGER PRIMARY KEY,
    patent_id TEXT NOT NULL,            -- e.g. US11831495B2
    url       TEXT NOT NULL DEFAULT '',
    date      TEXT NOT NULL DEFAULT '',
    summary   TEXT NOT NULL DEFAULT '',
    position  INTEGER NOT NULL DEFAULT 0,
    retired_at TEXT
);

CREATE TABLE IF NOT EXISTS skills (
    id       INTEGER PRIMARY KEY,
    category TEXT NOT NULL,             -- e.g. "Languages"
    name     TEXT NOT NULL,             -- e.g. "Go/Golang"
    tags     TEXT NOT NULL DEFAULT '',
    position INTEGER NOT NULL DEFAULT 0,
    retired_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_skills_category ON skills(category);

-- -------------------------------------------------------- application track

CREATE TABLE IF NOT EXISTS jobs (
    id          INTEGER PRIMARY KEY,
    url         TEXT NOT NULL DEFAULT '',
    company     TEXT NOT NULL DEFAULT '',
    title       TEXT NOT NULL DEFAULT '',
    location    TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '', -- extracted job posting text
    status      TEXT NOT NULL DEFAULT 'saved',
                -- saved | tailoring | applied | interviewing | offer | rejected | closed
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);

CREATE TABLE IF NOT EXISTS job_notes (
    id         INTEGER PRIMARY KEY,
    job_id     INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    body       TEXT NOT NULL,
    author     TEXT NOT NULL DEFAULT 'user', -- user | claude
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_job_notes_job ON job_notes(job_id);

-- Questions Claude raises while tailoring. Answering one in the web UI is how
-- new factual material enters the system; answered questions are the audit
-- trail for facts that were not in the original resume.
CREATE TABLE IF NOT EXISTS questions (
    id          INTEGER PRIMARY KEY,
    job_id      INTEGER REFERENCES jobs(id) ON DELETE SET NULL,
    question    TEXT NOT NULL,
    rationale   TEXT NOT NULL DEFAULT '', -- why Claude is asking
    answer      TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'open', -- open | answered | dismissed
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    answered_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_questions_status ON questions(status);

CREATE TABLE IF NOT EXISTS resumes (
    id         INTEGER PRIMARY KEY,
    job_id     INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    version    INTEGER NOT NULL,
    summary    TEXT NOT NULL DEFAULT '', -- tailored summary paragraph
    rationale  TEXT NOT NULL DEFAULT '', -- why these facts were chosen
    typst      TEXT NOT NULL DEFAULT '',
    pdf_path   TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (job_id, version)
);
CREATE INDEX IF NOT EXISTS idx_resumes_job ON resumes(job_id);

-- A cover letter is deliberately NOT modelled like a resume. A resume is a
-- selection over the fact base, and resume_items is what enforces that. A
-- letter is prose — an argument about why this candidate suits this posting —
-- so it is stored as text. The honesty constraint still applies, but it is
-- carried by `rationale` (which facts the letter leans on, and why) rather
-- than by row references. One letter per job: saving again bumps version and
-- replaces the text in place.
CREATE TABLE IF NOT EXISTS cover_letters (
    id         INTEGER PRIMARY KEY,
    job_id     INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    version    INTEGER NOT NULL DEFAULT 1,
    greeting   TEXT NOT NULL DEFAULT '', -- e.g. "Dear Render Engineering Team,"
    body       TEXT NOT NULL DEFAULT '', -- paragraphs separated by blank lines
    closing    TEXT NOT NULL DEFAULT '', -- e.g. "Sincerely,"
    rationale  TEXT NOT NULL DEFAULT '', -- why this argument, from which facts
    typst      TEXT NOT NULL DEFAULT '',
    pdf_path   TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (job_id)
);

-- One row per rendered line. kind+ref_id point back into the fact base.
CREATE TABLE IF NOT EXISTS resume_items (
    id            INTEGER PRIMARY KEY,
    resume_id     INTEGER NOT NULL REFERENCES resumes(id) ON DELETE CASCADE,
    kind          TEXT NOT NULL, -- role | bullet | project | project_bullet | patent | skill
    ref_id        INTEGER NOT NULL,
    parent_ref_id INTEGER,       -- bullet -> role id; project_bullet -> project id
    position      INTEGER NOT NULL DEFAULT 0,
    override_text TEXT NOT NULL DEFAULT '' -- reworded for this job; must stay factual
);
CREATE INDEX IF NOT EXISTS idx_resume_items_resume ON resume_items(resume_id);
