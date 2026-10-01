package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/vodarr/vodarr/internal/config"
	"github.com/vodarr/vodarr/internal/index"
	"github.com/vodarr/vodarr/internal/strm"
	"github.com/vodarr/vodarr/internal/xtream"
)

// groupedXtreamServer serves two VOD groups (English and Spanish) holding
// the same film, plus one English and one Spanish series.
func groupedXtreamServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("action") {
		case "get_vod_categories":
			json.NewEncoder(w).Encode([]map[string]any{
				{"category_id": "1", "category_name": "EN | Movies"},
				{"category_id": "2", "category_name": "ES | Películas"},
			})
		case "get_series_categories":
			json.NewEncoder(w).Encode([]map[string]any{
				{"category_id": "1", "category_name": "EN | Series"},
				{"category_id": "7", "category_name": "ES | Series"},
			})
		case "get_vod_streams":
			json.NewEncoder(w).Encode([]map[string]any{
				{"stream_id": 10, "name": "Inception", "category_id": "1", "container_extension": "mkv"},
				{"stream_id": 11, "name": "Inception", "category_id": "2", "container_extension": "mkv"},
			})
		case "get_series":
			json.NewEncoder(w).Encode([]map[string]any{
				{"series_id": 20, "name": "Scrubs", "category_id": "1"},
				{"series_id": 21, "name": "Scrubs", "category_id": "7"},
			})
		case "get_series_info":
			json.NewEncoder(w).Encode(map[string]any{
				"episodes": map[string]any{"1": []map[string]any{
					{"id": "500", "episode_num": 1, "title": "Pilot", "container_extension": "mkv"},
				}},
			})
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func groupedScheduler(t *testing.T, srv *httptest.Server, excluded []string) *Scheduler {
	t.Helper()
	out := t.TempDir()
	cfg := &config.Config{}
	cfg.Sync.GraceCycles = 3
	cfg.Sync.Parallelism = 1
	cfg.Sync.ExcludedCategories = excluded
	cfg.Output.Path = out
	cfg.Output.MoviesDir = "movies"
	cfg.Output.SeriesDir = "tv"
	return NewScheduler(cfg, xtream.NewClient(srv.URL, "u", "p"), nil, index.New(), strm.NewWriter(out, "movies", "tv"))
}

func TestSyncRecordsGroups(t *testing.T) {
	sched := groupedScheduler(t, groupedXtreamServer(t), nil)
	if err := sched.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	es := sched.idx.SearchByXtreamID(11, "movie")
	if es == nil || es.CategoryKey != "vod:2" || es.Category != "ES | Películas" {
		t.Fatalf("movie 11 group = %+v, want vod:2 / ES | Películas", es)
	}
	// Series category IDs are their own ID space: "1" is not the VOD "1".
	if s := sched.idx.SearchByXtreamID(20, "series"); s == nil || s.CategoryKey != "series:1" {
		t.Fatalf("series 20 group = %+v, want series:1", s)
	}
}

func TestExcludedGroupsLeftOutOfSync(t *testing.T) {
	sched := groupedScheduler(t, groupedXtreamServer(t), []string{"vod:2", "series:7"})
	if err := sched.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if sched.idx.SearchByXtreamID(11, "movie") != nil || sched.idx.SearchByXtreamID(21, "series") != nil {
		t.Error("items from excluded groups were indexed")
	}
	if sched.idx.SearchByXtreamID(10, "movie") == nil || sched.idx.SearchByXtreamID(20, "series") == nil {
		t.Error("items from kept groups are missing")
	}
}

func TestExcludingGroupRemovesNowAndSkipsGrace(t *testing.T) {
	ctx := context.Background()
	sched := groupedScheduler(t, groupedXtreamServer(t), nil)
	if err := sched.Sync(ctx); err != nil {
		t.Fatalf("sync 1: %v", err)
	}
	movies, series := sched.idx.Counts()
	if movies != 2 || series != 2 {
		t.Fatalf("after sync 1: %d movies / %d series, want 2 / 2", movies, series)
	}

	// Excluding takes effect immediately, without a sync.
	if removed := sched.SetExcludedCategories([]string{"vod:2", "series:7"}); removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	if sched.idx.SearchByXtreamID(11, "movie") != nil {
		t.Error("excluded movie still in index right after excluding")
	}

	// The next sync must not bring them back through the grace period:
	// they are left out on purpose, not missing from the provider.
	if err := sched.Sync(ctx); err != nil {
		t.Fatalf("sync 2: %v", err)
	}
	movies, series = sched.idx.Counts()
	if movies != 1 || series != 1 {
		t.Errorf("after sync 2: %d movies / %d series, want 1 / 1", movies, series)
	}

	// Re-including brings them back on the following sync.
	sched.SetExcludedCategories(nil)
	if err := sched.Sync(ctx); err != nil {
		t.Fatalf("sync 3: %v", err)
	}
	if movies, _ = sched.idx.Counts(); movies != 2 {
		t.Errorf("after re-including: %d movies, want 2", movies)
	}
}

func TestManualMatchLifecycle(t *testing.T) {
	ctx := context.Background()
	client, searches := newMovieMock(t,
		map[string][]movieEntry{"": {lionKing2019, lionKing1994}},
		[]movieEntry{lionKing2019, lionKing1994},
	)
	sched := movieScheduler(client)
	sched.idx = index.New()
	sched.cachePath = filepath.Join(t.TempDir(), ".vodarr-cache.json")
	matchesPath := filepath.Join(t.TempDir(), "matches.json")
	if err := sched.LoadOverrides(matchesPath); err != nil {
		t.Fatalf("LoadOverrides: %v", err)
	}

	// Automatically matched to the remake.
	auto := &index.Item{Type: index.TypeMovie, XtreamID: 1, Name: "The Lion King",
		TMDBId: "420818", IMDBId: "tt6105098", Year: "2019"}
	sched.idx.Replace([]*index.Item{auto})

	// The user corrects it to the original.
	got, err := sched.SetMatch(ctx, index.TypeMovie, 1, MatchOverride{TMDBId: "8587"})
	if err != nil {
		t.Fatalf("SetMatch: %v", err)
	}
	if got.IMDBId != "tt0110357" || !got.ManualMatch || got.Year != "1994" {
		t.Errorf("after SetMatch: IMDB %q manual %v year %q, want tt0110357 / true / 1994", got.IMDBId, got.ManualMatch, got.Year)
	}
	if live := sched.idx.SearchByXtreamID(1, "movie"); live.IMDBId != "tt0110357" {
		t.Errorf("live index IMDB = %q, want tt0110357", live.IMDBId)
	}
	if _, err := os.Stat(matchesPath); err != nil {
		t.Errorf("matches.json not written: %v", err)
	}

	// A sync (with a cache that still holds the old match) keeps the manual
	// match; it never reaches title search.
	fresh := &index.Item{Type: index.TypeMovie, XtreamID: 1, Name: "The Lion King"}
	cached := map[string]*index.Item{"movie:1": auto}
	before := *searches
	out, _ := sched.enrich(ctx, []*index.Item{fresh}, cached)
	if out[0].IMDBId != "tt0110357" || !out[0].ManualMatch {
		t.Errorf("after enrich: IMDB %q manual %v, want tt0110357 / true", out[0].IMDBId, out[0].ManualMatch)
	}
	if *searches != before {
		t.Error("enrich ran a title search for a manually matched item")
	}

	// It survives a restart.
	sched2 := movieScheduler(client)
	if err := sched2.LoadOverrides(matchesPath); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if ov, ok := sched2.override("movie:1"); !ok || ov.TMDBId != "8587" {
		t.Errorf("reloaded override = %+v, %v", ov, ok)
	}

	// Resetting returns to automatic matching.
	got, err = sched.ClearMatch(ctx, index.TypeMovie, 1)
	if err != nil {
		t.Fatalf("ClearMatch: %v", err)
	}
	if got.ManualMatch {
		t.Error("still marked manual after ClearMatch")
	}
	if _, ok := sched.override("movie:1"); ok {
		t.Error("override still stored after ClearMatch")
	}
}

func TestSetMatchValidation(t *testing.T) {
	sched := movieScheduler(nil)
	sched.idx = index.New()
	sched.idx.Replace([]*index.Item{{Type: index.TypeMovie, XtreamID: 1, Name: "X"}})
	ctx := context.Background()

	cases := []struct {
		name string
		id   int
		ov   MatchOverride
	}{
		{"no ids", 1, MatchOverride{}},
		{"not a number", 1, MatchOverride{TMDBId: "abc"}},
		{"tvdb on a movie", 1, MatchOverride{TVDBId: "81189"}},
		{"unknown item", 99, MatchOverride{TMDBId: "1"}},
	}
	for _, tc := range cases {
		if _, err := sched.SetMatch(ctx, index.TypeMovie, tc.id, tc.ov); err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
	}
}
