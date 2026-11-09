package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/vodarr/vodarr/internal/config"
	"github.com/vodarr/vodarr/internal/index"
	"github.com/vodarr/vodarr/internal/strm"
	"github.com/vodarr/vodarr/internal/tmdb"
	"github.com/vodarr/vodarr/internal/xtream"
)

// infoTagFixture is a provider whose bulk lists carry no TMDB tags, while
// get_vod_info and get_series_info do (seriesTag can be changed), and a
// TMDB that records every request.
type infoTagFixture struct {
	mu        gosync.Mutex
	seriesTag int
	vodInfos  int
	tmdbPaths []string
}

func (f *infoTagFixture) scheduler(t *testing.T) *Scheduler {
	prov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Query().Get("action") {
		case "get_vod_categories", "get_series_categories":
			json.NewEncoder(w).Encode([]map[string]any{{"category_id": "1", "category_name": "EN"}})
		case "get_vod_streams":
			json.NewEncoder(w).Encode([]map[string]any{{"stream_id": 10, "name": "1985", "category_id": "1", "container_extension": "mkv"}})
		case "get_vod_info":
			f.vodInfos++
			json.NewEncoder(w).Encode(map[string]any{"info": map[string]any{"tmdb_id": "27205"}})
		case "get_series":
			json.NewEncoder(w).Encode([]map[string]any{{"series_id": 20, "name": "1923", "category_id": "1"}})
		case "get_series_info":
			json.NewEncoder(w).Encode(map[string]any{"info": map[string]any{"tmdb_id": f.seriesTag},
				"episodes": map[string]any{"1": []map[string]any{{"id": "500", "episode_num": 1, "container_extension": "mkv"}}}})
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
		}
	}))
	t.Cleanup(prov.Close)

	tm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.tmdbPaths = append(f.tmdbPaths, r.URL.Path)
		f.mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/search/"):
			w.Write([]byte(`{"results":[]}`))
		case strings.HasSuffix(r.URL.Path, "/external_ids") && strings.HasPrefix(r.URL.Path, "/movie/"):
			w.Write([]byte(`{"imdb_id":"tt1375666"}`))
		case strings.HasSuffix(r.URL.Path, "/external_ids"):
			w.Write([]byte(`{"imdb_id":"tt1","tvdb_id":` + strings.Split(r.URL.Path, "/")[2] + `}`))
		default:
			w.Write([]byte(`{"title":"T","name":"T","release_date":"2010-07-16","first_air_date":"2022-12-18"}`))
		}
	}))
	t.Cleanup(tm.Close)
	tc := tmdb.NewClient("testkey")
	tc.SetBaseURL(tm.URL)
	t.Cleanup(tc.Stop)

	out := t.TempDir()
	cfg := &config.Config{}
	cfg.TMDB.APIKey = "testkey"
	cfg.Sync.GraceCycles = 3
	cfg.Sync.Parallelism = 1
	cfg.Output.Path = out
	return NewScheduler(cfg, xtream.NewClient(prov.URL, "u", "p"), tc, index.New(), strm.NewWriter(out, "movies", "tv"))
}

func (f *infoTagFixture) searched() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.tmdbPaths {
		if strings.HasPrefix(p, "/search/") {
			return true
		}
	}
	return false
}

func TestProviderInfoTagsBeatTitleSearch(t *testing.T) {
	ctx := context.Background()
	f := &infoTagFixture{seriesTag: 157744}
	sched := f.scheduler(t)
	if err := sched.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if m := sched.idx.Lookup("", 10, "movie"); m == nil || m.TMDBId != "27205" || m.IMDBId != "tt1375666" {
		t.Errorf("movie = %+v, want get_vod_info's TMDB 27205", m)
	}
	if s := sched.idx.Lookup("", 20, "series"); s == nil || s.TMDBId != "157744" || s.TVDBId != "157744" {
		t.Errorf("series = %+v, want get_series_info's TMDB 157744", s)
	}
	if f.searched() {
		t.Errorf("searched TMDB by title although the provider tagged both: %v", f.tmdbPaths)
	}

	// A second sync reuses the matches: no further get_vod_info call.
	if err := sched.Sync(ctx); err != nil {
		t.Fatalf("sync 2: %v", err)
	}
	if f.vodInfos != 1 {
		t.Errorf("get_vod_info called %d times, want 1", f.vodInfos)
	}
}

func TestChangedProviderTagRematches(t *testing.T) {
	ctx := context.Background()
	f := &infoTagFixture{seriesTag: 157744}
	sched := f.scheduler(t)
	if err := sched.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// The provider corrects its tag; the cached match must follow it. The
	// episode list (and with it the tag) is refetched once it is stale.
	f.seriesTag = 999
	ageSnapshot(t, source{}.snapshotPath(sched.cfg.Output.Path), 72*time.Hour)
	if err := sched.Sync(ctx); err != nil {
		t.Fatalf("sync 2: %v", err)
	}
	if s := sched.idx.Lookup("", 20, "series"); s == nil || s.TMDBId != "999" {
		t.Errorf("series = %+v, want the new tag 999", s)
	}
}
