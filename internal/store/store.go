// Package store owns the SQLite database: the fact base plus the job
// application track.
package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

	// Fact versions. Editing a fact appends a row rather than changing one, so
	// a resume keeps rendering the text it was built from. fact_id groups the
	// versions of one fact; the row id identifies the version, and that is what
	// resume_items points at.
	for _, table := range factTables {
		has, err := hasColumn(db, table, "fact_id")
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN fact_id INTEGER`); err != nil {
			return fmt.Errorf("add fact_id to %s: %w", table, err)
		}
		if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN version INTEGER NOT NULL DEFAULT 1`); err != nil {
			return fmt.Errorf("add version to %s: %w", table, err)
		}
		// Existing rows are each the first version of their own fact.
		if _, err := db.Exec(`UPDATE ` + table + ` SET fact_id = id WHERE fact_id IS NULL`); err != nil {
			return fmt.Errorf("backfill fact_id on %s: %w", table, err)
		}
	}

	// Bullet nesting. A bullet may name a parent bullet, which groups one
	// engagement inside a role under a lead-in.
	if has, err := hasColumn(db, "bullets", "parent_id"); err != nil {
		return err
	} else if !has {
		if _, err := db.Exec(`ALTER TABLE bullets ADD COLUMN parent_id INTEGER`); err != nil {
			return fmt.Errorf("add parent_id to bullets: %w", err)
		}
	}
	// Outside the guard: a database created from schema.sql already has the
	// column and still needs the index.
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_bullets_parent ON bullets(parent_id)`); err != nil {
		return fmt.Errorf("index bullets.parent_id: %w", err)
	}

	// Emphasis terms arrived after the resumes table did.
	if has, err := hasColumn(db, "resumes", "highlight"); err != nil {
		return err
	} else if !has {
		if _, err := db.Exec(`ALTER TABLE resumes ADD COLUMN highlight TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add highlight to resumes: %w", err)
		}
	}

	// Signature links arrived after the cover_letters table did.
	if has, err := hasColumn(db, "cover_letters", "links"); err != nil {
		return err
	} else if !has {
		if _, err := db.Exec(`ALTER TABLE cover_letters ADD COLUMN links TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add links to cover_letters: %w", err)
		}
	}

	// The sent snapshot arrived after the resumes table did.
	for _, col := range []string{"sent_typst", "sent_pdf_path", "sent_at"} {
		has, err := hasColumn(db, "resumes", col)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE resumes ADD COLUMN ` + col + ` TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add %s to resumes: %w", col, err)
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

// factScope decides which rows of a versioned fact table a query sees. Retired
// and superseded are different things and want different treatment: a retired
// fact is still a fact you might restore, while a superseded version is just an
// older wording of a fact that is already on screen.
type factScope int

const (
	// scopeActive is what new tailoring may draw from: the current version of
	// each fact, minus anything retired.
	scopeActive factScope = iota
	// scopeCurrent is the editing view: the current version of each fact,
	// retired ones included so they can be restored. Superseded versions stay
	// hidden, because listing them would show one row per edit.
	scopeCurrent
	// scopeHistory is every row ever written. Rendering a stored resume needs
	// it, since the resume points at the exact version it was built from.
	scopeHistory
)

// factFilter restricts a fact table to the rows a scope admits.
func factFilter(table string, sc factScope) string {
	if sc == scopeHistory {
		return ""
	}
	// COALESCE because a row inserted without a fact_id is the first version of
	// its own fact, and NULL never equals NULL in SQL.
	current := " WHERE id = (SELECT MAX(v.id) FROM " + table + " v" +
		" WHERE COALESCE(v.fact_id, v.id) = COALESCE(" + table + ".fact_id, " + table + ".id))"
	if sc == scopeCurrent {
		return current
	}
	return current + " AND retired_at IS NULL"
}

// factIDOf maps every row id in a versioned table to the stable fact id it
// belongs to, superseded rows included. Children point at whichever version of
// their parent existed when they were written, so attaching a child to its
// parent has to go through this: correcting a parent gives it a new row id, but
// never a new fact id.
func (s *Store) factIDOf(table string) (map[int64]int64, error) {
	rows, err := s.db.Query(`SELECT id, COALESCE(fact_id, id) FROM ` + table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, factID int64
		if err := rows.Scan(&id, &factID); err != nil {
			return nil, err
		}
		out[id] = factID
	}
	return out, rows.Err()
}

// retireClause filters out retired rows unless the caller wants them.
func retireClause(includeRetired bool, prefix string) string {
	if includeRetired {
		return ""
	}
	return " " + prefix + " retired_at IS NULL"
}

// factColumns are the fields UpdateFact may set, per table. Anything outside
// this list is structural (ids, versions, positions) or bookkeeping.
var factColumns = map[string][]string{
	"roles":           {"company", "location", "title", "start_date", "end_date", "summary"},
	"bullets":         {"text", "tags", "parent_id"},
	"projects":        {"name", "url", "date", "summary", "tags"},
	"project_bullets": {"text", "tags"},
	"patents":         {"patent_id", "url", "date", "summary"},
	"skills":          {"name", "category"},
}

// UpdateFact records a correction as a new version of a fact. The old row stays
// exactly as it is, so every resume that selected it renders the same text it
// rendered before. The new row becomes what new tailoring sees.
//
// It returns the id of the new version, which is what resume_items would point at
// from here on.
func (s *Store) UpdateFact(kind string, id int64, fields map[string]string) (int64, error) {
	table, ok := factTable(kind)
	if !ok {
		return 0, fmt.Errorf("unknown fact kind %q", kind)
	}
	allowed := factColumns[table]
	if len(fields) == 0 {
		return 0, fmt.Errorf("no fields to update")
	}
	for k := range fields {
		if !slices.Contains(allowed, k) {
			return 0, fmt.Errorf("%s has no updatable field %q", table, k)
		}
	}

	cols, err := s.columnsOf(table)
	if err != nil {
		return 0, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var factID int64
	var version int
	if err := tx.QueryRow(`SELECT COALESCE(fact_id, id), version FROM `+table+` WHERE id = ?`, id).
		Scan(&factID, &version); err == sql.ErrNoRows {
		return 0, fmt.Errorf("%s %d not found", table, id)
	} else if err != nil {
		return 0, err
	}

	// Copy every column except the row identity, overriding what changed.
	var names []string
	var values []string
	var args []any
	for _, c := range cols {
		switch c {
		case "id":
			continue
		case "version":
			names = append(names, c)
			values = append(values, "?")
			args = append(args, version+1)
		case "fact_id":
			names = append(names, c)
			values = append(values, "?")
			args = append(args, factID)
		case "retired_at":
			// A correction is not a retirement.
			names = append(names, c)
			values = append(values, "NULL")
		default:
			names = append(names, c)
			if v, ok := fields[c]; ok {
				values = append(values, "?")
				args = append(args, v)
			} else {
				values = append(values, c)
			}
		}
	}

	res, err := tx.Exec(`INSERT INTO `+table+` (`+strings.Join(names, ", ")+`) `+
		`SELECT `+strings.Join(values, ", ")+` FROM `+table+` WHERE id = ?`, append(args, id)...)
	if err != nil {
		return 0, err
	}
	newID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return newID, tx.Commit()
}

// columnsOf reads a table's column names in declaration order.
func (s *Store) columnsOf(table string) ([]string, error) {
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
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
func (s *Store) FactBase() (*FactBase, error) { return s.factBase(scopeActive) }

// FactBaseAll includes retired facts, so the facts page can offer to restore
// them. It still shows one row per fact: a correction supersedes the wording it
// replaced rather than adding a second fact.
func (s *Store) FactBaseAll() (*FactBase, error) { return s.factBase(scopeCurrent) }

// FactBaseHistory returns every version of every fact, retired or not. Assemble
// needs this: a resume that was already generated references its facts by row
// id, and must keep rendering the exact version it selected.
func (s *Store) FactBaseHistory() (*FactBase, error) { return s.factBase(scopeHistory) }

func (s *Store) factBase(sc factScope) (*FactBase, error) {
	p, err := s.GetProfile()
	if err != nil {
		return nil, err
	}
	contacts, err := s.ListContacts()
	if err != nil {
		return nil, err
	}
	roles, err := s.ListRoles(sc)
	if err != nil {
		return nil, err
	}
	projects, err := s.ListProjects(sc)
	if err != nil {
		return nil, err
	}
	patents, err := s.ListPatents(sc)
	if err != nil {
		return nil, err
	}
	skills, err := s.ListSkills(sc)
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

func (s *Store) ListRoles(sc factScope) ([]Role, error) {
	// A bullet's role_id names the role row that existed when the bullet was
	// written, which is not the current row once the role has been corrected.
	// Both resolve to the same fact id, so group on that.
	roleFact, err := s.factIDOf("roles")
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`
		SELECT id, company, location, title, start_date, end_date, summary, position, COALESCE(retired_at, '')
		FROM roles` + factFilter("roles", sc) + ` ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roles []Role
	byFact := map[int64]int{}
	for rows.Next() {
		var r Role
		if err := rows.Scan(&r.ID, &r.Company, &r.Location, &r.Title,
			&r.StartDate, &r.EndDate, &r.Summary, &r.Position, &r.RetiredAt); err != nil {
			return nil, err
		}
		// Under scopeHistory several versions of one role are listed; the last
		// wins the bullets so that every bullet still surfaces exactly once.
		byFact[roleFact[r.ID]] = len(roles)
		roles = append(roles, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	brows, err := s.db.Query(`SELECT id, role_id, COALESCE(parent_id, 0), text, tags, source, position,
		COALESCE(retired_at, '') FROM bullets` + factFilter("bullets", sc) + ` ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer brows.Close()
	for brows.Next() {
		var b Bullet
		if err := brows.Scan(&b.ID, &b.RoleID, &b.ParentID, &b.Text, &b.Tags, &b.Source,
			&b.Position, &b.RetiredAt); err != nil {
			return nil, err
		}
		if i, ok := byFact[roleFact[b.RoleID]]; ok {
			roles[i].Bullets = append(roles[i].Bullets, b)
		}
	}
	if err := brows.Err(); err != nil {
		return nil, err
	}
	// byFact holds indexes into this slice, so sorting runs after the bullets
	// are attached.
	SortRolesByDate(roles)
	return roles, nil
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

// NestRoleBullets groups each role's bullets under their parents, in place.
// The fact mapping a child needs to find a corrected parent lives here, so
// callers outside the store nest through this rather than building it again.
func (s *Store) NestRoleBullets(roles []Role) error {
	factOf, err := s.factIDOf("bullets")
	if err != nil {
		return err
	}
	for i := range roles {
		roles[i].Bullets = NestBullets(roles[i].Bullets, factOf)
	}
	return nil
}

// nullID converts an unset id to NULL, so parent_id holds a real reference or
// nothing. Zero is not a valid row id.
func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func (s *Store) AddBullet(b Bullet) (int64, error) {
	if b.Source == "" {
		b.Source = "resume"
	}
	res, err := s.db.Exec(
		`INSERT INTO bullets (role_id, parent_id, text, tags, source, position) VALUES (?, ?, ?, ?, ?, ?)`,
		b.RoleID, nullID(b.ParentID), b.Text, b.Tags, b.Source, b.Position)
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

func (s *Store) ListProjects(sc factScope) ([]Project, error) {
	// Same reasoning as ListRoles: group children on the parent's fact id, not
	// on the row id they happen to point at.
	projFact, err := s.factIDOf("projects")
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`SELECT id, name, url, date, summary, tags, position, COALESCE(retired_at, '')
		FROM projects` + factFilter("projects", sc) + ` ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []Project
	byFact := map[int64]int{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.URL, &p.Date, &p.Summary, &p.Tags, &p.Position, &p.RetiredAt); err != nil {
			return nil, err
		}
		byFact[projFact[p.ID]] = len(projects)
		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	brows, err := s.db.Query(`SELECT id, project_id, text, tags, source, position, COALESCE(retired_at, '')
		FROM project_bullets` + factFilter("project_bullets", sc) + ` ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer brows.Close()
	for brows.Next() {
		var b ProjectBullet
		if err := brows.Scan(&b.ID, &b.ProjectID, &b.Text, &b.Tags, &b.Source, &b.Position, &b.RetiredAt); err != nil {
			return nil, err
		}
		if i, ok := byFact[projFact[b.ProjectID]]; ok {
			projects[i].Bullets = append(projects[i].Bullets, b)
		}
	}
	if err := brows.Err(); err != nil {
		return nil, err
	}
	// byFact holds indexes into this slice, so sorting runs after the bullets
	// are attached.
	SortProjectsByDate(projects)
	return projects, nil
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

func (s *Store) ListPatents(sc factScope) ([]Patent, error) {
	rows, err := s.db.Query(`SELECT id, patent_id, url, date, summary, position, COALESCE(retired_at, '')
		FROM patents` + factFilter("patents", sc) + ` ORDER BY position, id`)
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

func (s *Store) ListSkills(sc factScope) ([]Skill, error) {
	rows, err := s.db.Query(`SELECT id, category, name, tags, position, COALESCE(retired_at, '')
		FROM skills` + factFilter("skills", sc) + ` ORDER BY position, id`)
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

// ListJobNotes returns a job's notes newest first. The most recent note is the
// one worth reading — an interview outcome or a change of plan supersedes what
// came before — so it leads rather than sitting at the bottom of a long list.
func (s *Store) ListJobNotes(jobID int64) ([]JobNote, error) {
	rows, err := s.db.Query(`SELECT id, job_id, body, author, created_at FROM job_notes WHERE job_id = ? ORDER BY id DESC`, jobID)
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

// JobNoteOwner returns the job a note belongs to. Deleting a note redraws
// that job's note list, and the row is gone by then.
func (s *Store) JobNoteOwner(id int64) (int64, error) {
	var jobID int64
	err := s.db.QueryRow(`SELECT job_id FROM job_notes WHERE id = ?`, id).Scan(&jobID)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("note %d not found", id)
	}
	return jobID, err
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

// ResumeFrozen reports whether a job's resume may still be changed, and the
// status that decided it. Freezing is keyed on the job rather than on the
// snapshot so the rule reads the way a person would say it: once you have
// applied, the resume you applied with stops moving.
func (s *Store) ResumeFrozen(jobID int64) (bool, string, error) {
	var status string
	err := s.db.QueryRow(`SELECT status FROM jobs WHERE id = ?`, jobID).Scan(&status)
	if err == sql.ErrNoRows {
		return false, "", fmt.Errorf("job %d not found", jobID)
	}
	if err != nil {
		return false, "", err
	}
	return IsSentStatus(status), status, nil
}

// MarkResumeSent freezes a copy of what was sent. It is deliberately
// write-once: a second call on the same resume is a no-op, so moving a job
// applied -> interviewing -> offer cannot overwrite the original with a later
// render.
func (s *Store) MarkResumeSent(resumeID int64, typst, pdfPath string) error {
	_, err := s.db.Exec(`
		UPDATE resumes
		   SET sent_typst = ?, sent_pdf_path = ?, sent_at = datetime('now')
		 WHERE id = ? AND sent_at = ''`, typst, pdfPath, resumeID)
	return err
}

// joinHighlight and splitHighlight move emphasis terms between the []string the
// rest of the program uses and the newline-separated column. Blank entries are
// dropped on the way in so an empty list and a list of empty strings store the
// same way.
func joinHighlight(terms []string) string {
	var keep []string
	for _, t := range terms {
		if t = strings.TrimSpace(t); t != "" {
			keep = append(keep, t)
		}
	}
	return strings.Join(keep, "\n")
}

func splitHighlight(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, t := range strings.Split(s, "\n") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// CreateResume writes the tailored resume for a job, replacing whatever was
// there. Only the current resume is kept: re-tailoring supersedes the previous
// attempt rather than accumulating versions. Items must reference existing,
// non-retired fact rows; validation happens here so a bad tailoring call fails
// loudly instead of silently producing an empty resume.
func (s *Store) CreateResume(jobID int64, summary, rationale string, highlight []string, items []ResumeItem) (*Resume, error) {
	frozen, status, err := s.ResumeFrozen(jobID)
	if err != nil {
		return nil, err
	}
	if frozen {
		return nil, fmt.Errorf("job %d is %s: its resume is frozen because a copy is already out with an employer. Move the job back to tailoring to replace it", jobID, status)
	}

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

	res, err := tx.Exec(`INSERT INTO resumes (job_id, version, summary, rationale, highlight) VALUES (?, ?, ?, ?, ?)`,
		jobID, version, summary, rationale, joinHighlight(highlight))
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
	var hl string
	err := s.db.QueryRow(`
		SELECT id, job_id, version, summary, rationale, highlight, typst, pdf_path, created_at,
		       sent_typst, sent_pdf_path, sent_at
		FROM resumes WHERE id = ?`, id).
		Scan(&r.ID, &r.JobID, &r.Version, &r.Summary, &r.Rationale, &hl, &r.Typst, &r.PDFPath, &r.CreatedAt,
			&r.SentTypst, &r.SentPDFPath, &r.SentAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("resume %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	r.Highlight = splitHighlight(hl)
	return &r, nil
}

func (s *Store) ListResumes(jobID int64) ([]Resume, error) {
	rows, err := s.db.Query(`
		SELECT id, job_id, version, summary, rationale, highlight, typst, pdf_path, created_at,
		       sent_typst, sent_pdf_path, sent_at
		FROM resumes WHERE job_id = ? ORDER BY version DESC`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Resume
	for rows.Next() {
		var r Resume
		var hl string
		if err := rows.Scan(&r.ID, &r.JobID, &r.Version, &r.Summary, &r.Rationale,
			&hl, &r.Typst, &r.PDFPath, &r.CreatedAt,
			&r.SentTypst, &r.SentPDFPath, &r.SentAt); err != nil {
			return nil, err
		}
		r.Highlight = splitHighlight(hl)
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

const coverLetterCols = `id, job_id, version, greeting, body, closing, links, rationale, typst, pdf_path, created_at, updated_at`

func scanCoverLetter(sc rowScanner) (CoverLetter, error) {
	var c CoverLetter
	var links string
	err := sc.Scan(&c.ID, &c.JobID, &c.Version, &c.Greeting, &c.Body, &c.Closing,
		&links, &c.Rationale, &c.Typst, &c.PDFPath, &c.CreatedAt, &c.UpdatedAt)
	c.Links = splitLinks(links)
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
		INSERT INTO cover_letters (job_id, version, greeting, body, closing, links, rationale)
		VALUES (?, 1, ?, ?, ?, ?, ?)
		ON CONFLICT(job_id) DO UPDATE SET
			version    = cover_letters.version + 1,
			greeting   = excluded.greeting,
			body       = excluded.body,
			closing    = excluded.closing,
			links      = excluded.links,
			rationale  = excluded.rationale,
			typst      = '',
			pdf_path   = '',
			updated_at = datetime('now')`,
		c.JobID, c.Greeting, c.Body, c.Closing, joinLinks(c.Links), c.Rationale)
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
