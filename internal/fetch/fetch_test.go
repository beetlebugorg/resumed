package fetch

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestCompanyFromURL(t *testing.T) {
	cases := map[string]string{
		"https://job-boards.greenhouse.io/northwind/jobs/40000001": "Northwind",
		"https://boards.greenhouse.io/acme-corp/jobs/42":           "Acme Corp",
		"https://jobs.lever.co/figma/abc-123":                      "Figma",
		"https://jobs.ashbyhq.com/openai/xyz":                      "Openai",
		"https://apply.workable.com/some_startup/j/ABC/":           "Some Startup",
		// Not an ATS host: guessing an employer from the path would be wrong.
		"https://www.contoso.com/careers/staff-backend-engineer": "",
		"https://example.com": "",
		"not a url at all":    "",
	}
	for in, want := range cases {
		if got := companyFromURL(in); got != want {
			t.Errorf("companyFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitPageTitle(t *testing.T) {
	cases := []struct {
		in, title, company string
	}{
		{"Job Application for Staff+ Software Engineer, Developer Productivity at Northwind",
			"Staff+ Software Engineer, Developer Productivity", "Northwind"},
		{"Senior Backend Engineer - Stripe", "Senior Backend Engineer", "Stripe"},
		{"Platform Engineer | Datadog", "Platform Engineer", "Datadog"},
		{"Apply for Site Reliability Engineer at Cloudflare", "Site Reliability Engineer", "Cloudflare"},
		// No separator: the whole thing is the title.
		{"Careers", "Careers", ""},
		// Greenhouse's placeholder title carries nothing.
		{"page_title", "", ""},
		{"", "", ""},
		// "at" inside the role must not split early — we take the LAST separator.
		{"Engineer at Scale at Netflix", "Engineer at Scale", "Netflix"},
	}
	for _, c := range cases {
		title, company := splitPageTitle(c.in)
		if title != c.title || company != c.company {
			t.Errorf("splitPageTitle(%q) = (%q, %q), want (%q, %q)",
				c.in, title, company, c.title, c.company)
		}
	}
}

// TestMetadataLayering builds the page shape Greenhouse actually serves — no
// JSON-LD, a placeholder <title>, real Open Graph tags — and checks each layer
// contributes what it should.
func TestMetadataLayering(t *testing.T) {
	page := `<!doctype html><html><head>
		<title>page_title</title>
		<meta property="og:title" content="Staff+ Software Engineer, Developer Productivity"/>
		<meta property="og:description" content="San Francisco, CA | New York City, NY"/>
	</head><body>
		<div class="job__location"><div>San Francisco, CA | New York City, NY</div></div>
		<main><p>We are looking for an engineer to own build and CI infrastructure.</p></main>
	</body></html>`

	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatal(err)
	}

	meta := metaTags(doc)
	if got := meta["og:title"]; got != "Staff+ Software Engineer, Developer Productivity" {
		t.Errorf("og:title = %q", got)
	}
	titleFromTag, companyFromTitle := splitPageTitle(pageTitle(doc))
	if titleFromTag != "" || companyFromTitle != "" {
		t.Errorf("placeholder title should yield nothing, got (%q, %q)", titleFromTag, companyFromTitle)
	}
	if got := firstNonEmpty(meta["og:title"], titleFromTag); got != "Staff+ Software Engineer, Developer Productivity" {
		t.Errorf("title resolution = %q", got)
	}
	if got := locationFromDOM(doc); got != "San Francisco, CA | New York City, NY" {
		t.Errorf("locationFromDOM = %q", got)
	}
	if got := extractText(doc); !strings.Contains(got, "build and CI infrastructure") {
		t.Errorf("extractText lost the body: %q", got)
	}
}

// TestJSONLDStillWins guards the ordering: a page with both JSON-LD and og:
// tags must prefer the structured data.
func TestJSONLDStillWins(t *testing.T) {
	page := `<!doctype html><html><head>
		<title>Wrong Title - Wrong Co</title>
		<meta property="og:title" content="Wrong OG Title"/>
		<script type="application/ld+json">
		{"@context":"https://schema.org","@type":"JobPosting",
		 "title":"Real Title","description":"<p>Real description body.</p>",
		 "hiringOrganization":{"@type":"Organization","name":"Real Company"},
		 "jobLocation":{"@type":"Place","address":{"addressLocality":"Austin","addressRegion":"TX"}}}
		</script>
	</head><body><main><p>ignored</p></main></body></html>`

	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatal(err)
	}
	jp := findJobPosting(doc)
	if jp == nil {
		t.Fatal("JobPosting not found")
	}
	if jp.Title != "Real Title" {
		t.Errorf("title = %q", jp.Title)
	}
	if got := jp.orgName(); got != "Real Company" {
		t.Errorf("company = %q", got)
	}
	if got := jp.locality(); got != "Austin, TX" {
		t.Errorf("location = %q", got)
	}
	if got := htmlToText(jp.Description); got != "Real description body." {
		t.Errorf("description = %q", got)
	}
}

// TestJSONLDInGraph covers the @graph wrapper some sites use.
func TestJSONLDInGraph(t *testing.T) {
	raw := []byte(`{"@context":"https://schema.org","@graph":[
		{"@type":"WebSite","name":"Careers"},
		{"@type":["JobPosting"],"title":"Graph Role","description":"Body text here.",
		 "hiringOrganization":"Plain String Co"}]}`)
	jp := scanLD(raw)
	if jp == nil {
		t.Fatal("JobPosting inside @graph not found")
	}
	if jp.Title != "Graph Role" {
		t.Errorf("title = %q", jp.Title)
	}
	if got := jp.orgName(); got != "Plain String Co" {
		t.Errorf("string-valued hiringOrganization = %q", got)
	}
}
