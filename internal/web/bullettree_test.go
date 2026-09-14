package web

import (
	"strings"
	"testing"

	"github.com/beetlebugorg/resumed/internal/store"
)

// A template cannot recurse while carrying the highlight terms, so bulletTree
// does it in Go. The resume preview and the PDF have to show the same shape.
func TestBulletTreeNestsToAnyDepth(t *testing.T) {
	got := string(bulletTree([]store.Bullet{
		{Text: "lead-in", Children: []store.Bullet{
			{Text: "child", Children: []store.Bullet{
				{Text: "grandchild"},
			}},
		}},
		{Text: "plain"},
	}, nil))

	want := "<ul><li>lead-in<ul><li>child<ul><li>grandchild</li></ul></li></ul></li><li>plain</li></ul>"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestBulletTreeIsEmptyForNoBullets(t *testing.T) {
	if got := bulletTree(nil, nil); got != "" {
		t.Errorf("got %q, want an empty string rather than a bare list", got)
	}
}

// Highlight terms have to reach every level, or a term is bold on the parent
// and plain on the child.
func TestBulletTreeHighlightsChildren(t *testing.T) {
	got := string(bulletTree([]store.Bullet{
		{Text: "built the platform", Children: []store.Bullet{{Text: "platform hardening"}}},
	}, []string{"platform"}))

	if strings.Count(got, "<strong>platform</strong>") != 2 {
		t.Errorf("term not emphasised at both levels: %s", got)
	}
}

// Bullet text is user data and must not reach the page as markup.
func TestBulletTreeEscapesText(t *testing.T) {
	got := string(bulletTree([]store.Bullet{
		{Text: "a <script>alert(1)</script> b", Children: []store.Bullet{
			{Text: "c & d"},
		}},
	}, nil))

	if strings.Contains(got, "<script>") {
		t.Errorf("script tag survived into the page: %s", got)
	}
	if !strings.Contains(got, "c &amp; d") {
		t.Errorf("ampersand not escaped in a child: %s", got)
	}
}
