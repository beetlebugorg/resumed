package render

import (
	"html/template"
	"strings"
)

// HighlightHTML escapes s for HTML and wraps every highlight term in <strong>,
// using the same matching rules as the PDF. Sharing highlightSpans is the point:
// the web view and the generated document must agree on what is emphasised, or
// reviewing a resume on screen tells you nothing about the file you send.
func HighlightHTML(s string, terms []string) template.HTML {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	spans := highlightSpans(s, terms)
	if len(spans) == 0 {
		return template.HTML(template.HTMLEscapeString(s))
	}
	var b strings.Builder
	prev := 0
	for _, sp := range spans {
		b.WriteString(template.HTMLEscapeString(s[prev:sp.lo]))
		b.WriteString("<strong>")
		b.WriteString(template.HTMLEscapeString(s[sp.lo:sp.hi]))
		b.WriteString("</strong>")
		prev = sp.hi
	}
	b.WriteString(template.HTMLEscapeString(s[prev:]))
	return template.HTML(b.String())
}
