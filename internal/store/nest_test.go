package store

import "testing"

// factOf maps each row id to itself, for bullets that have never been
// corrected.
func selfFacts(ids ...int64) map[int64]int64 {
	m := map[int64]int64{}
	for _, id := range ids {
		m[id] = id
	}
	return m
}

func TestNestBulletsGroupsChildren(t *testing.T) {
	top := NestBullets([]Bullet{
		{ID: 1, Text: "lead-in"},
		{ID: 2, ParentID: 1, Text: "child a"},
		{ID: 3, ParentID: 1, Text: "child b"},
		{ID: 4, Text: "other"},
	}, selfFacts(1, 2, 3, 4))

	if len(top) != 2 {
		t.Fatalf("got %d top-level bullets, want 2", len(top))
	}
	if len(top[0].Children) != 2 {
		t.Fatalf("lead-in has %d children, want 2", len(top[0].Children))
	}
	if top[0].Children[0].ID != 2 || top[0].Children[1].ID != 3 {
		t.Errorf("children out of order: %d, %d", top[0].Children[0].ID, top[0].Children[1].ID)
	}
	if len(top[1].Children) != 0 {
		t.Errorf("unrelated bullet gained %d children", len(top[1].Children))
	}
}

// Correcting a parent writes a new row with a new id. The child still points at
// the row that existed when it was written, so grouping resolves through
// fact_id. This is the failure bf2b007 fixed for role bullets.
func TestNestBulletsFollowsACorrectedParent(t *testing.T) {
	// Bullet 1 was corrected and is now row 9. Both rows are fact 1, and
	// factIDOf maps superseded rows too.
	factOf := map[int64]int64{1: 1, 9: 1, 2: 2}
	top := NestBullets([]Bullet{
		{ID: 9, Text: "corrected lead-in"},
		{ID: 2, ParentID: 1, Text: "child written against the old row"},
	}, factOf)

	if len(top) != 1 {
		t.Fatalf("got %d top-level bullets, want 1: the child lost its parent", len(top))
	}
	if len(top[0].Children) != 1 || top[0].Children[0].ID != 2 {
		t.Errorf("child did not attach to the corrected parent")
	}
}

// Tailoring selects a child and drops its lead-in. Dropping the child too would
// lose material the resume asked for.
func TestNestBulletsPromotesAnOrphan(t *testing.T) {
	top := NestBullets([]Bullet{
		{ID: 2, ParentID: 1, Text: "child whose parent was not selected"},
	}, selfFacts(2))

	if len(top) != 1 {
		t.Fatalf("got %d top-level bullets, want 1", len(top))
	}
	if top[0].ID != 2 {
		t.Errorf("got bullet %d, want the orphan promoted", top[0].ID)
	}
}

// Nesting runs more than once over the same data on the facts page. Stale
// children from an earlier pass would double every child.
func TestNestBulletsIsIdempotent(t *testing.T) {
	flat := []Bullet{
		{ID: 1, Text: "lead-in"},
		{ID: 2, ParentID: 1, Text: "child"},
	}
	once := NestBullets(flat, selfFacts(1, 2))
	twice := NestBullets(once, selfFacts(1, 2))
	if len(twice) != 1 || len(twice[0].Children) != 1 {
		t.Fatalf("got %d top-level with %d children, want 1 and 1", len(twice), len(twice[0].Children))
	}
	if twice[0].Children[0].ID != 2 {
		t.Errorf("child is bullet %d, want 2", twice[0].Children[0].ID)
	}
}
