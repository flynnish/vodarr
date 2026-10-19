package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vodarr/vodarr/internal/config"
	"github.com/vodarr/vodarr/internal/index"
	"github.com/vodarr/vodarr/internal/strm"
	"github.com/vodarr/vodarr/internal/xtream"
)

// fakeProvider serves one VOD stream and one series, both with ID 10, so
// two providers collide on every ID unless kept apart. down makes it fail.
func fakeProvider(t *testing.T, movie, show, group string, down bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		switch r.URL.Query().Get("action") {
		case "get_vod_categories", "get_series_categories":
			json.NewEncoder(w).Encode([]map[string]any{{"category_id": "1", "category_name": group}})
		case "get_vod_streams":
			json.NewEncoder(w).Encode([]map[string]any{{"stream_id": 10, "name": movie, "category_id": "1", "container_extension": "mkv"}})
		case "get_series":
			json.NewEncoder(w).Encode([]map[string]any{{"series_id": 10, "name": show, "category_id": "1"}})
		case "get_series_info":
			json.NewEncoder(w).Encode(map[string]any{"episodes": map[string]any{"1": []map[string]any{
				{"id": "500", "episode_num": 1, "title": "Pilot", "container_extension": "mkv"}}}})
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func twoProviderScheduler(t *testing.T, primary, extra *httptest.Server, excluded []string) *Scheduler {
	t.Helper()
	out := t.TempDir()
	cfg := &config.Config{}
	cfg.Sync.GraceCycles = 3
	cfg.Sync.Parallelism = 1
	cfg.Sync.ExcludedCategories = excluded
	cfg.Output.Path = out
	sched := NewScheduler(cfg, xtream.NewClient(primary.URL, "u", "p"), nil, index.New(), strm.NewWriter(out, "movies", "tv"))
	sched.SetProviders([]string{"provb"}, []string{"ProviderB"}, []*xtream.Client{xtream.NewClient(extra.URL, "u2", "p2")})
	return sched
}

func TestSyncKeepsProvidersApart(t *testing.T) {
	sched := twoProviderScheduler(t,
		fakeProvider(t, "Inception", "Scrubs", "EN | Movies", false),
		fakeProvider(t, "Dune", "Suits", "DE | Filme", false), nil)
	if err := sched.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}

	movies, series := sched.idx.Counts()
	if movies != 2 || series != 2 {
		t.Fatalf("got %d movies / %d series, want 2 / 2 (one of each per provider)", movies, series)
	}
	a := sched.idx.Lookup("", 10, "movie")
	b := sched.idx.Lookup("provb", 10, "movie")
	if a == nil || a.Name != "Inception" || a.Provider != "" || a.ProviderName != "" {
		t.Errorf("primary movie 10 = %+v", a)
	}
	if b == nil || b.Name != "Dune" || b.Provider != "provb" || b.ProviderName != "ProviderB" {
		t.Errorf("extra movie 10 = %+v", b)
	}
	// Group keys are per provider: both are category 1.
	if a != nil && b != nil && (a.CategoryKey != "vod:1" || b.CategoryKey != "provb|vod:1") {
		t.Errorf("group keys %q / %q, want vod:1 / provb|vod:1", a.CategoryKey, b.CategoryKey)
	}
	if s := sched.idx.Lookup("provb", 10, "series"); s == nil || s.Name != "Suits" || len(s.Episodes) != 1 {
		t.Errorf("extra series 10 = %+v", s)
	}
}

func TestExcludingAnExtraProvidersGroup(t *testing.T) {
	// Excluding provider B's group 1 must not touch the primary's group 1.
	sched := twoProviderScheduler(t,
		fakeProvider(t, "Inception", "Scrubs", "EN | Movies", false),
		fakeProvider(t, "Dune", "Suits", "ES | Peliculas", false), []string{"provb|vod:1"})
	if err := sched.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if sched.idx.Lookup("provb", 10, "movie") != nil {
		t.Error("excluded extra-provider group was indexed")
	}
	if sched.idx.Lookup("", 10, "movie") == nil || sched.idx.Lookup("provb", 10, "series") == nil {
		t.Error("excluding provider B's VOD group removed other items")
	}
}

func TestOneProviderDownKeepsTheOthers(t *testing.T) {
	ctx := context.Background()
	extra := fakeProvider(t, "Dune", "Suits", "DE | Filme", false)
	sched := twoProviderScheduler(t, fakeProvider(t, "Inception", "Scrubs", "EN | Movies", false), extra, nil)
	if err := sched.Sync(ctx); err != nil {
		t.Fatalf("sync 1: %v", err)
	}

	// Provider B goes down: the primary still syncs, B's titles are kept by
	// the grace period instead of disappearing.
	sched.extra[0].client = xtream.NewClient(fakeProvider(t, "", "", "", true).URL, "u2", "p2")
	if err := sched.Sync(ctx); err != nil {
		t.Fatalf("sync 2 should succeed with one provider down: %v", err)
	}
	b := sched.idx.Lookup("provb", 10, "movie")
	if b == nil || b.MissingSince == 0 {
		t.Errorf("provider B's movie should be grace-retained, got %+v", b)
	}
	if a := sched.idx.Lookup("", 10, "movie"); a == nil || a.MissingSince != 0 {
		t.Errorf("primary movie should be fresh, got %+v", a)
	}
}

func TestManualMatchOnExtraProvider(t *testing.T) {
	sched := movieScheduler(nil)
	sched.idx = index.New()
	sched.idx.Replace([]*index.Item{
		{Type: index.TypeMovie, XtreamID: 10, Name: "Primary Ten"},
		{Type: index.TypeMovie, XtreamID: 10, Name: "Extra Ten", Provider: "provb"},
	})
	got, err := sched.SetMatchFor(context.Background(), "provb", index.TypeMovie, 10, MatchOverride{TMDBId: "27205"})
	if err != nil {
		t.Fatalf("SetMatchFor: %v", err)
	}
	if got.Name != "Extra Ten" || !got.ManualMatch {
		t.Errorf("matched %+v, want the extra provider's item", got)
	}
	if p := sched.idx.Lookup("", 10, "movie"); p.ManualMatch {
		t.Error("the primary's item with the same ID was matched too")
	}
	if _, ok := sched.override("provb|movie:10"); !ok {
		t.Error("override not stored under the extra provider's key")
	}
}
