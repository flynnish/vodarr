package sync

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/vodarr/vodarr/internal/index"
	"github.com/vodarr/vodarr/internal/tmdb"
	"github.com/vodarr/vodarr/internal/tvdb"
)

// matchVersion is stored on each enriched item. Bump it whenever title
// matching changes in a way that should correct IDs already held in the
// index cache: items matched by title under an older version are
// re-enriched once instead of keeping their (possibly wrong) IDs forever.
//
// 1: pick among all same-titled search results instead of trusting the
// first one (Scrubs 2001 vs Scrubs 2026, Bake Off and its spin-offs, The
// Lion King 1994 vs 2019).
const matchVersion = 1

// maxDetailChecks bounds the extra TMDB detail calls made to break a tie
// between shows or movies that share a title.
const maxDetailChecks = 5

// trailingQualifierRe matches a trailing disambiguator such as "(2026)",
// "(US)" or "(UK)" that TVDB and IPTV providers append to shared titles.
var trailingQualifierRe = regexp.MustCompile(`\s*\((\d{4}|[A-Za-z]{2})\)\s*$`)

// normalizeSeriesTitle reduces a title to a comparable form: lowercased,
// punctuation removed, "&" treated as "and", leading article and trailing
// year/country disambiguator dropped.
func normalizeSeriesTitle(s string) string {
	s = trailingQualifierRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(strings.ToLower(s), "&", " and ")

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
	out := strings.TrimSpace(b.String())
	return strings.TrimPrefix(out, "the ")
}

// maxSeason returns the highest season number among a series' episodes.
func maxSeason(item *index.Item) int {
	n := 0
	for _, ep := range item.Episodes {
		if ep.Season > n {
			n = ep.Season
		}
	}
	return n
}

// yearOf parses the leading 4-digit year of a date or year string, or 0.
func yearOf(s string) int {
	if len(s) < 4 {
		return 0
	}
	y, err := strconv.Atoi(s[:4])
	if err != nil {
		return 0
	}
	return y
}

// closestYearIndex picks the candidate whose year best fits the provider
// year: an exact match first, then the latest year before it (providers
// often report the date of the newest season, not the premiere), then the
// nearest year after it. Returns 0 (TMDB/TVDB's own order) when year is 0
// or no candidate has a year.
func closestYearIndex(years []int, year int) int {
	if year <= 0 {
		return 0
	}
	best, bestScore := 0, -1
	for i, y := range years {
		if y <= 0 {
			continue
		}
		var score int
		switch {
		case y == year:
			score = 1_000_000
		case y < year:
			score = 500_000 - (year - y)
		default:
			score = 1_000 - (y - year)
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	return best
}

// searchTMDBSeries finds the TMDB show that best matches an IPTV series.
//
// TMDB's first hit for a shared title is frequently the wrong show: a
// revival outranks the original, or a spin-off outranks the main series.
// So every result whose title matches exactly is collected (from both the
// year-filtered and the unfiltered search, since the provider's year is
// often that of the latest season), and ties are broken by season count
// and year. Without any exact match, TMDB's top hit is used as before.
func (s *Scheduler) searchTMDBSeries(ctx context.Context, title string, year, seasons int) (*tmdb.TVSearchResult, error) {
	want := normalizeSeriesTitle(title)

	var yearResults []tmdb.TVSearchResult
	if year > 0 {
		var err error
		yearResults, err = s.tmdb.SearchTVAll(ctx, title, year)
		if err != nil {
			return nil, err
		}
	}
	allResults, err := s.tmdb.SearchTVAll(ctx, title, 0)
	if err != nil {
		return nil, err
	}

	var exact []tmdb.TVSearchResult
	seen := make(map[int]bool)
	for _, list := range [][]tmdb.TVSearchResult{yearResults, allResults} {
		for _, r := range list {
			if seen[r.ID] {
				continue
			}
			if normalizeSeriesTitle(r.Name) == want || normalizeSeriesTitle(r.OriginalName) == want {
				seen[r.ID] = true
				exact = append(exact, r)
			}
		}
	}

	switch {
	case len(exact) == 1:
		return &exact[0], nil
	case len(exact) > 1:
		exact = s.filterBySeasonCount(ctx, exact, seasons)
		years := make([]int, len(exact))
		for i, r := range exact {
			years[i] = yearOf(r.FirstAirDate)
		}
		return &exact[closestYearIndex(years, year)], nil
	case len(yearResults) > 0:
		return &yearResults[0], nil
	case len(allResults) > 0:
		return &allResults[0], nil
	}
	return nil, nil
}

// filterBySeasonCount drops candidates that have fewer seasons on TMDB than
// the provider carries: a one-season revival cannot be the show the
// provider has nine seasons of. Unknown counts are kept. If every candidate
// would be dropped, the list is returned unchanged.
func (s *Scheduler) filterBySeasonCount(ctx context.Context, cands []tmdb.TVSearchResult, seasons int) []tmdb.TVSearchResult {
	if seasons <= 1 {
		return cands
	}
	var kept []tmdb.TVSearchResult
	for i, c := range cands {
		if i >= maxDetailChecks {
			kept = append(kept, c)
			continue
		}
		n, err := s.tmdb.GetTVSeasonCount(ctx, c.ID)
		if err != nil || n == 0 || n >= seasons {
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		return cands
	}
	return kept
}

// searchTVDBSeries finds the TVDB series that best matches an IPTV series,
// preferring exact title matches (TVDB disambiguates shared titles as
// "Scrubs (2026)") and breaking ties by year.
func (s *Scheduler) searchTVDBSeries(ctx context.Context, title string, year int) (*tvdb.SeriesResult, error) {
	results, err := s.tvdb.SearchSeriesAll(ctx, title)
	if err != nil || len(results) == 0 {
		return nil, err
	}

	want := normalizeSeriesTitle(title)
	var exact []tvdb.SeriesResult
	for _, r := range results {
		if normalizeSeriesTitle(r.Name) == want {
			exact = append(exact, r)
		}
	}
	if len(exact) == 0 {
		return &results[0], nil
	}
	years := make([]int, len(exact))
	for i, r := range exact {
		years[i] = yearOf(r.Year)
		if years[i] == 0 {
			// TVDB sometimes leaves year empty but keeps it in the name.
			if m := yearInParensRe.FindStringSubmatch(r.Name); m != nil {
				years[i], _ = strconv.Atoi(m[1])
			}
		}
	}
	return &exact[closestYearIndex(years, year)], nil
}

// searchTMDBMovie finds the TMDB movie that best matches an IPTV movie.
//
// As with series, the first hit for a shared title is often another
// version: the more popular remake, or a same-year film with a similar
// name. Exact title matches from the year-filtered and unfiltered searches
// are collected; ties are broken by year (within one year, since release
// years differ between countries and festivals) and then by the runtime
// the provider reports. Without any exact match the old behaviour stands:
// the year-filtered top hit, else the unfiltered one.
func (s *Scheduler) searchTMDBMovie(ctx context.Context, title string, year int, durationSecs float64) (*tmdb.MovieSearchResult, error) {
	want := normalizeSeriesTitle(title)

	var yearResults []tmdb.MovieSearchResult
	if year > 0 {
		var err error
		yearResults, err = s.tmdb.SearchMovieAll(ctx, title, year)
		if err != nil {
			return nil, err
		}
	}
	allResults, err := s.tmdb.SearchMovieAll(ctx, title, 0)
	if err != nil {
		return nil, err
	}

	var exact []tmdb.MovieSearchResult
	seen := make(map[int]bool)
	for _, list := range [][]tmdb.MovieSearchResult{yearResults, allResults} {
		for _, r := range list {
			if seen[r.ID] {
				continue
			}
			if normalizeSeriesTitle(r.Title) == want || normalizeSeriesTitle(r.OriginalTitle) == want {
				seen[r.ID] = true
				exact = append(exact, r)
			}
		}
	}

	if len(exact) == 0 {
		switch {
		case len(yearResults) > 0:
			return &yearResults[0], nil
		case len(allResults) > 0:
			return &allResults[0], nil
		}
		return nil, nil
	}

	if year > 0 {
		var near []tmdb.MovieSearchResult
		for _, r := range exact {
			if y := yearOf(r.ReleaseDate); y > 0 && absInt(y-year) <= 1 {
				near = append(near, r)
			}
		}
		if len(near) > 0 {
			exact = near
		}
	}
	if len(exact) > 1 {
		exact = s.filterByRuntime(ctx, exact, durationSecs)
	}

	best, bestDiff := 0, -1
	for i, r := range exact {
		y := yearOf(r.ReleaseDate)
		if year <= 0 || y <= 0 {
			continue
		}
		if d := absInt(y - year); bestDiff < 0 || d < bestDiff {
			best, bestDiff = i, d
		}
	}
	return &exact[best], nil
}

// filterByRuntime keeps candidates whose TMDB runtime is close to the
// provider's duration (within 10 minutes or 15%, whichever is larger).
// Unknown runtimes are kept; if nothing is close, the list is unchanged.
func (s *Scheduler) filterByRuntime(ctx context.Context, cands []tmdb.MovieSearchResult, durationSecs float64) []tmdb.MovieSearchResult {
	mins := int(durationSecs / 60)
	if mins <= 0 {
		return cands
	}
	tolerance := max(10, mins*15/100)

	var near, unknown []tmdb.MovieSearchResult
	for i, c := range cands {
		if i >= maxDetailChecks {
			unknown = append(unknown, c)
			continue
		}
		d, err := s.tmdb.GetMovieDetails(ctx, c.ID)
		switch {
		case err != nil || d == nil || d.RuntimeMins == 0:
			unknown = append(unknown, c)
		case absInt(d.RuntimeMins-mins) <= tolerance:
			near = append(near, c)
		}
	}
	if len(near) == 0 {
		return cands
	}
	return append(near, unknown...)
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
