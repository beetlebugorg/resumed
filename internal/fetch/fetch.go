// Package fetch retrieves a job posting from a URL and reduces it to plain
// text.
//
// Extraction runs in layers, best source first, because no single one covers
// the real spread of job boards:
//
//  1. schema.org JobPosting as JSON-LD — clean title/company/location/
//     description with no guessing. Many ATS pages have it; notably,
//     Greenhouse's job-boards.greenhouse.io does not.
//  2. Open Graph / Twitter meta tags — where Greenhouse keeps the real title.
//  3. The URL itself — ATS hosts put the employer slug at a fixed path
//     position, which beats scraping for it.
//  4. The <title>, split on the usual "role at company" patterns.
//
// The description always falls back to HTML stripped to readable text. A
// JavaScript-rendered page yields nothing useful, and the caller is told to
// paste the text in by hand.
package fetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

type Result struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Company     string `json:"company,omitempty"`
	Location    string `json:"location,omitempty"`
	Description string `json:"description"`
	Source      string `json:"source"` // json-ld | html
}

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/122.0 Safari/537.36"

// Fetch downloads and extracts a job posting.
func Fetch(ctx context.Context, url string) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("bad url: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}

	res := &Result{URL: url}
	if jp := findJobPosting(doc); jp != nil {
		res.Source = "json-ld"
		res.Title = jp.Title
		res.Company = jp.orgName()
		res.Location = jp.locality()
		res.Description = htmlToText(jp.Description)
	}
	// Fall back (or fill gaps) from the page itself.
	if strings.TrimSpace(res.Description) == "" {
		res.Source = "html"
		res.Description = extractText(doc)
	}

	// Metadata, best source first. Greenhouse — the most common board — ships
	// no JSON-LD at all but does set Open Graph tags, and its URL carries the
	// company slug, so these fallbacks matter in practice.
	meta := metaTags(doc)
	titleFromTag, companyFromTitle := splitPageTitle(pageTitle(doc))
	if res.Title == "" {
		res.Title = firstNonEmpty(meta["og:title"], meta["twitter:title"], titleFromTag)
	}
	if res.Company == "" {
		res.Company = firstNonEmpty(meta["og:site_name"], companyFromURL(url), companyFromTitle)
	}
	if res.Location == "" {
		res.Location = locationFromDOM(doc)
	}
	if strings.TrimSpace(res.Description) == "" {
		return res, fmt.Errorf("no readable job description found at %s "+
			"(the page is probably JavaScript-rendered — paste the text in manually)", url)
	}
	return res, nil
}

// ---------------------------------------------------------------- JSON-LD

type jobPosting struct {
	Type        any             `json:"@type"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Org         json.RawMessage `json:"hiringOrganization"`
	Loc         json.RawMessage `json:"jobLocation"`
}

func (j *jobPosting) orgName() string {
	if len(j.Org) == 0 {
		return ""
	}
	var asStruct struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(j.Org, &asStruct); err == nil && asStruct.Name != "" {
		return asStruct.Name
	}
	var asString string
	if err := json.Unmarshal(j.Org, &asString); err == nil {
		return asString
	}
	return ""
}

func (j *jobPosting) locality() string {
	if len(j.Loc) == 0 {
		return ""
	}
	// jobLocation may be an object or an array of them.
	var one struct {
		Address struct {
			Locality string `json:"addressLocality"`
			Region   string `json:"addressRegion"`
			Country  any    `json:"addressCountry"`
		} `json:"address"`
	}
	raw := j.Loc
	if len(raw) > 0 && raw[0] == '[' {
		var many []json.RawMessage
		if err := json.Unmarshal(raw, &many); err != nil || len(many) == 0 {
			return ""
		}
		raw = many[0]
	}
	if err := json.Unmarshal(raw, &one); err != nil {
		return ""
	}
	parts := []string{}
	if one.Address.Locality != "" {
		parts = append(parts, one.Address.Locality)
	}
	if one.Address.Region != "" {
		parts = append(parts, one.Address.Region)
	}
	return strings.Join(parts, ", ")
}

func hasJobPostingType(t any) bool {
	switch v := t.(type) {
	case string:
		return v == "JobPosting"
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == "JobPosting" {
				return true
			}
		}
	}
	return false
}

// findJobPosting walks every <script type="application/ld+json"> block looking
// for a JobPosting, including ones nested in @graph arrays.
func findJobPosting(n *html.Node) *jobPosting {
	var found *jobPosting
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "script" && attr(n, "type") == "application/ld+json" {
			if jp := scanLD([]byte(textOf(n))); jp != nil {
				found = jp
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return found
}

func scanLD(raw []byte) *jobPosting {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return nil
	}
	// A block may be a single object, an array, or an object with @graph.
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil
	}
	var candidates []json.RawMessage
	switch raw[0] {
	case '[':
		if err := json.Unmarshal(raw, &candidates); err != nil {
			return nil
		}
	default:
		var obj struct {
			Graph []json.RawMessage `json:"@graph"`
		}
		if err := json.Unmarshal(raw, &obj); err == nil && len(obj.Graph) > 0 {
			candidates = obj.Graph
		} else {
			candidates = []json.RawMessage{raw}
		}
	}
	for _, c := range candidates {
		var jp jobPosting
		if err := json.Unmarshal(c, &jp); err != nil {
			continue
		}
		if hasJobPostingType(jp.Type) && strings.TrimSpace(jp.Description) != "" {
			return &jp
		}
	}
	return nil
}

// ------------------------------------------------------------ text extraction

var skipTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "svg": true,
	"nav": true, "header": true, "footer": true, "form": true,
	"iframe": true, "template": true,
}

// blockTags force a line break so bullet lists and paragraphs survive.
var blockTags = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"section": true, "article": true, "ul": true, "ol": true, "table": true,
}

func extractText(n *html.Node) string {
	// Prefer <main> or <article> when present — they usually bound the posting.
	if root := findFirst(n, "main", "article"); root != nil {
		if t := collapse(textFrom(root)); len(t) > 200 {
			return t
		}
	}
	if body := findFirst(n, "body"); body != nil {
		return collapse(textFrom(body))
	}
	return collapse(textFrom(n))
}

func textFrom(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && skipTags[n.Data] {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && n.Data == "li" {
			b.WriteString("\n- ")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && blockTags[n.Data] {
			b.WriteString("\n")
		}
	}
	walk(n)
	return b.String()
}

// htmlToText renders an HTML fragment (as found in JSON-LD descriptions) as text.
func htmlToText(frag string) string {
	if !strings.Contains(frag, "<") {
		return collapse(frag)
	}
	doc, err := html.Parse(strings.NewReader(frag))
	if err != nil {
		return collapse(frag)
	}
	return collapse(textFrom(doc))
}

// collapse normalises whitespace: trims each line, drops runs of blank lines.
func collapse(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, " ", " ")
	lines := strings.Split(s, "\n")
	var out []string
	blank := 0
	for _, ln := range lines {
		ln = strings.TrimSpace(strings.Join(strings.Fields(ln), " "))
		if ln == "" || ln == "-" {
			blank++
			if blank > 1 || len(out) == 0 {
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, ln)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func findFirst(n *html.Node, tags ...string) *html.Node {
	want := map[string]bool{}
	for _, t := range tags {
		want[t] = true
	}
	var found *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode && want[n.Data] {
			found = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return found
}

func pageTitle(n *html.Node) string {
	if t := findFirst(n, "title"); t != nil {
		return collapse(textOf(t))
	}
	return ""
}

// ------------------------------------------------------- metadata fallbacks

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// metaTags collects <meta property|name="..." content="..."> into a map.
func metaTags(root *html.Node) map[string]string {
	out := map[string]string{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "meta" {
			key := firstNonEmpty(attr(n, "property"), attr(n, "name"))
			if content := attr(n, "content"); key != "" && content != "" {
				if _, seen := out[strings.ToLower(key)]; !seen {
					out[strings.ToLower(key)] = collapse(content)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

// atsPathCompany maps an applicant-tracking host to the path segment holding
// the employer slug: job-boards.greenhouse.io/northwind/jobs/123 -> "northwind".
var atsPathCompany = map[string]int{
	"job-boards.greenhouse.io":    0,
	"boards.greenhouse.io":        0,
	"jobs.lever.co":               0,
	"jobs.ashbyhq.com":            0,
	"apply.workable.com":          0,
	"jobs.smartrecruiters.com":    0,
	"careers.smartrecruiters.com": 0,
}

// companyFromURL recovers the employer from an ATS URL. These hosts put the
// company slug in a fixed position, which is more reliable than scraping.
func companyFromURL(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil {
		return ""
	}
	idx, ok := atsPathCompany[strings.ToLower(u.Host)]
	if !ok {
		return ""
	}
	parts := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	if idx >= len(parts) {
		return ""
	}
	return titleCaseSlug(parts[idx])
}

// titleCaseSlug turns "acme-corp" into "Acme Corp".
func titleCaseSlug(slug string) string {
	words := strings.FieldsFunc(slug, func(r rune) bool { return r == '-' || r == '_' })
	for i, w := range words {
		r := []rune(w)
		if len(r) == 0 {
			continue
		}
		words[i] = strings.ToUpper(string(r[0])) + string(r[1:])
	}
	return strings.Join(words, " ")
}

// titleSeparators are the patterns boards use to glue a role to an employer in
// the <title>, longest first so " - " does not win over " – ".
var titleSeparators = []string{" at ", " | ", " – ", " — ", " - ", " @ "}

// splitPageTitle pulls a role and employer out of a page title. Greenhouse
// renders "Job Application for <role> at <company>"; most others use a
// separator. Returns empty strings when nothing sensible can be recovered.
func splitPageTitle(t string) (title, company string) {
	t = strings.TrimSpace(t)
	if t == "" || t == "page_title" {
		return "", ""
	}
	for _, prefix := range []string{"Job Application for ", "Apply for ", "Job Application: "} {
		if rest, ok := strings.CutPrefix(t, prefix); ok {
			t = rest
			break
		}
	}
	for _, sep := range titleSeparators {
		if i := strings.LastIndex(t, sep); i > 0 {
			left := strings.TrimSpace(t[:i])
			right := strings.TrimSpace(t[i+len(sep):])
			if left != "" && right != "" {
				return left, right
			}
		}
	}
	return t, ""
}

// locationClasses are the class names boards use for the location line.
var locationClasses = []string{"job__location", "location", "posting-categories", "job-location"}

func locationFromDOM(root *html.Node) string {
	var found string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != "" {
			return
		}
		if n.Type == html.ElementNode {
			class := strings.ToLower(attr(n, "class"))
			for _, want := range locationClasses {
				if strings.Contains(class, want) {
					if t := collapse(textOf(n)); t != "" && len(t) < 120 {
						found = t
						return
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return found
}

func textOf(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		} else {
			b.WriteString(textOf(c))
		}
	}
	return b.String()
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
