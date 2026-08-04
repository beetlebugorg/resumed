package store

import (
	"fmt"
	"sort"
)

// SkillGroup is one "Category: a, b, c" line.
type SkillGroup struct {
	Category string   `json:"category"`
	Names    []string `json:"names"`
}

// Document is a tailored resume resolved against the fact base and ready to
// render. Every field here came from a fact row (possibly reworded by an
// item override) — nothing is synthesised at render time.
type Document struct {
	Profile     Profile      `json:"profile"`
	Contacts    []Contact    `json:"contacts"`
	Summary     string       `json:"summary"`
	Roles       []Role       `json:"roles"`
	Projects    []Project    `json:"projects"`
	Patents     []Patent     `json:"patents"`
	SkillGroups []SkillGroup `json:"skill_groups"`
	Job         *Job         `json:"job,omitempty"`
	Resume      *Resume      `json:"resume,omitempty"`
}

// Assemble resolves a stored resume into a renderable Document, applying
// selection, ordering, and per-item text overrides.
func (s *Store) Assemble(resumeID int64) (*Document, error) {
	r, err := s.GetResume(resumeID)
	if err != nil {
		return nil, err
	}
	job, err := s.GetJob(r.JobID)
	if err != nil {
		return nil, err
	}
	// Deliberately FactBaseAll: a stored resume references its facts by id and
	// must keep rendering byte-for-byte even if one of them was later retired.
	// Using FactBase here would break every version that cited a retired fact.
	fb, err := s.FactBaseAll()
	if err != nil {
		return nil, err
	}
	items, err := s.ListResumeItems(resumeID)
	if err != nil {
		return nil, err
	}

	summary := r.Summary
	if summary == "" {
		summary = fb.Profile.Summary
	}
	doc := &Document{
		Profile:  fb.Profile,
		Contacts: fb.Contacts,
		Summary:  summary,
		Job:      job,
		Resume:   r,
	}

	// Index the fact base for lookup.
	roleByID := map[int64]Role{}
	bulletByID := map[int64]Bullet{}
	for _, role := range fb.Roles {
		bare := role
		bare.Bullets = nil
		roleByID[role.ID] = bare
		for _, b := range role.Bullets {
			bulletByID[b.ID] = b
		}
	}
	projByID := map[int64]Project{}
	pbByID := map[int64]ProjectBullet{}
	for _, p := range fb.Projects {
		bare := p
		bare.Bullets = nil
		projByID[p.ID] = bare
		for _, b := range p.Bullets {
			pbByID[b.ID] = b
		}
	}
	patentByID := map[int64]Patent{}
	for _, p := range fb.Patents {
		patentByID[p.ID] = p
	}
	skillByID := map[int64]Skill{}
	for _, sk := range fb.Skills {
		skillByID[sk.ID] = sk
	}

	// Pass 1: containers, in item order.
	type placed struct {
		pos int
		idx int
	}
	rolePos := map[int64]placed{}
	projPos := map[int64]placed{}
	var skillOrder []int64

	for _, it := range items {
		switch it.Kind {
		case KindRole:
			role, ok := roleByID[it.RefID]
			if !ok {
				return nil, fmt.Errorf("resume references missing role %d", it.RefID)
			}
			if it.OverrideText != "" {
				role.Summary = it.OverrideText
			}
			rolePos[it.RefID] = placed{pos: it.Position, idx: len(doc.Roles)}
			doc.Roles = append(doc.Roles, role)
		case KindProject:
			p, ok := projByID[it.RefID]
			if !ok {
				return nil, fmt.Errorf("resume references missing project %d", it.RefID)
			}
			if it.OverrideText != "" {
				p.Summary = it.OverrideText
			}
			projPos[it.RefID] = placed{pos: it.Position, idx: len(doc.Projects)}
			doc.Projects = append(doc.Projects, p)
		case KindPatent:
			p, ok := patentByID[it.RefID]
			if !ok {
				return nil, fmt.Errorf("resume references missing patent %d", it.RefID)
			}
			doc.Patents = append(doc.Patents, p)
		case KindSkill:
			if _, ok := skillByID[it.RefID]; !ok {
				return nil, fmt.Errorf("resume references missing skill %d", it.RefID)
			}
			skillOrder = append(skillOrder, it.RefID)
		}
	}

	// Pass 2: children attach to their container. A bullet whose role was not
	// selected is skipped rather than erroring — that is a normal consequence
	// of dropping a role during tailoring.
	for _, it := range items {
		switch it.Kind {
		case KindBullet:
			b, ok := bulletByID[it.RefID]
			if !ok {
				return nil, fmt.Errorf("resume references missing bullet %d", it.RefID)
			}
			if it.OverrideText != "" {
				b.Text = it.OverrideText
			}
			p, ok := rolePos[b.RoleID]
			if !ok {
				continue
			}
			b.Position = it.Position
			doc.Roles[p.idx].Bullets = append(doc.Roles[p.idx].Bullets, b)
		case KindProjectBullet:
			b, ok := pbByID[it.RefID]
			if !ok {
				return nil, fmt.Errorf("resume references missing project bullet %d", it.RefID)
			}
			if it.OverrideText != "" {
				b.Text = it.OverrideText
			}
			p, ok := projPos[b.ProjectID]
			if !ok {
				continue
			}
			b.Position = it.Position
			doc.Projects[p.idx].Bullets = append(doc.Projects[p.idx].Bullets, b)
		}
	}

	// Sort containers by their item position, children by theirs.
	sort.SliceStable(doc.Roles, func(i, j int) bool {
		return rolePos[doc.Roles[i].ID].pos < rolePos[doc.Roles[j].ID].pos
	})
	sort.SliceStable(doc.Projects, func(i, j int) bool {
		return projPos[doc.Projects[i].ID].pos < projPos[doc.Projects[j].ID].pos
	})
	for i := range doc.Roles {
		sort.SliceStable(doc.Roles[i].Bullets, func(a, b int) bool {
			return doc.Roles[i].Bullets[a].Position < doc.Roles[i].Bullets[b].Position
		})
	}
	for i := range doc.Projects {
		sort.SliceStable(doc.Projects[i].Bullets, func(a, b int) bool {
			return doc.Projects[i].Bullets[a].Position < doc.Projects[i].Bullets[b].Position
		})
	}

	// Skills group by category, preserving first-seen category order.
	var catOrder []string
	byCat := map[string][]string{}
	for _, id := range skillOrder {
		sk := skillByID[id]
		if _, seen := byCat[sk.Category]; !seen {
			catOrder = append(catOrder, sk.Category)
		}
		byCat[sk.Category] = append(byCat[sk.Category], sk.Name)
	}
	for _, cat := range catOrder {
		doc.SkillGroups = append(doc.SkillGroups, SkillGroup{Category: cat, Names: byCat[cat]})
	}

	return doc, nil
}

// AssembleFull builds a Document from the entire fact base, used for the
// untailored master resume.
func (s *Store) AssembleFull() (*Document, error) {
	fb, err := s.FactBase()
	if err != nil {
		return nil, err
	}
	doc := &Document{
		Profile:  fb.Profile,
		Contacts: fb.Contacts,
		Summary:  fb.Profile.Summary,
		Roles:    fb.Roles,
		Projects: fb.Projects,
		Patents:  fb.Patents,
	}
	var catOrder []string
	byCat := map[string][]string{}
	for _, sk := range fb.Skills {
		if _, seen := byCat[sk.Category]; !seen {
			catOrder = append(catOrder, sk.Category)
		}
		byCat[sk.Category] = append(byCat[sk.Category], sk.Name)
	}
	for _, cat := range catOrder {
		doc.SkillGroups = append(doc.SkillGroups, SkillGroup{Category: cat, Names: byCat[cat]})
	}
	return doc, nil
}
