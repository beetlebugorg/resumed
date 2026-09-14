package web

import "testing"

func TestIsCaveatReadsTheTagList(t *testing.T) {
	for _, tc := range []struct {
		tags string
		want bool
	}{
		{"go,production,caveat,gap", true},
		{"ai,ml,boundary,caveat,scope,not-for-resume", true},
		{" caveat ", true}, // whitespace around a tag
		{"open-source,systems,zig,go,product", false},
		{"", false},
		{"scope", false},   // scope alone is not a caveat
		{"caveats", false}, // the tag is "caveat"
	} {
		if got := isCaveat(tc.tags); got != tc.want {
			t.Errorf("isCaveat(%q) = %v, want %v", tc.tags, got, tc.want)
		}
	}
}

// Resume material excludes caveats and retired facts. Caveats includes retired
// caveats. All includes every row.
func TestKeepFactPerView(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		caveat, retired      bool
		resume, caveats, all bool
	}{
		{"plain fact", false, false, true, false, true},
		{"caveat", true, false, false, true, true},
		{"retired fact", false, true, false, false, true},
		{"retired caveat", true, true, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := keepFact(factsResume, tc.caveat, tc.retired); got != tc.resume {
				t.Errorf("resume view = %v, want %v", got, tc.resume)
			}
			if got := keepFact(factsCaveats, tc.caveat, tc.retired); got != tc.caveats {
				t.Errorf("caveats view = %v, want %v", got, tc.caveats)
			}
			if got := keepFact(factsAll, tc.caveat, tc.retired); got != tc.all {
				t.Errorf("all view = %v, want %v", got, tc.all)
			}
		})
	}
}

// Counts describe the whole fact base, so they are the same in every view.
func TestFactCountsPartitionTheBase(t *testing.T) {
	var c factCounts
	c.add(false, false) // resume material
	c.add(false, false)
	c.add(true, false) // caveat
	c.add(false, true) // retired, so in neither filtered view
	c.add(true, true)  // retired caveat, counted as a caveat

	if c.All != 5 {
		t.Errorf("All = %d, want 5", c.All)
	}
	if c.Resume != 2 {
		t.Errorf("Resume = %d, want 2", c.Resume)
	}
	if c.Caveats != 2 {
		t.Errorf("Caveats = %d, want 2", c.Caveats)
	}
}

func TestFactTabsMarkTheCurrentView(t *testing.T) {
	tabs := factTabs(factsCaveats, factCounts{All: 10, Resume: 7, Caveats: 3}).Tabs
	if len(tabs) != 3 {
		t.Fatalf("got %d tabs, want 3", len(tabs))
	}
	on := ""
	for _, tab := range tabs {
		if tab.On {
			on = tab.Key
		}
	}
	if on != factsCaveats {
		t.Errorf("marked %q current, want %q", on, factsCaveats)
	}
	if tabs[0].Href != "/facts?show=resume" {
		t.Errorf("Href = %q, want /facts?show=resume", tabs[0].Href)
	}
}
