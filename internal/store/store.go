// Package store owns the SQLite database: the fact base plus the job
// application track.
package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // pure-Go driver, no cgo
)

//go:embed schema.sql
var schemaSQL string

type Store struct {
	db   *sql.DB
	Path string
}

// Open opens (creating if needed) the database at path and applies the schema.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db, Path: path}, nil
}

// factTables are the tables a fact can live in, and the ones retirement
// applies to.
var factTables = []string{"roles", "bullets", "projects", "project_bullets", "patents", "skills"}

// migrate brings an existing database up to the current schema. CREATE TABLE
// IF NOT EXISTS does nothing to a table that already exists, so new columns
// have to be added explicitly.
func migrate(db *sql.DB) error {
	for _, table := range factTables {
		has, err := hasColumn(db, table, "retired_at")
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN retired_at TEXT`); err != nil {
			return fmt.Errorf("add retired_at to %s: %w", table, err)
		}
	}

	// Collapse to one resume per job. Earlier builds kept a version chain; the
	// model is now "the current resume for this job", so anything older than
	// the newest version per job is dropped (resume_items cascade). Without
	// this, a database written by an older build would show every past version
	// as if each were current.
	if _, err := db.Exec(`
		DELETE FROM resumes
		WHERE id NOT IN (
			SELECT id FROM resumes r
			WHERE r.version = (SELECT MAX(version) FROM resumes WHERE job_id = r.job_id)
		)`); err != nil {
		return fmt.Errorf("collapse resume versions: %w", err)
	}
	return nil
}

func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// retireClause filters out retired rows unless the caller wants them.
func retireClause(includeRetired bool, prefix string) string {
	if includeRetired {
		return ""
	}
	return " " + prefix + " retired_at IS NULL"
}

// SetFactRetired retires or restores a fact. Retiring never deletes: resumes
// already generated still reference the row, and must keep rendering.
func (s *Store) SetFactRetired(kind string, id int64, retired bool) error {
	table, ok := factTable(kind)
	if !ok {
		return fmt.Errorf("unknown fact kind %q", kind)
	}
	var res sql.Result
	var err error
	if retired {
		res, err = s.db.Exec(`UPDATE `+table+` SET retired_at = datetime('now') WHERE id = ?`, id)
	} else {
		res, err = s.db.Exec(`UPDATE `+table+` SET retired_at = NULL WHERE id = ?`, id)
	}
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return fmt.Errorf("%s %d not found", kind, id)
	}
	return err
}

// factTable maps a resume-item kind to its fact table.
func factTable(kind string) (string, bool) {
	t := map[string]string{
		KindRole:          "roles",
		KindBullet:        "bullets",
		KindProject:       "projects",
		KindProjectBullet: "project_bullets",
		KindPatent:        "patents",
		KindSkill:         "skills",
	}[kind]
	return t, t != ""
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) DB() *sql.DB { return s.db }

// ------------------------------------------------------------------- profile

func (s *Store) GetProfile() (Profile, error) {
	var p Profile
	err := s.db.QueryRow(`SELECT name, location, summary FROM profile WHERE id = 1`).
		Scan(&p.Name, &p.Location, &p.Summary)
	if err == sql.ErrNoRows {
		return Profile{}, nil
	}
	return p, err
}

func (s *Store) SetProfile(p Profile) error {
	_, err := s.db.Exec(`
		INSERT INTO profile (id, name, location, summary, updated_at)
		VALUES (1, ?, ?, ?, datetime('now'))
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			location = excluded.location,
			summary = excluded.summary,
			updated_at = datetime('now')`,
		p.Name, p.Location, p.Summary)
	return err
}

func (s *Store) ListContacts() ([]Contact, error) {
	rows, err := s.db.Query(`SELECT id, kind, value, url, position FROM contacts ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Contact
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.ID, &c.Kind, &c.Value, &c.URL, &c.Position); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) AddContact(c Contact) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO contacts (kind, value, url, position) VALUES (?, ?, ?, ?)`,
		c.Kind, c.Value, c.URL, c.Position)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) DeleteContact(id int64) error {
	_, err := s.db.Exec(`DELETE FROM contacts WHERE id = ?`, id)
	return err
}

// ----------------------------------------------------------------- fact base

// FactBase returns the facts available for new tailoring. Retired facts are
// excluded — that is what "retired" means.
func (s *Store) FactBase() (*FactBase, error) { return s.factBase(false) }

// FactBaseAll includes retired facts. Assemble needs this: a resume that was
// already generated references its facts by id, and must keep rendering even
// after one of them is retired.
func (s *Store) FactBaseAll() (*FactBase, error) { return s.factBase(true) }

func (s *Store) factBase(includeRetired bool) (*FactBase, error) {
	p, err := s.GetProfile()
	if err != nil {
		return nil, err
	}
	contacts, err := s.ListContacts()
	if err != nil {
		return nil, err
	}
	roles, err := s.ListRoles(includeRetired)
	if err != nil {
		return nil, err
	}
	projects, err := s.ListProjects(includeRetired)
	if err != nil {
		return nil, err
	}
	patents, err := s.ListPatents(includeRetired)
	if err != nil {
		return nil, err
	}
	skills, err := s.ListSkills(includeRetired)
	if err != nil {
		return nil, err
	}
	return &FactBase{
		Profile:  p,
		Contacts: contacts,
		Roles:    roles,
		Projects: projects,
		Patents:  patents,
		Skills:   skills,
	}, nil
}

func (s *Store) ListRoles(includeRetired bool) ([]Role, error) {
	rows, err := s.db.Query(`
		SELECT id, company, location, title, start_date, end_date, summary, position, COALESCE(retired_at, '')
		FROM roles` + retireClause(includeRetired, "WHERE") + ` ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roles []Role
	byID := map[int64]int{}
	for rows.Next() {
		var r Role
		if err := rows.Scan(&r.ID, &r.Company, &r.Location, &r.Title,
			&r.StartDate, &r.EndDate, &r.Summary, &r.Position, &r.RetiredAt); err != nil {
			return nil, err
		}
		byID[r.ID] = len(roles)
		roles = append(roles, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	brows, err := s.db.Query(`SELECT id, role_id, text, tags, source, position, COALESCE(retired_at, '')
		FROM bullets` + retireClause(includeRetired, "WHERE") + ` ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer brows.Close()
	for brows.Next() {
		var b Bullet
		if err := brows.Scan(&b.ID, &b.RoleID, &b.Text, &b.Tags, &b.Source, &b.Position, &b.RetiredAt); err != nil {
			return nil, err
		}
		if i, ok := byID[b.RoleID]; ok {
			roles[i].Bullets = append(roles[i].Bullets, b)
		}
	}
	return roles, brows.Err()
}

func (s *Store) AddRole(r Role) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO roles (company, location, title, start_date, end_date, summary, position)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.Company, r.Location, r.Title, r.StartDate, r.EndDate, r.Summary, r.Position)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateRole(r Role) error {
	_, err := s.db.Exec(`
		UPDATE roles SET company=?, location=?, title=?, start_date=?, end_date=?, summary=?, position=?
		WHERE id=?`,
		r.Company, r.Location, r.Title, r.StartDate, r.EndDate, r.Summary, r.Position, r.ID)
	return err
}

func (s *Store) DeleteRole(id int64) error {
	_, err := s.db.Exec(`DELETE FROM roles WHERE id = ?`, id)
	return err
}

func (s *Store) AddBullet(b Bullet) (int64, error) {
	if b.Source == "" {
		b.Source = "resume"
	}
	res, err := s.db.Exec(`INSERT INTO bullets (role_id, text, tags, source, position) VALUES (?, ?, ?, ?, ?)`,
		b.RoleID, b.Text, b.Tags, b.Source, b.Position)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateBullet(b Bullet) error {
	_, err := s.db.Exec(`UPDATE bullets SET text=?, tags=?, position=? WHERE id=?`,
		b.Text, b.Tags, b.Position, b.ID)
	return err
}

func (s *Store) DeleteBullet(id int64) error {
	_, err := s.db.Exec(`DELETE FROM bullets WHERE id = ?`, id)
	return err
}

func (s *Store) ListProjects(includeRetired bool) ([]Project, error) {
	rows, err := s.db.Query(`SELECT id, name, url, date, summary, tags, position, COALESCE(retired_at, '')
		FROM projects` + retireClause(includeRetired, "WHERE") + ` ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []Project
	byID := map[int64]int{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.URL, &p.Date, &p.Summary, &p.Tags, &p.Position, &p.RetiredAt); err != nil {
			return nil, err
		}
		byID[p.ID] = len(projects)
		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	brows, err := s.db.Query(`SELECT id, project_id, text, tags, source, position, COALESCE(retired_at, '')
		FROM project_bullets` + retireClause(includeRetired, "WHERE") + ` ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer brows.Close()
	for brows.Next() {
		var b ProjectBullet
		if err := brows.Scan(&b.ID, &b.ProjectID, &b.Text, &b.Tags, &b.Source, &b.Position, &b.RetiredAt); err != nil {
			return nil, err
		}
		if i, ok := byID[b.ProjectID]; ok {
			projects[i].Bullets = append(projects[i].Bullets, b)
		}
	}
	return projects, brows.Err()
}

func (s *Store) AddProject(p Project) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO projects (name, url, date, summary, tags, position) VALUES (?, ?, ?, ?, ?, ?)`,
		p.Name, p.URL, p.Date, p.Summary, p.Tags, p.Position)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateProject(p Project) error {
	_, err := s.db.Exec(`UPDATE projects SET name=?, url=?, date=?, summary=?, tags=?, position=? WHERE id=?`,
		p.Name, p.URL, p.Date, p.Summary, p.Tags, p.Position, p.ID)
	return err
}

func (s *Store) DeleteProject(id int64) error {
	_, err := s.db.Exec(`DELETE FROM projects WHERE id = ?`, id)
	return err
}

func (s *Store) AddProjectBullet(b ProjectBullet) (int64, error) {
	if b.Source == "" {
		b.Source = "resume"
	}
	res, err := s.db.Exec(`INSERT INTO project_bullets (project_id, text, tags, source, position) VALUES (?, ?, ?, ?, ?)`,
		b.ProjectID, b.Text, b.Tags, b.Source, b.Position)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateProjectBullet(b ProjectBullet) error {
	_, err := s.db.Exec(`UPDATE project_bullets SET text=?, tags=?, position=? WHERE id=?`,
		b.Text, b.Tags, b.Position, b.ID)
	return err
}

func (s *Store) DeleteProjectBullet(id int64) error {
	_, err := s.db.Exec(`DELETE FROM project_bullets WHERE id = ?`, id)
	return err
}

func (s *Store) ListPatents(includeRetired bool) ([]Patent, error) {
	rows, err := s.db.Query(`SELECT id, patent_id, url, date, summary, position, COALESCE(retired_at, '')
		FROM patents` + retireClause(includeRetired, "WHERE") + ` ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Patent
	for rows.Next() {
		var p Patent
		if err := rows.Scan(&p.ID, &p.PatentID, &p.URL, &p.Date, &p.Summary, &p.Position, &p.RetiredAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) AddPatent(p Patent) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO patents (patent_id, url, date, summary, position) VALUES (?, ?, ?, ?, ?)`,
		p.PatentID, p.URL, p.Date, p.Summary, p.Position)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) DeletePatent(id int64) error {
	_, err := s.db.Exec(`DELETE FROM patents WHERE id = ?`, id)
	return err
}

func (s *Store) ListSkills(includeRetired bool) ([]Skill, error) {
	rows, err := s.db.Query(`SELECT id, category, name, tags, position, COALESCE(retired_at, '')
		FROM skills` + retireClause(includeRetired, "WHERE") + ` ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Skill
	for rows.Next() {
		var sk Skill
		if err := rows.Scan(&sk.ID, &sk.Category, &sk.Name, &sk.Tags, &sk.Position, &sk.RetiredAt); err != nil {
			return nil, err
		}
		out = append(out, sk)
	}
	return out, rows.Err()
}

func (s *Store) AddSkill(sk Skill) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO skills (category, name, tags, position) VALUES (?, ?, ?, ?)`,
		sk.Category, sk.Name, sk.Tags, sk.Position)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateSkill(sk Skill) error {
	_, err := s.db.Exec(`UPDATE skills SET category=?, name=?, tags=?, position=? WHERE id=?`,
		sk.Category, sk.Name, sk.Tags, sk.Position, sk.ID)
	return err
}

func (s *Store) DeleteSkill(id int64) error {
	_, err := s.db.Exec(`DELETE FROM skills WHERE id = ?`, id)
	return err
}

// ---------------------------------------------------------------------- jobs

func (s *Store) AddJob(j Job) (int64, error) {
	if j.Status == "" {
		j.Status = "saved"
	}
	res, err := s.db.Exec(`
		INSERT INTO jobs (url, company, title, location, description, status)
		VALUES (?, ?, ?, ?, ?, ?)`,
		j.URL, j.Company, j.Title, j.Location, j.Description, j.Status)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ListJobs(status string) ([]Job, error) {
	q := `SELECT id, url, company, title, location, description, status, created_at, updated_at FROM jobs`
	var args []any
	if status != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY updated_at DESC, id DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

type rowScanner interface{ Scan(dest ...any) error }

func scanJob(sc rowScanner) (Job, error) {
	var j Job
	err := sc.Scan(&j.ID, &j.URL, &j.Company, &j.Title, &j.Location,
		&j.Description, &j.Status, &j.CreatedAt, &j.UpdatedAt)
	return j, err
}

func (s *Store) GetJob(id int64) (*Job, error) {
	row := s.db.QueryRow(`
		SELECT id, url, company, title, location, description, status, created_at, updated_at
		FROM jobs WHERE id = ?`, id)
	j, err := scanJob(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("job %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// JobByURL finds an existing job with the same URL so adding twice does not
// create duplicates.
func (s *Store) JobByURL(url string) (*Job, error) {
	if strings.TrimSpace(url) == "" {
		return nil, nil
	}
	row := s.db.QueryRow(`
		SELECT id, url, company, title, location, description, status, created_at, updated_at
		FROM jobs WHERE url = ? ORDER BY id LIMIT 1`, url)
	j, err := scanJob(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// UpdateJobFields applies a partial update; only non-nil fields are written.
func (s *Store) UpdateJobFields(id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	allowed := map[string]bool{
		"url": true, "company": true, "title": true,
		"location": true, "description": true, "status": true,
	}
	var sets []string
	var args []any
	for k, v := range fields {
		if !allowed[k] {
			return fmt.Errorf("field %q is not updatable", k)
		}
		sets = append(sets, k+" = ?")
		args = append(args, v)
	}
	sets = append(sets, "updated_at = datetime('now')")
	args = append(args, id)
	_, err := s.db.Exec(`UPDATE jobs SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	return err
}

func (s *Store) DeleteJob(id int64) error {
	_, err := s.db.Exec(`DELETE FROM jobs WHERE id = ?`, id)
	return err
}

func (s *Store) AddJobNote(jobID int64, body, author string) (int64, error) {
	if author == "" {
		author = "user"
	}
	res, err := s.db.Exec(`INSERT INTO job_notes (job_id, body, author) VALUES (?, ?, ?)`, jobID, body, author)
	if err != nil {
		return 0, err
	}
	_, _ = s.db.Exec(`UPDATE jobs SET updated_at = datetime('now') WHERE id = ?`, jobID)
	return res.LastInsertId()
}

func (s *Store) ListJobNotes(jobID int64) ([]JobNote, error) {
	rows, err := s.db.Query(`SELECT id, job_id, body, author, created_at FROM job_notes WHERE job_id = ? ORDER BY id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobNote
	for rows.Next() {
		var n JobNote
		if err := rows.Scan(&n.ID, &n.JobID, &n.Body, &n.Author, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) DeleteJobNote(id int64) error {
	_, err := s.db.Exec(`DELETE FROM job_notes WHERE id = ?`, id)
	return err
}

// ----------------------------------------------------------------- questions

func (s *Store) AddQuestion(q Question) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO questions (job_id, question, rationale) VALUES (?, ?, ?)`,
		q.JobID, q.Question, q.Rationale)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ListQuestions(status string, jobID *int64) ([]Question, error) {
	q := `SELECT id, job_id, question, rationale, answer, status, created_at, COALESCE(answered_at, '')
	      FROM questions`
	var where []string
	var args []any
	if status != "" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	if jobID != nil {
		where = append(where, "job_id = ?")
		args = append(args, *jobID)
	}
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += ` ORDER BY id DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Question
	for rows.Next() {
		var qq Question
		if err := rows.Scan(&qq.ID, &qq.JobID, &qq.Question, &qq.Rationale,
			&qq.Answer, &qq.Status, &qq.CreatedAt, &qq.AnsweredAt); err != nil {
			return nil, err
		}
		out = append(out, qq)
	}
	return out, rows.Err()
}

// AnswerQuestion records or revises an answer. answered_at is set once and
// preserved on later edits — fixing a typo should not make the answer look new.
func (s *Store) AnswerQuestion(id int64, answer string) error {
	_, err := s.db.Exec(`
		UPDATE questions
		SET answer = ?, status = 'answered', answered_at = COALESCE(answered_at, datetime('now'))
		WHERE id = ?`, answer, id)
	return err
}

func (s *Store) SetQuestionStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE questions SET status = ? WHERE id = ?`, status, id)
	return err
}

// ------------------------------------------------------------------- resumes

// CreateResume writes the tailored resume for a job, replacing whatever was
// there. Only the current resume is kept: re-tailoring supersedes the previous
// attempt rather than accumulating versions. Items must reference existing,
// non-retired fact rows; validation happens here so a bad tailoring call fails
// loudly instead of silently producing an empty resume.
func (s *Store) CreateResume(jobID int64, summary, rationale string, items []ResumeItem) (*Resume, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Drop the previous resume for this job. resume_items cascade.
	if _, err := tx.Exec(`DELETE FROM resumes WHERE job_id = ?`, jobID); err != nil {
		return nil, err
	}
	const version = 1

	res, err := tx.Exec(`INSERT INTO resumes (job_id, version, summary, rationale) VALUES (?, ?, ?, ?)`,
		jobID, version, summary, rationale)
	if err != nil {
		return nil, err
	}
	resumeID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	stmt, err := tx.Prepare(`
		INSERT INTO resume_items (resume_id, kind, ref_id, parent_ref_id, position, override_text)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	for i, it := range items {
		if err := validateItemRef(tx, it); err != nil {
			return nil, err
		}
		pos := it.Position
		if pos == 0 {
			pos = i
		}
		if _, err := stmt.Exec(resumeID, it.Kind, it.RefID, it.ParentRefID, pos, it.OverrideText); err != nil {
			return nil, err
		}
	}

	if _, err := tx.Exec(`UPDATE jobs SET updated_at = datetime('now') WHERE id = ?`, jobID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetResume(resumeID)
}

// validateItemRef enforces the core invariant: every rendered line traces back
// to a row in the fact base.
func validateItemRef(tx *sql.Tx, it ResumeItem) error {
	table := map[string]string{
		KindRole:          "roles",
		KindBullet:        "bullets",
		KindProject:       "projects",
		KindProjectBullet: "project_bullets",
		KindPatent:        "patents",
		KindSkill:         "skills",
	}[it.Kind]
	if table == "" {
		return fmt.Errorf("unknown resume item kind %q", it.Kind)
	}
	var retiredAt sql.NullString
	err := tx.QueryRow(`SELECT retired_at FROM `+table+` WHERE id = ?`, it.RefID).Scan(&retiredAt)
	if err == sql.ErrNoRows {
		return fmt.Errorf("%s id %d does not exist in the fact base", it.Kind, it.RefID)
	}
	if err != nil {
		return err
	}
	if retiredAt.Valid && retiredAt.String != "" {
		return fmt.Errorf("%s id %d was retired on %s and cannot be used in a new resume; "+
			"restore it first if this is still true", it.Kind, it.RefID, retiredAt.String)
	}
	return nil
}

func (s *Store) GetResume(id int64) (*Resume, error) {
	var r Resume
	err := s.db.QueryRow(`
		SELECT id, job_id, version, summary, rationale, typst, pdf_path, created_at
		FROM resumes WHERE id = ?`, id).
		Scan(&r.ID, &r.JobID, &r.Version, &r.Summary, &r.Rationale, &r.Typst, &r.PDFPath, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("resume %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) ListResumes(jobID int64) ([]Resume, error) {
	rows, err := s.db.Query(`
		SELECT id, job_id, version, summary, rationale, typst, pdf_path, created_at
		FROM resumes WHERE job_id = ? ORDER BY version DESC`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Resume
	for rows.Next() {
		var r Resume
		if err := rows.Scan(&r.ID, &r.JobID, &r.Version, &r.Summary, &r.Rationale,
			&r.Typst, &r.PDFPath, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LatestResume returns the highest-version resume for a job, or nil if none.
func (s *Store) LatestResume(jobID int64) (*Resume, error) {
	list, err := s.ListResumes(jobID)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

func (s *Store) SetResumeOutput(id int64, typstSrc, pdfPath string) error {
	_, err := s.db.Exec(`UPDATE resumes SET typst = ?, pdf_path = ? WHERE id = ?`, typstSrc, pdfPath, id)
	return err
}

func (s *Store) ListResumeItems(resumeID int64) ([]ResumeItem, error) {
	rows, err := s.db.Query(`
		SELECT id, resume_id, kind, ref_id, parent_ref_id, position, override_text
		FROM resume_items WHERE resume_id = ? ORDER BY position, id`, resumeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResumeItem
	for rows.Next() {
		var it ResumeItem
		if err := rows.Scan(&it.ID, &it.ResumeID, &it.Kind, &it.RefID,
			&it.ParentRefID, &it.Position, &it.OverrideText); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) DeleteResume(id int64) error {
	_, err := s.db.Exec(`DELETE FROM resumes WHERE id = ?`, id)
	return err
}

// ------------------------------------------------------------ cover letters

const coverLetterCols = `id, job_id, version, greeting, body, closing, rationale, typst, pdf_path, created_at, updated_at`

func scanCoverLetter(sc rowScanner) (CoverLetter, error) {
	var c CoverLetter
	err := sc.Scan(&c.ID, &c.JobID, &c.Version, &c.Greeting, &c.Body, &c.Closing,
		&c.Rationale, &c.Typst, &c.PDFPath, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

// SaveCoverLetter writes the one letter a job is allowed. Saving again bumps
// the version and replaces the text, clearing the rendered output so a stale
// PDF can never outlive the prose it was built from.
func (s *Store) SaveCoverLetter(c CoverLetter) (*CoverLetter, error) {
	if strings.TrimSpace(c.Body) == "" {
		return nil, fmt.Errorf("cover letter body is empty")
	}
	if _, err := s.GetJob(c.JobID); err != nil {
		return nil, err
	}
	_, err := s.db.Exec(`
		INSERT INTO cover_letters (job_id, version, greeting, body, closing, rationale)
		VALUES (?, 1, ?, ?, ?, ?)
		ON CONFLICT(job_id) DO UPDATE SET
			version    = cover_letters.version + 1,
			greeting   = excluded.greeting,
			body       = excluded.body,
			closing    = excluded.closing,
			rationale  = excluded.rationale,
			typst      = '',
			pdf_path   = '',
			updated_at = datetime('now')`,
		c.JobID, c.Greeting, c.Body, c.Closing, c.Rationale)
	if err != nil {
		return nil, err
	}
	return s.CoverLetterByJob(c.JobID)
}

func (s *Store) GetCoverLetter(id int64) (*CoverLetter, error) {
	c, err := scanCoverLetter(s.db.QueryRow(
		`SELECT `+coverLetterCols+` FROM cover_letters WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("cover letter %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// CoverLetterByJob returns a job's letter, or nil when none has been written.
// A missing letter is an ordinary state, not an error.
func (s *Store) CoverLetterByJob(jobID int64) (*CoverLetter, error) {
	c, err := scanCoverLetter(s.db.QueryRow(
		`SELECT `+coverLetterCols+` FROM cover_letters WHERE job_id = ?`, jobID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) SetCoverLetterOutput(id int64, typstSrc, pdfPath string) error {
	_, err := s.db.Exec(
		`UPDATE cover_letters SET typst = ?, pdf_path = ?, updated_at = datetime('now') WHERE id = ?`,
		typstSrc, pdfPath, id)
	return err
}

func (s *Store) DeleteCoverLetter(id int64) error {
	_, err := s.db.Exec(`DELETE FROM cover_letters WHERE id = ?`, id)
	return err
}
