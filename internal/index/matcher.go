package index

import (
	"regexp"
	"strings"
	"unicode"
)

// trailingArticleRe matches a catalogue-style title with its article moved
// to the end, "Office, The", optionally followed by a qualifier such as
// "(2005)" or "[UK]".
var trailingArticleRe = regexp.MustCompile(`(?i)^\s*(.+?),\s*(the|a|an)(\s*[(\[].*)?\s*$`)

// MoveTrailingArticle turns "Office, The" into "The Office" (keeping any
// trailing qualifier), so it can be searched for and compared.
func MoveTrailingArticle(s string) string {
	return trailingArticleRe.ReplaceAllString(s, "$2 $1$3")
}

// NormalizeTitle reduces a title to a comparable form, so that "The Office",
// "Office" and "Office, The" or "Fast & Furious" and "Fast and Furious" are
// the same: lowercased, punctuation removed, "&" read as "and" and a leading
// (or catalogue-style trailing) "the", "a" or "an" dropped.
func NormalizeTitle(s string) string {
	return normalizeTitle(s)
}

// normalizeTitle lowercases, strips punctuation and common noise words.
func normalizeTitle(s string) string {
	s = MoveTrailingArticle(s)
	s = strings.ReplaceAll(strings.ToLower(s), "&", " and ")

	// Remove common noise: articles, punctuation
	var b strings.Builder
	prevSpace := true
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevSpace = false
		} else if !prevSpace {
			b.WriteRune(' ')
			prevSpace = true
		}
	}
	result := strings.TrimSpace(b.String())

	// Remove leading articles
	for _, article := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(result, article) {
			result = result[len(article):]
			break
		}
	}

	return result
}

// titleSimilarity returns a score in [0, 1] based on trigram overlap.
func titleSimilarity(a, b string) float64 {
	if a == b {
		return 1.0
	}
	if a == "" || b == "" {
		return 0.0
	}

	triA := trigrams(a)
	triB := trigrams(b)

	if len(triA) == 0 || len(triB) == 0 {
		// Fall back to exact prefix match for very short strings
		if strings.HasPrefix(b, a) || strings.HasPrefix(a, b) {
			return 0.8
		}
		return 0.0
	}

	// Jaccard similarity of trigram sets
	intersection := 0
	setB := make(map[string]struct{}, len(triB))
	for _, t := range triB {
		setB[t] = struct{}{}
	}
	for _, t := range triA {
		if _, ok := setB[t]; ok {
			intersection++
		}
	}
	union := len(triA) + len(triB) - intersection
	if union == 0 {
		return 0
	}
	jaccard := float64(intersection) / float64(union)

	// Boost if one is a prefix/substring of the other
	if strings.Contains(b, a) || strings.Contains(a, b) {
		jaccard = min(1.0, jaccard+0.2)
	}

	return jaccard
}

// trigrams returns the set of character trigrams for a string.
func trigrams(s string) []string {
	runes := []rune(s)
	if len(runes) < 3 {
		return []string{s}
	}
	out := make([]string, 0, len(runes)-2)
	for i := 0; i <= len(runes)-3; i++ {
		out = append(out, string(runes[i:i+3]))
	}
	return out
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
