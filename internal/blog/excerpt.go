package blog

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// excerptRunes is how long a worked-out excerpt may be: a card's three lines
// and a meta description's length (BR-132).
const excerptRunes = 160

var (
	mdImage    = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	mdLink     = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	htmlTag    = regexp.MustCompile(`<[^>]+>`)
	lineMarker = regexp.MustCompile(`(?m)^\s*(?:>+\s?|[-*+]\s+|\d+[.)]\s+)+`)
	// ponytail: emphasis is stripped by its markers at word edges, not parsed;
	// a stray * inside a word survives. A real Markdown parser if that bites.
	emphOpen  = regexp.MustCompile(`(^|\s)[*_]+(\S)`)
	emphClose = regexp.MustCompile(`(\S)[*_]+(\s|$|[.,!?;:)])`)
	spaces    = regexp.MustCompile(`\s+`)
)

// Excerpt is the excerpt a post shows when it has none of its own (BR-132):
// the first paragraph of body that is not a heading, with the Markdown taken
// out, cut at 160 characters on a word boundary and ended with "…".
func Excerpt(body string) string {
	for _, para := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" || strings.HasPrefix(para, "#") || strings.HasPrefix(para, "```") ||
			strings.Trim(para, "-*_ ") == "" {
			continue
		}
		if text := plain(para); text != "" {
			return cut(text)
		}
	}
	return ""
}

// ResolveExcerpt is what the storefront shows and the admin previews: the
// written excerpt, or the one worked out from the body.
func ResolveExcerpt(excerpt *string, body string) string {
	if excerpt != nil && strings.TrimSpace(*excerpt) != "" {
		return *excerpt
	}
	return Excerpt(body)
}

func plain(md string) string {
	s := mdImage.ReplaceAllString(md, "")
	s = mdLink.ReplaceAllString(s, "$1")
	s = htmlTag.ReplaceAllString(s, "")
	s = lineMarker.ReplaceAllString(s, "")
	s = strings.NewReplacer("**", "", "__", "", "~~", "", "`", "").Replace(s)
	s = emphOpen.ReplaceAllString(s, "$1$2")
	s = emphClose.ReplaceAllString(s, "$1$2")
	return strings.TrimSpace(spaces.ReplaceAllString(s, " "))
}

func cut(s string) string {
	if utf8.RuneCountInString(s) <= excerptRunes {
		return s
	}
	r := []rune(s)[:excerptRunes]
	if i := strings.LastIndexByte(string(r), ' '); i > 0 {
		return strings.TrimRight(string(r)[:i], " ,;:") + "…"
	}
	return string(r) + "…"
}
