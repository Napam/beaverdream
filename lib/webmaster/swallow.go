package webmaster

import (
	"strings"

	"golang.org/x/net/html"
)

// postsMarker stands in for the post list while the page is tokenized. A
// custom element name cannot collide with model markup that means something.
const postsMarker = "<bd-posts-marker>"

// PostsSwallowedBy names what hides the post list in the page as the browser
// reads it: a comment, or an element whose content is raw text (script,
// style, textarea, title and the like), opened before {{posts}} and never
// closed. Empty means the posts render.
//
// Only accidents are worth naming. CSS can hide posts on purpose, and that is
// the model's call; this catches the edit that left a tag open.
func PostsSwallowedBy(site Site) string {
	at := strings.Index(site.HTML, PostsPlaceholder)
	if at < 0 {
		return ""
	}
	doc := site.HTML[:at] + postsMarker

	tokens := html.NewTokenizer(strings.NewReader(doc))
	open := ""
	for {
		switch tokens.Next() {
		case html.ErrorToken:
			// The marker never surfaced as a tag, a comment or text, so a tag
			// left without its > or an attribute without its closing quote
			// took it in.
			return "an unclosed tag or attribute quote"
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := tokens.TagName()
			if string(name) == "bd-posts-marker" {
				return ""
			}
			open = string(name)
		case html.CommentToken:
			if strings.Contains(string(tokens.Raw()), postsMarker) {
				return "an unclosed <!-- comment"
			}
		case html.TextToken:
			if strings.Contains(string(tokens.Raw()), postsMarker) {
				return "an unclosed <" + open + ">"
			}
		}
	}
}
