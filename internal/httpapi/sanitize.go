package httpapi

import "github.com/microcosm-cc/bluemonday"

// emailHTMLPolicy sanitizes an email's HTML body for safe rendering
// (FR-VW-01: "удаление script, onclick, внешних ресурсов, data URI").
// bluemonday's stock UGCPolicy() isn't used directly: its AllowImages()
// call permits both http(s) images and base64 data: images — exactly the
// two things FR-VW-01 says must be stripped — so this policy is built
// without ever allowing an <img> element at all, rather than trying to
// carve out an exception afterward (bluemonday has no "disallow" once an
// element's been allowed).
//
// Anything not explicitly allowed below (script, style, on* event
// handler attributes, iframe, object, forms, ...) is stripped by
// bluemonday's own allowlist model — there's no separate denylist to
// maintain.
var emailHTMLPolicy = newEmailHTMLPolicy()

func newEmailHTMLPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()

	p.AllowStandardAttributes()
	p.AllowStandardURLs() // mailto/http/https only, parseable — never data:

	p.AllowElements(
		"p", "br", "div", "span", "hr",
		"h1", "h2", "h3", "h4", "h5", "h6",
		"b", "strong", "i", "em", "u", "s", "strike", "small", "mark", "sub", "sup",
		"blockquote", "pre", "code", "q", "cite",
	)
	p.AllowLists()
	p.AllowTables()
	p.AllowAttrs("href").OnElements("a")
	p.AllowAttrs("cite").OnElements("blockquote", "q")

	p.RequireNoFollowOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)

	return p
}

// sanitizeEmailHTML returns html with everything FR-VW-01 disallows
// stripped out — safe to render directly in the Web UI.
func sanitizeEmailHTML(html string) string {
	return emailHTMLPolicy.Sanitize(html)
}
