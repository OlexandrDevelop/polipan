// SPDX-License-Identifier: Apache-2.0

package main

import (
	"html"
	"html/template"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	paraSplit      = regexp.MustCompile(`\n{2,}`)
	urlPattern     = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `]+`)
	usernamePatten = regexp.MustCompile(`^[A-Za-z0-9_-]{3,24}$`)
	reserved       = map[string]bool{"admin": true, "administrator": true, "root": true, "polipan": true, "support": true,
		"moderator": true, "mod": true, "system": true, "staff": true, "security": true, "help": true}
)

// renderPost turns plain text into safe HTML. Only text we escape and tags we write ourselves are in the result.
// A blank line starts a paragraph. A line break stays a line break. ``` starts and ends a code block.
// A web address becomes a link.
func renderPost(text string) template.HTML {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	var out strings.Builder
	parts := strings.Split(text, "```")
	for i, part := range parts {
		if i%2 == 1 && i < len(parts)-1 {
			out.WriteString("<pre><code>" + html.EscapeString(strings.Trim(part, "\n")) + "</code></pre>")
			continue
		}
		if i%2 == 1 {
			// An opening ``` without a closing one is plain text.
			part = "```" + part
		}
		for _, para := range paraSplit.Split(part, -1) {
			para = strings.Trim(para, "\n")
			if strings.TrimSpace(para) == "" {
				continue
			}
			out.WriteString("<p>" + strings.ReplaceAll(linkify(para), "\n", "<br>") + "</p>")
		}
	}
	return template.HTML(out.String()) // #nosec G203 -- built from escaped text only
}

// linkify escapes the text and links the web addresses in it.
func linkify(s string) string {
	var out strings.Builder
	last := 0
	for _, loc := range urlPattern.FindAllStringIndex(s, -1) {
		out.WriteString(html.EscapeString(s[last:loc[0]]))
		raw := s[loc[0]:loc[1]]
		trimmed := strings.TrimRight(raw, ".,;:!?)]}")
		rest := raw[len(trimmed):]
		if len(trimmed) > len("https://") && utf8.ValidString(trimmed) {
			e := html.EscapeString(trimmed)
			out.WriteString(`<a href="` + e + `" rel="nofollow noopener ugc">` + e + `</a>`)
		} else {
			out.WriteString(html.EscapeString(trimmed))
		}
		out.WriteString(html.EscapeString(rest))
		last = loc[1]
	}
	out.WriteString(html.EscapeString(s[last:]))
	return out.String()
}

func validUsername(name string) string {
	switch {
	case !usernamePatten.MatchString(name):
		return "The name needs 3 to 24 letters, numbers, - or _."
	case reserved[strings.ToLower(name)]:
		return "This name is reserved."
	}
	return ""
}

func validPassword(pw string) string {
	n := utf8.RuneCountInString(pw)
	switch {
	case n < 10:
		return "The password needs at least 10 characters."
	case n > 200:
		return "The password is too long (200 characters at most)."
	}
	return ""
}

func validTitle(title string) string {
	n := utf8.RuneCountInString(strings.TrimSpace(title))
	if n < 3 || n > 120 {
		return "The title needs 3 to 120 characters."
	}
	return ""
}

func validBody(body string) string {
	n := utf8.RuneCountInString(strings.TrimSpace(body))
	if n < 1 || n > 10000 {
		return "The text needs 1 to 10000 characters."
	}
	return ""
}
