package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	gosync "sync"
	"testing"
	"time"

	"github.com/vodarr/vodarr/internal/config"
	"github.com/vodarr/vodarr/internal/index"
	"github.com/vodarr/vodarr/internal/strm"
	"github.com/vodarr/vodarr/internal/xtream"
)

// episodeProvider serves one series whose last_modified never changes, as
// many providers do when they add episodes. Its episode count and failure
// state can be changed between syncs.
type episodeProvider struct {
	mu       gosync.Mutex
	episodes int
	fail     bool
	fetches  int
}

func (p *episodeProvider) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		switch r.URL.Query().Get("action") {
		case "get_vod_categories", "get_series_categories":
			json.NewEncoder(w).Encode([]map[string]any{{"category_id": "1", "category_name": "EN | Series"}})
		case "get_vod_streams":
			w.Write([]byte(`[]`))
		case "get_series":
			json.NewEncoder(w).Encode([]map[string]any{{"series_id": 10, "name": "Scrubs", "category_id": "1", "last_modified": "1700000000"}})
		case "get_series_info":
			p.fetches++
			if p.fail {
				http.Error(w, "busy", http.StatusBadGateway)
				return
			}
			var eps []map[string]any
			for i := 1; i <= p.episodes; i++ {
				eps = append(eps, map[string]any{"id": 500 + i, "episode_num": i, "title": "Ep", "container_extension": "mkv"})
			}
			json.NewEncoder(w).Encode(map[string]any{"episodes": map[string]any{"1": eps}})
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ageSnapshot moves every series' fetch time back by d.
func ageSnapshot(t *testing.T, path string, d time.Duration) {
	t.Helper()
	snap, err := LoadSnapshot(path)
	if err != nil || snap == nil {
		t.Fatalf("load snapshot: %v", err)
	}
	for id, e := range snap.Series {
		e.FetchedAt = e.FetchedAt.Add(-d)
		snap.Series[id] = e
	}
	if err := SaveSnapshot(path, snap); err != nil {
		t.Fatal(err)
	}
}

func TestNewEpisodesAppearWithoutLastModifiedChange(t *testing.T) {
	ctx := context.Background()
	prov := &episodeProvider{episodes: 2}
	srv := prov.serve(t)
	out := t.TempDir()
	cfg := &config.Config{}
	cfg.Sync.GraceCycles = 3
	cfg.Sync.Parallelism = 1
	cfg.Output.Path = out
	sched := NewScheduler(cfg, xtream.NewClient(srv.URL, "u", "p"), nil, index.New(), strm.NewWriter(out, "movies", "tv"))
	episodes := func() int {
		if it := sched.idx.Lookup("", 10, "series"); it != nil {
			return len(it.Episodes)
		}
		return -1
	}
	sync := func() {
		t.Helper()
		if err := sched.Sync(ctx); err != nil {
			t.Fatalf("sync: %v", err)
		}
	}

	sync()
	if got := episodes(); got != 2 {
		t.Fatalf("first sync: %d episodes, want 2", got)
	}

	// An episode is added; a sync soon after still reuses the cached list.
	prov.episodes = 3
	sync()
	if prov.fetches != 1 || episodes() != 2 {
		t.Fatalf("recent list should be reused: %d fetches, %d episodes", prov.fetches, episodes())
	}

	// Once the cached list is old it is fetched again and the episode shows up.
	snapPath := source{}.snapshotPath(out)
	ageSnapshot(t, snapPath, 48*time.Hour)
	sync()
	if prov.fetches != 2 || episodes() != 3 {
		t.Fatalf("stale list should be refetched: %d fetches, %d episodes, want 2 and 3", prov.fetches, episodes())
	}

	// A refetch that fails keeps the last good list and is retried next sync.
	ageSnapshot(t, snapPath, 48*time.Hour)
	prov.fail = true
	sync()
	if got := episodes(); got != 3 {
		t.Fatalf("failed refetch: %d episodes, want the previous 3", got)
	}
	prov.fail = false
	prov.episodes = 4
	sync()
	if got := episodes(); got != 4 {
		t.Errorf("failed refetch not retried next sync: %d episodes, want 4", got)
	}
}

func TestSeriesStaleSpreadsRefetches(t *testing.T) {
	now := time.Now()
	if seriesStale(5, now.Add(-23*time.Hour), now) {
		t.Error("a list under a day old is stale")
	}
	if !seriesStale(5, now.Add(-49*time.Hour), now) {
		t.Error("a list over two days old is not stale")
	}
	if !seriesStale(5, time.Time{}, now) {
		t.Error("a list with no fetch time (older snapshot) is not stale")
	}
	if seriesStale(0, now.Add(-30*time.Hour), now) == seriesStale(12, now.Add(-30*time.Hour), now) {
		t.Error("refetch times are not spread by ID")
	}
}
