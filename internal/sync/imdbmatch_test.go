package sync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vodarr/vodarr/internal/index"
	"github.com/vodarr/vodarr/internal/tmdb"
)

// queryTMDB is a fake TMDB whose searches answer by query text, with
// "Law & Order" (TV 549) and "Fast & Furious" (movie 13804) only findable
// under their "&" spelling, and tt0098844 linked to Law & Order.
func queryTMDB(t *testing.T) *tmdb.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		switch {
		case r.URL.Path == "/search/tv" && q == "Law & Order":
			w.Write([]byte(`{"results":[{"id":549,"name":"Law & Order","first_air_date":"1990-09-13"}]}`))
		case r.URL.Path == "/search/movie" && q == "Fast & Furious":
			w.Write([]byte(`{"results":[{"id":13804,"title":"Fast & Furious","release_date":"2009-04-02"}]}`))
		case strings.HasPrefix(r.URL.Path, "/search/"):
			// A loose result that is not the title, as TMDB gives.
			w.Write([]byte(`{"results":[{"id":1,"name":"Order","title":"Furious 7"}]}`))
		case r.URL.Path == "/find/tt0098844":
			w.Write([]byte(`{"movie_results":[],"tv_results":[{"id":549}]}`))
		case r.URL.Path == "/find/tt9999999":
			w.Write([]byte(`{"movie_results":[],"tv_results":[]}`))
		case r.URL.Path == "/tv/549/external_ids":
			w.Write([]byte(`{"imdb_id":"tt0098844","tvdb_id":72368}`))
		case r.URL.Path == "/tv/549":
			w.Write([]byte(`{"name":"Law & Order"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	tc := tmdb.NewClient("testkey")
	tc.SetBaseURL(srv.URL)
	t.Cleanup(tc.Stop)
	return tc
}

func TestTitleSearchTriesTheOtherAndSpelling(t *testing.T) {
	sched := movieScheduler(queryTMDB(t))
	ctx := context.Background()

	show := &index.Item{Type: index.TypeSeries, Name: "Law and Order"}
	if err := sched.resolveByTitle(ctx, show); err != nil || show.TMDBId != "549" {
		t.Errorf("Law and Order: TMDB %q, %v; want 549 via the & spelling", show.TMDBId, err)
	}
	film := &index.Item{Type: index.TypeMovie, Name: "Fast and Furious (2009)", Year: "2009"}
	if err := sched.resolveByTitle(ctx, film); err != nil || film.TMDBId != "13804" {
		t.Errorf("Fast and Furious: TMDB %q, %v; want 13804 via the & spelling", film.TMDBId, err)
	}
}

func TestTitleSpellings(t *testing.T) {
	cases := map[string][]string{
		"Law & Order":            {"Law & Order", "Law and Order"},
		"Law&Order":              {"Law&Order", "Law and Order"},
		"Fast and Furious":       {"Fast and Furious", "Fast & Furious"},
		"Andor":                  {"Andor"},
		"Sandy and Brandy and X": {"Sandy and Brandy and X", "Sandy & Brandy & X"},
	}
	for in, want := range cases {
		got := titleSpellings(in)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("titleSpellings(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestManualMatchByIMDB(t *testing.T) {
	sched := movieScheduler(queryTMDB(t))
	sched.idx = index.New()
	sched.idx.Replace([]*index.Item{
		{Type: index.TypeSeries, XtreamID: 7, Name: "L&O"},
		{Type: index.TypeMovie, XtreamID: 8, Name: "Obscure Film"},
	})
	ctx := context.Background()

	// IMDB only: TMDB's entry for it supplies the name and TVDB ID.
	got, err := sched.SetMatch(ctx, index.TypeSeries, 7, MatchOverride{IMDBId: " TT0098844 "})
	if err != nil {
		t.Fatalf("SetMatch: %v", err)
	}
	if got.TMDBId != "549" || got.TVDBId != "72368" || got.IMDBId != "tt0098844" || got.CanonicalName != "Law & Order" {
		t.Errorf("matched %+v, want TMDB 549 / TVDB 72368 / tt0098844", got)
	}

	// An IMDB ID TMDB does not know is still used, on its own.
	got, err = sched.SetMatch(ctx, index.TypeMovie, 8, MatchOverride{IMDBId: "tt9999999"})
	if err != nil {
		t.Fatalf("SetMatch unknown IMDB: %v", err)
	}
	if got.IMDBId != "tt9999999" || got.TMDBId != "" || got.EnrichFailReason != "" {
		t.Errorf("matched %+v, want just IMDB tt9999999", got)
	}

	if _, err := sched.SetMatch(ctx, index.TypeMovie, 8, MatchOverride{IMDBId: "0098844"}); err == nil {
		t.Error("an IMDB ID without tt was accepted")
	}
}
