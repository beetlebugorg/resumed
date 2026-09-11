package fetch

import (
	"context"
	"os"
	"strings"
	"testing"
)

// Live fetches hit the real internet and break when a board redesigns, so they
// are opt-in:
//
//	RESUMED_LIVE=1 go test ./internal/fetch -run Live -v
//
// Run this when a posting imports with missing company/title/location — it
// shows which extraction layer gave up.
func TestLiveBoards(t *testing.T) {
	if os.Getenv("RESUMED_LIVE") == "" {
		t.Skip("set RESUMED_LIVE=1 to fetch real job boards")
	}
	// The posting comes from the environment. A URL committed here would be
	// both a record of somewhere a candidate applied and a link that rots as
	// soon as the posting closes.
	url := os.Getenv("RESUMED_LIVE_URL")
	if url == "" {
		t.Skip("set RESUMED_LIVE_URL to the posting to fetch")
	}
	cases := []struct {
		name, url   string
		wantCompany string
	}{
		{"posting", url, os.Getenv("RESUMED_LIVE_COMPANY")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := Fetch(context.Background(), c.url)
			if err != nil {
				t.Fatalf("fetch: %v", err)
			}
			t.Logf("source=%s title=%q company=%q location=%q desc=%d bytes",
				res.Source, res.Title, res.Company, res.Location, len(res.Description))
			if res.Company != c.wantCompany {
				t.Errorf("company = %q, want %q", res.Company, c.wantCompany)
			}
			if strings.TrimSpace(res.Title) == "" {
				t.Error("title is empty")
			}
			if len(res.Description) < 500 {
				t.Errorf("description looks truncated: %d bytes", len(res.Description))
			}
		})
	}
}
