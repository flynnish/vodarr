package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vodarr/vodarr/internal/config"
	"github.com/vodarr/vodarr/internal/index"
	"github.com/vodarr/vodarr/internal/tmdb"
)

func TestNormalizeSeriesTitle(t *testing.T) {
	cases := map[string]string{
		"Scrubs":                      "scrubs",
		"Scrubs (2026)":               "scrubs",
		"The Office (US)":             "office",
		"The Great British Bake Off":  "great british bake off",
		"Great British Bake-Off":      "great british bake off",
		"Law & Order: SVU":            "law and order svu",
		"Bake Off: The Professionals": "bake off the professionals",
		"  Doctor Who  ":              "doctor who",
		"1883":                        "1883",
	}
	for in, want := range cases {
		if got := normalizeSeriesTitle(in); got != want {
			t.Errorf("normalizeSeriesTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClosestYearIndex(t *testing.T) {
	years := []int{2026, 2001}
	if got := closestYearIndex(years, 2001); got != 1 {
		t.Errorf("exact 2001: got %d, want 1", got)
	}
	if got := closestYearIndex(years, 2026); got != 0 {
		t.Errorf("exact 2026: got %d, want 0", got)
	}
	// Provider reports the latest season's year: prefer the latest premiere
	// before it, not the nearest one after it.
	if got := closestYearIndex(years, 2010); got != 1 {
		t.Errorf("2010: got %d, want 1", got)
	}
	if got := closestYearIndex(years, 0); got != 0 {
		t.Errorf("no year: got %d, want 0 (search order)", got)
	}
}

// tvMock serves /search/tv (keyed by first_air_date_year, "" = unfiltered),
// /tv/{id} (name + number_of_seasons) and /tv/{id}/external_ids.
type tvShow struct {
	id      int
	name    string
	aired   string
	seasons int
	tvdbID  int
}

func newTVMock(t *testing.T, search map[string][]tvShow, shows []tvShow) *tmdb.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/search/tv", func(w http.ResponseWriter, r *http.Request) {
		var results []map[string]any
		for _, s := range search[r.URL.Query().Get("first_air_date_year")] {
			results = append(results, map[string]any{"id": s.id, "name": s.name, "first_air_date": s.aired})
		}
		json.NewEncoder(w).Encode(map[string]any{"results": results})
	})
	for _, s := range shows {
		s := s
		mux.HandleFunc("/tv/"+itoa(s.id), func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"name": s.name, "number_of_seasons": s.seasons})
		})
		mux.HandleFunc("/tv/"+itoa(s.id)+"/external_ids", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"tvdb_id": s.tvdbID})
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	tc := tmdb.NewClient("testkey")
	tc.SetBaseURL(srv.URL)
	t.Cleanup(tc.Stop)
	return tc
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func episodesThrough(seasons int) []index.EpisodeItem {
	var eps []index.EpisodeItem
	for s := 1; s <= seasons; s++ {
		eps = append(eps, index.EpisodeItem{EpisodeID: s, Season: s, EpisodeNum: 1})
	}
	return eps
}

var (
	scrubsRevival  = tvShow{id: 2, name: "Scrubs", aired: "2026-02-25", seasons: 1, tvdbID: 200}
	scrubsOriginal = tvShow{id: 1, name: "Scrubs", aired: "2001-10-02", seasons: 9, tvdbID: 100}
	scrubsInterns  = tvShow{id: 3, name: "Scrubs: Interns", aired: "2009-12-01", seasons: 1, tvdbID: 300}
)

func scrubsScheduler(t *testing.T) *Scheduler {
	tc := newTVMock(t,
		map[string][]tvShow{
			"":     {scrubsRevival, scrubsOriginal, scrubsInterns}, // revival ranks first
			"2001": {scrubsOriginal},
			"2026": {scrubsRevival},
		},
		[]tvShow{scrubsRevival, scrubsOriginal, scrubsInterns},
	)
	cfg := &config.Config{}
	cfg.TMDB.APIKey = "testkey"
	cfg.Sync.Parallelism = 1
	return &Scheduler{cfg: cfg, tmdb: tc}
}

func TestResolveByTitleSharedSeriesTitle(t *testing.T) {
	cases := []struct {
		name     string
		item     index.Item
		wantTMDB string
	}{
		{
			// No year from the provider: TMDB ranks the revival first, but it
			// has one season and the provider carries nine.
			name:     "original without year, by season count",
			item:     index.Item{Name: "Scrubs", Episodes: episodesThrough(9)},
			wantTMDB: "1",
		},
		{
			name:     "original with year",
			item:     index.Item{Name: "Scrubs", Year: "2001", Episodes: episodesThrough(9)},
			wantTMDB: "1",
		},
		{
			// Provider year is that of its newest season: the 2026 search only
			// holds the revival, but the season count rules it out.
			name:     "original with latest-season year",
			item:     index.Item{Name: "Scrubs", Year: "2026", Episodes: episodesThrough(9)},
			wantTMDB: "1",
		},
		{
			name:     "revival with year in name",
			item:     index.Item{Name: "Scrubs (2026)", Year: "2026", Episodes: episodesThrough(1)},
			wantTMDB: "2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sched := scrubsScheduler(t)
			item := tc.item
			item.Type = index.TypeSeries
			if err := sched.resolveByTitle(context.Background(), &item); err != nil {
				t.Fatalf("resolveByTitle: %v", err)
			}
			if item.TMDBId != tc.wantTMDB {
				t.Errorf("TMDBId = %q, want %q", item.TMDBId, tc.wantTMDB)
			}
		})
	}
}

func TestResolveByTitlePrefersExactTitleOverYearFilteredHit(t *testing.T) {
	// The provider dates the series by its latest season (2023). The
	// year-filtered search returns an unrelated 2023 show; the old code took
	// it. The exact title from the unfiltered search must win.
	gbbo := tvShow{id: 13, name: "The Great British Bake Off", aired: "2010-08-17", seasons: 15}
	tc := newTVMock(t,
		map[string][]tvShow{
			"2023": {{id: 11, name: "The Great American Baking Show: Celebrity Holiday", aired: "2023-11-27"}},
			"":     {{id: 12, name: "The Great British Bake Off: An Extra Slice", aired: "2013-08-20"}, gbbo},
		},
		[]tvShow{gbbo},
	)
	cfg := &config.Config{}
	cfg.TMDB.APIKey = "testkey"
	sched := &Scheduler{cfg: cfg, tmdb: tc}

	item := &index.Item{Type: index.TypeSeries, Name: "Great British Bake Off", Year: "2023"}
	if err := sched.resolveByTitle(context.Background(), item); err != nil {
		t.Fatalf("resolveByTitle: %v", err)
	}
	if item.TMDBId != "13" {
		t.Errorf("TMDBId = %q, want 13", item.TMDBId)
	}
}

func TestResolveByTitleNoExactMatchKeepsTopHit(t *testing.T) {
	// Abbreviated provider title: nothing matches exactly, so TMDB's top
	// hit is still used, as before.
	tc := newTVMock(t,
		map[string][]tvShow{
			"": {{id: 1400, name: "It's Always Sunny in Philadelphia", aired: "2005-08-04"}},
		},
		nil,
	)
	cfg := &config.Config{}
	cfg.TMDB.APIKey = "testkey"
	sched := &Scheduler{cfg: cfg, tmdb: tc}

	item := &index.Item{Type: index.TypeSeries, Name: "Always Sunny"}
	if err := sched.resolveByTitle(context.Background(), item); err != nil {
		t.Fatalf("resolveByTitle: %v", err)
	}
	if item.TMDBId != "1400" {
		t.Errorf("TMDBId = %q, want 1400", item.TMDBId)
	}
}

func TestEnrichRedoesStaleSeriesMatch(t *testing.T) {
	// A series cached by the old matcher holds the revival's IDs. With an
	// unchanged name it used to be skipped forever; now it is re-matched.
	sched := scrubsScheduler(t)
	item := &index.Item{Type: index.TypeSeries, XtreamID: 7, Name: "Scrubs", Episodes: episodesThrough(9)}
	cached := map[string]*index.Item{
		"series:7": {Type: index.TypeSeries, XtreamID: 7, Name: "Scrubs",
			TMDBId: "2", TVDBId: "200", CanonicalName: "Scrubs", Year: "2026"},
	}

	out, err := sched.enrich(context.Background(), []*index.Item{item}, cached)
	if err != nil {
		t.Fatalf("enrich: %v", err)
	}
	if got := out[0]; got.TMDBId != "1" || got.TVDBId != "100" {
		t.Errorf("got TMDB %q / TVDB %q, want 1 / 100", got.TMDBId, got.TVDBId)
	}
	if out[0].MatchVersion != matchVersion {
		t.Errorf("MatchVersion = %d, want %d", out[0].MatchVersion, matchVersion)
	}

	// Once re-matched, the cache entry is trusted again.
	fresh := &index.Item{Type: index.TypeSeries, XtreamID: 7, Name: "Scrubs"}
	out, _ = sched.enrich(context.Background(), []*index.Item{fresh}, map[string]*index.Item{"series:7": out[0]})
	if out[0].TVDBId != "100" {
		t.Errorf("cached re-use: TVDB %q, want 100", out[0].TVDBId)
	}
}

// movieMock serves /search/movie (keyed by the year param, "" = unfiltered),
// /movie/{id} (title, runtime, release date) and /movie/{id}/external_ids.
type movieEntry struct {
	id      int
	title   string
	release string
	runtime int
	imdb    string
}

func newMovieMock(t *testing.T, search map[string][]movieEntry, movies []movieEntry) (*tmdb.Client, *int) {
	t.Helper()
	searches := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/search/movie", func(w http.ResponseWriter, r *http.Request) {
		searches++
		var results []map[string]any
		for _, m := range search[r.URL.Query().Get("year")] {
			results = append(results, map[string]any{"id": m.id, "title": m.title, "release_date": m.release})
		}
		json.NewEncoder(w).Encode(map[string]any{"results": results})
	})
	for _, m := range movies {
		m := m
		mux.HandleFunc("/movie/"+itoa(m.id), func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"title": m.title, "runtime": m.runtime, "release_date": m.release})
		})
		mux.HandleFunc("/movie/"+itoa(m.id)+"/external_ids", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"imdb_id": m.imdb})
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	tc := tmdb.NewClient("testkey")
	tc.SetBaseURL(srv.URL)
	t.Cleanup(tc.Stop)
	return tc, &searches
}

func movieScheduler(tc *tmdb.Client) *Scheduler {
	cfg := &config.Config{}
	cfg.TMDB.APIKey = "testkey"
	cfg.Sync.Parallelism = 1
	return &Scheduler{cfg: cfg, tmdb: tc}
}

var (
	lionKing2019 = movieEntry{id: 420818, title: "The Lion King", release: "2019-07-12", runtime: 118, imdb: "tt6105098"}
	lionKing1994 = movieEntry{id: 8587, title: "The Lion King", release: "1994-06-24", runtime: 89, imdb: "tt0110357"}
	dune1984     = movieEntry{id: 841, title: "Dune", release: "1984-12-14", runtime: 137, imdb: "tt0087182"}
	dune2021     = movieEntry{id: 438631, title: "Dune", release: "2021-09-15", runtime: 155, imdb: "tt1160419"}
	duneMaking   = movieEntry{id: 9, title: "Dune: The Making Of", release: "2021-10-01", runtime: 30}
)

func TestResolveByTitleMovieRemakes(t *testing.T) {
	search := map[string][]movieEntry{
		"":     {lionKing2019, lionKing1994, dune2021, dune1984},
		"1994": {lionKing1994},
		"2019": {lionKing2019},
		"2021": {duneMaking, dune2021}, // a same-year non-match ranks first
	}
	all := []movieEntry{lionKing2019, lionKing1994, dune1984, dune2021, duneMaking}

	cases := []struct {
		name     string
		item     index.Item
		wantTMDB string
	}{
		{
			// No year: the popular remake ranks first, but the provider's
			// 88-minute runtime only fits the original.
			name:     "no year, by runtime",
			item:     index.Item{Name: "The Lion King", Duration: 88 * 60},
			wantTMDB: "8587",
		},
		{
			name:     "year picks remake",
			item:     index.Item{Name: "The Lion King", Year: "2019"},
			wantTMDB: "420818",
		},
		{
			name:     "year picks original",
			item:     index.Item{Name: "Lion King (1994)", Year: "1994"},
			wantTMDB: "8587",
		},
		{
			// The year-filtered search ranks a making-of first; the old
			// code took it.
			name:     "exact title beats same-year top hit",
			item:     index.Item{Name: "Dune", Year: "2021"},
			wantTMDB: "438631",
		},
		{
			// Provider year off by one (regional release).
			name:     "year off by one",
			item:     index.Item{Name: "Dune", Year: "2020"},
			wantTMDB: "438631",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := newMovieMock(t, search, all)
			item := tc.item
			item.Type = index.TypeMovie
			if err := movieScheduler(client).resolveByTitle(context.Background(), &item); err != nil {
				t.Fatalf("resolveByTitle: %v", err)
			}
			if item.TMDBId != tc.wantTMDB {
				t.Errorf("TMDBId = %q, want %q", item.TMDBId, tc.wantTMDB)
			}
		})
	}
}

func TestEnrichRedoesStaleMovieMatchOnly(t *testing.T) {
	client, searches := newMovieMock(t,
		map[string][]movieEntry{"": {lionKing2019, lionKing1994}},
		[]movieEntry{lionKing2019, lionKing1994},
	)
	sched := movieScheduler(client)

	// Title-matched by the old code to the remake; the provider's runtime
	// says original. It must be re-matched.
	matched := &index.Item{Type: index.TypeMovie, XtreamID: 1, Name: "The Lion King", Duration: 88 * 60}
	// Provider-tagged: never title-matched, so its cache is kept as is.
	tagged := &index.Item{Type: index.TypeMovie, XtreamID: 2, Name: "Some Film", TMDBId: "555"}
	cached := map[string]*index.Item{
		"movie:1": {Type: index.TypeMovie, XtreamID: 1, Name: "The Lion King",
			TMDBId: "420818", IMDBId: "tt6105098", CanonicalName: "The Lion King", Year: "2019"},
		"movie:2": {Type: index.TypeMovie, XtreamID: 2, Name: "Some Film",
			TMDBId: "555", IMDBId: "tt0000555", CanonicalName: "Some Film", Year: "2001"},
	}

	out, err := sched.enrich(context.Background(), []*index.Item{matched, tagged}, cached)
	if err != nil {
		t.Fatalf("enrich: %v", err)
	}
	if out[0].TMDBId != "8587" || out[0].IMDBId != "tt0110357" {
		t.Errorf("stale movie: got TMDB %q / IMDB %q, want 8587 / tt0110357", out[0].TMDBId, out[0].IMDBId)
	}
	if out[1].IMDBId != "tt0000555" {
		t.Errorf("provider-tagged movie: IMDB %q, want cached tt0000555", out[1].IMDBId)
	}
	if *searches != 1 {
		t.Errorf("searches = %d, want 1 (only the stale movie)", *searches)
	}
}
