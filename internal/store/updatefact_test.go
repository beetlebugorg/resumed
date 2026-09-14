package store

import (
	"strconv"
	"strings"
	"testing"
)

// position and parent_id are integers, and fields arrive as strings. SQLite
// stores what it is handed, so text in an integer column sorts as 0 with
// nothing to show why.
func TestUpdateFactRejectsNonNumericIntegerFields(t *testing.T) {
	s := testStore(t)
	_, id, _ := seed(t, s)

	for _, f := range []string{"position", "parent_id"} {
		if _, err := s.UpdateFact("bullet", id, map[string]string{f: "soon"}); err == nil {
			t.Errorf("%s accepted %q", f, "soon")
		} else if !strings.Contains(err.Error(), "whole number") {
			t.Errorf("%s: error %q does not explain the problem", f, err)
		}
	}
}

func TestUpdateFactSetsPosition(t *testing.T) {
	s := testStore(t)
	_, id, _ := seed(t, s)

	newID, err := s.UpdateFact("bullet", id, map[string]string{"position": "7"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	roles, err := s.ListRoles(scopeCurrent)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var got *Bullet
	for i := range roles {
		for j := range roles[i].Bullets {
			if roles[i].Bullets[j].ID == newID {
				got = &roles[i].Bullets[j]
			}
		}
	}
	if got == nil {
		t.Fatalf("bullet %d missing after update", newID)
	}
	if got.Position != 7 {
		t.Errorf("Position = %d, want 7", got.Position)
	}
}

// Zero detaches a bullet from its parent, so it has to be accepted rather than
// read as an empty field.
func TestUpdateFactAcceptsZeroParent(t *testing.T) {
	s := testStore(t)
	_, id, _ := seed(t, s)

	if _, err := s.UpdateFact("bullet", id, map[string]string{"parent_id": "0"}); err != nil {
		t.Errorf("parent_id 0 rejected: %v", err)
	}
}

// Roles sort by date, so exposing position there would suggest an ordering
// that does not apply.
func TestUpdateFactStillRejectsPositionOnARole(t *testing.T) {
	s := testStore(t)
	roleID, _, _ := seed(t, s)
	if _, err := s.UpdateFact("role", roleID, map[string]string{"position": "3"}); err == nil {
		t.Error("role accepted a position")
	}
}

// Detaching has to survive a round trip, not just avoid an error.
func TestUpdateFactZeroParentClearsTheLink(t *testing.T) {
	s := testStore(t)
	roleID, keepID, dropID := seed(t, s)

	attached, err := s.UpdateFact("bullet", dropID, map[string]string{"parent_id": itoa(keepID)})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if got := bulletByID(t, s, roleID, attached); got.ParentID != keepID {
		t.Fatalf("ParentID = %d, want %d", got.ParentID, keepID)
	}

	detached, err := s.UpdateFact("bullet", attached, map[string]string{"parent_id": "0"})
	if err != nil {
		t.Fatalf("detach: %v", err)
	}
	if got := bulletByID(t, s, roleID, detached); got.ParentID != 0 {
		t.Errorf("ParentID = %d, want 0 after detaching", got.ParentID)
	}
}

func bulletByID(t *testing.T, s *Store, roleID, bulletID int64) Bullet {
	t.Helper()
	roles, err := s.ListRoles(scopeCurrent)
	if err != nil {
		t.Fatalf("list roles: %v", err)
	}
	for _, r := range roles {
		for _, b := range r.Bullets {
			if b.ID == bulletID {
				return b
			}
		}
	}
	t.Fatalf("bullet %d not found on role %d", bulletID, roleID)
	return Bullet{}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
