package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/beetlebugorg/resumed/internal/store"
)

// Editing a fact in the browser goes through the same versioning path as
// update_facts does over MCP: the old wording stays on the row every existing
// resume cites, and the correction becomes a new version that new tailoring
// sees. So this is a correction tool, not a rewrite tool — which is the point,
// because a resume you already sent must keep rendering what you sent.

// factField is one editable column of a fact.
type factField struct {
	Name  string // the store column, e.g. "start_date"
	Label string
	Value string
	Multi bool // render as a textarea rather than an input
}

// factEditView is a fact opened for editing.
type factEditView struct {
	Kind    string
	ID      int64
	Fields  []factField
	Context string // the role or project this fact sits under
	Group   bool   // a container: its heading and its children redraw together
}

// factEditFor builds the form for one fact. The field list per kind mirrors
// store.factColumns — anything outside it is structural and not the user's to
// edit.
func factEditFor(fb *store.FactBase, kind string, id int64) (factEditView, error) {
	v := factEditView{Kind: kind, ID: id}
	switch kind {
	case store.KindRole:
		for _, r := range fb.Roles {
			if r.ID != id {
				continue
			}
			v.Group = true
			v.Context = "Role"
			v.Fields = []factField{
				{Name: "company", Label: "Company", Value: r.Company},
				{Name: "title", Label: "Title", Value: r.Title},
				{Name: "location", Label: "Location", Value: r.Location},
				{Name: "start_date", Label: "Start", Value: r.StartDate},
				{Name: "end_date", Label: "End", Value: r.EndDate},
				{Name: "summary", Label: "Summary", Value: r.Summary, Multi: true},
			}
			return v, nil
		}
	case store.KindBullet:
		for _, r := range fb.Roles {
			for _, b := range r.Bullets {
				if b.ID != id {
					continue
				}
				v.Context = r.Company + " — " + r.Title
				v.Fields = []factField{
					{Name: "text", Label: "Fact", Value: b.Text, Multi: true},
					{Name: "tags", Label: "Tags", Value: b.Tags},
				}
				return v, nil
			}
		}
	case store.KindProject:
		for _, p := range fb.Projects {
			if p.ID != id {
				continue
			}
			v.Group = true
			v.Context = "Project"
			v.Fields = []factField{
				{Name: "name", Label: "Name", Value: p.Name},
				{Name: "url", Label: "URL", Value: p.URL},
				{Name: "date", Label: "Date", Value: p.Date},
				{Name: "summary", Label: "Summary", Value: p.Summary, Multi: true},
				{Name: "tags", Label: "Tags", Value: p.Tags},
			}
			return v, nil
		}
	case store.KindProjectBullet:
		for _, p := range fb.Projects {
			for _, b := range p.Bullets {
				if b.ID != id {
					continue
				}
				v.Context = p.Name
				v.Fields = []factField{
					{Name: "text", Label: "Fact", Value: b.Text, Multi: true},
					{Name: "tags", Label: "Tags", Value: b.Tags},
				}
				return v, nil
			}
		}
	case store.KindPatent:
		for _, p := range fb.Patents {
			if p.ID != id {
				continue
			}
			v.Context = "Patent"
			v.Fields = []factField{
				{Name: "patent_id", Label: "Number", Value: p.PatentID},
				{Name: "summary", Label: "Title", Value: p.Summary, Multi: true},
				{Name: "url", Label: "URL", Value: p.URL},
				{Name: "date", Label: "Year", Value: p.Date},
			}
			return v, nil
		}
	case store.KindSkill:
		for _, sk := range fb.Skills {
			if sk.ID != id {
				continue
			}
			v.Context = sk.Category
			v.Fields = []factField{
				{Name: "name", Label: "Skill", Value: sk.Name},
				{Name: "category", Label: "Category", Value: sk.Category},
			}
			return v, nil
		}
	default:
		return v, fmt.Errorf("unknown fact kind %q", kind)
	}
	return v, fmt.Errorf("%s %d not found", kind, id)
}

// editFact swaps a fact into a prefilled form.
func (s *server) editFact(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	fb, err := appOf(r).Store.FactBaseAll()
	if fail(w, err) {
		return
	}
	v, err := factEditFor(fb, kind, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.partial(w, editPartial(v), v)
}

// editPartial picks the wrapper the form is swapped into: a list item for a
// bullet or skill, a block for a role or project.
func editPartial(v factEditView) string {
	if v.Group {
		return "fact-group-edit"
	}
	return "fact-edit"
}

// showFactRow renders one fact back in its reading state — used to cancel out
// of editing a leaf row.
func (s *server) showFactRow(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	row, err := s.factRowByID(r, kind, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.partial(w, "fact-row", row)
}

// saveFact records the correction as a new version and swaps the row back.
//
// The new version has a new row id, so the swapped-in row carries it: the next
// edit from this page has to target the version it is showing, not the one it
// replaced.
func (s *server) saveFact(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	kind := r.PathValue("kind")
	id, err := pathID(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	fb, err := appOf(r).Store.FactBaseAll()
	if fail(w, err) {
		return
	}
	before, err := factEditFor(fb, kind, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Only send what actually changed. An update with no changed field is a
	// no-op, and versioning a fact to store identical text is just noise.
	fields := map[string]string{}
	for _, f := range before.Fields {
		got := strings.TrimSpace(r.FormValue(f.Name))
		if got != f.Value {
			fields[f.Name] = got
		}
	}

	newID := id
	if len(fields) > 0 {
		newID, err = appOf(r).Store.UpdateFact(kind, id, fields)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	if !isHTMX(r) {
		http.Redirect(w, r, "/facts", http.StatusSeeOther)
		return
	}
	// A container's heading and its bullets are drawn as one block, so let the
	// page redraw rather than trying to patch a group in place.
	if before.Group {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	row, err := s.factRowByID(r, kind, newID)
	if fail(w, err) {
		return
	}
	s.partial(w, "fact-row", row)
}
