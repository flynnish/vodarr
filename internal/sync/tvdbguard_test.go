package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vodarr/vodarr/internal/index"
	"github.com/vodarr/vodarr/internal/tvdb"
)

// tvdbWithResults serves a TVDB login and a /search that returns results.
func tvdbWithResults(t *testing.T, results []map[string]any) *tvdb.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"token": "t"}})
		case "/search":
			json.NewEncoder(w).Encode(map[string]any{"data": results})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := tvdb.NewClient("key")
	c.SetBaseURL(srv.URL)
	return c
}

// normalPeople2020 is TheTVDB's Normal People, linked to its own IMDB/TMDB.
var normalPeople2020 = map[string]any{
	"tvdb_id": "371806", "name": "Normal People", "year": "2020",
	"remote_ids": []map[string]any{
		{"id": "tt9059760", "sourceName": "IMDB"},
		{"id": "89905", "sourceName": "TheMovieDB.com"},
	},
}

func TestTVDBSearchRejectsAnotherShowsEntry(t *testing.T) {
	sched := &Scheduler{tvdb: tvdbWithResults(t, []map[string]any{normalPeople2020})}
	ctx := context.Background()

	// The provider's 2021 show is identified by TMDB/IMDB but TMDB has no
	// TVDB link for it. TheTVDB's same-named result belongs to another show.
	got, err := sched.searchTVDBSeries(ctx, "Normal People", 2021, "tt14372194", "126553")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("borrowed another show's TVDB entry: %+v", got)
	}

	// The real show is still matched.
	got, _ = sched.searchTVDBSeries(ctx, "Normal People", 2020, "tt9059760", "89905")
	if got == nil || got.TVDBID != 371806 {
		t.Errorf("real show: got %+v, want 371806", got)
	}
}

func TestTVDBSearchRejectsDistantYear(t *testing.T) {
	// No remote IDs to compare, but the years are far apart.
	sched := &Scheduler{tvdb: tvdbWithResults(t, []map[string]any{
		{"tvdb_id": "500", "name": "The Office", "year": "2001"},
	})}
	if got, _ := sched.searchTVDBSeries(context.Background(), "The Office", 2005, "", ""); got != nil {
		t.Errorf("2001 show taken for a 2005 one: %+v", got)
	}
	// One year off is tolerated (provider dates are often a year out).
	if got, _ := sched.searchTVDBSeries(context.Background(), "The Office", 2002, "", ""); got == nil {
		t.Error("one year off should still match")
	}
}

func TestConflictingTVDBIDs(t *testing.T) {
	cached := map[string]*index.Item{
		"series:1": {Type: index.TypeSeries, XtreamID: 1, TVDBId: "371806", TMDBId: "89905"},
		"series:2": {Type: index.TypeSeries, XtreamID: 2, TVDBId: "371806", TMDBId: "126553"}, // borrowed
		"series:3": {Type: index.TypeSeries, XtreamID: 3, TVDBId: "76156", TMDBId: "4556"},
		"series:4": {Type: index.TypeSeries, XtreamID: 4, TVDBId: "76156", TMDBId: "4556"}, // a second copy: fine
		"series:5": {Type: index.TypeSeries, XtreamID: 5, TVDBId: "999", TMDBId: "1", ManualMatch: true},
		"series:6": {Type: index.TypeSeries, XtreamID: 6, TVDBId: "999", TMDBId: "2"},
	}
	got := conflictingTVDBIDs(cached)
	if !got["371806"] {
		t.Error("371806 is shared by two TMDB shows and should be re-matched")
	}
	if got["76156"] {
		t.Error("two copies of one show are not a conflict")
	}
	if got["999"] {
		t.Error("a manual match must not be overridden")
	}
}
