package arr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/vodarr/vodarr/internal/config"
	"github.com/vodarr/vodarr/internal/index"
)

// fakeArr records the search commands it receives.
type fakeArr struct {
	mu       sync.Mutex
	commands []map[string]any
	apiKeys  []string
}

func (f *fakeArr) record(r *http.Request) {
	var cmd map[string]any
	json.NewDecoder(r.Body).Decode(&cmd)
	f.mu.Lock()
	f.commands = append(f.commands, cmd)
	f.mu.Unlock()
}

func (f *fakeArr) ids(field string) []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int
	for _, c := range f.commands {
		for _, v := range c[field].([]any) {
			out = append(out, int(v.(float64)))
		}
	}
	return out
}

func newSonarr(t *testing.T, missing []map[string]any) (*httptest.Server, *fakeArr) {
	f := &fakeArr{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.apiKeys = append(f.apiKeys, r.Header.Get("X-Api-Key"))
		f.mu.Unlock()
		switch r.URL.Path {
		case "/api/v3/wanted/missing":
			json.NewEncoder(w).Encode(map[string]any{"totalRecords": len(missing), "records": missing})
		case "/api/v3/command":
			f.record(r)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

func episode(id, tvdb, season, ep int) map[string]any {
	return map[string]any{"id": id, "seasonNumber": season, "episodeNumber": ep,
		"series": map[string]any{"tvdbId": tvdb}}
}

func catalog() *index.Index {
	idx := index.New()
	idx.Replace([]*index.Item{
		{Type: index.TypeSeries, XtreamID: 1, Name: "Scrubs", TVDBId: "76156", Episodes: []index.EpisodeItem{
			{EpisodeID: 1, Season: 1, EpisodeNum: 1}, {EpisodeID: 2, Season: 1, EpisodeNum: 2},
		}},
		{Type: index.TypeMovie, XtreamID: 2, Name: "The Lion King", TMDBId: "8587", IMDBId: "tt0110357"},
		{Type: index.TypeMovie, XtreamID: 3, Name: "Dune", IMDBId: "tt1160419"},
	})
	return idx
}

func TestSonarrSearchesOnlyWhatVODarrHas(t *testing.T) {
	srv, f := newSonarr(t, []map[string]any{
		episode(101, 76156, 1, 1), // in VODarr
		episode(102, 76156, 1, 2), // in VODarr
		episode(103, 76156, 1, 3), // series known, episode not on provider
		episode(104, 999, 1, 1),   // series not in VODarr
	})
	inst := config.ArrInstance{Name: "Sonarr", Type: "sonarr", URL: srv.URL + "/", APIKey: "k"}

	res := NewSearcher().Run(context.Background(), []config.ArrInstance{inst}, catalog())

	if len(res) != 1 || res[0].Wanted != 4 || res[0].Searched != 2 {
		t.Fatalf("result = %+v, want wanted 4 / searched 2", res)
	}
	got := f.ids("episodeIds")
	if len(got) != 2 || got[0] != 101 || got[1] != 102 {
		t.Errorf("searched episode IDs = %v, want [101 102]", got)
	}
	if f.commands[0]["name"] != "EpisodeSearch" {
		t.Errorf("command = %v, want EpisodeSearch", f.commands[0]["name"])
	}
	for _, k := range f.apiKeys {
		if k != "k" {
			t.Errorf("API key header = %q, want k", k)
		}
	}
}

func TestCooldownStopsRepeatSearches(t *testing.T) {
	srv, f := newSonarr(t, []map[string]any{episode(101, 76156, 1, 1)})
	inst := config.ArrInstance{Name: "Sonarr", Type: "sonarr", URL: srv.URL, APIKey: "k"}
	s := NewSearcher()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }

	s.Run(context.Background(), []config.ArrInstance{inst}, catalog())
	res := s.Run(context.Background(), []config.ArrInstance{inst}, catalog())
	if res[0].Searched != 0 || res[0].Skipped != 1 {
		t.Errorf("second run = %+v, want searched 0 / skipped 1", res[0])
	}
	if len(f.commands) != 1 {
		t.Errorf("commands sent = %d, want 1", len(f.commands))
	}

	now = now.Add(cooldown + time.Minute)
	if res = s.Run(context.Background(), []config.ArrInstance{inst}, catalog()); res[0].Searched != 1 {
		t.Errorf("after cooldown = %+v, want searched 1", res[0])
	}
}

func TestNothingWantedSendsNoCommand(t *testing.T) {
	srv, f := newSonarr(t, []map[string]any{episode(104, 999, 1, 1)})
	inst := config.ArrInstance{Name: "Sonarr", Type: "sonarr", URL: srv.URL, APIKey: "k"}
	NewSearcher().Run(context.Background(), []config.ArrInstance{inst}, catalog())
	if len(f.commands) != 0 {
		t.Errorf("sent %d commands, want none", len(f.commands))
	}
}

func TestRadarrSearchesMatchedMovies(t *testing.T) {
	f := &fakeArr{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/wanted/missing":
			json.NewEncoder(w).Encode(map[string]any{"totalRecords": 4, "records": []map[string]any{
				{"id": 1, "tmdbId": 8587, "isAvailable": true},                          // by TMDB
				{"id": 2, "tmdbId": 438631, "imdbId": "tt1160419", "isAvailable": true}, // by IMDB
				{"id": 3, "tmdbId": 5, "isAvailable": true},                             // not in VODarr
				{"id": 4, "tmdbId": 8587, "isAvailable": false},                         // not released yet
			}})
		case "/api/v3/command":
			f.record(r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	inst := config.ArrInstance{Name: "Radarr", Type: "radarr", URL: srv.URL, APIKey: "k"}

	NewSearcher().Run(context.Background(), []config.ArrInstance{inst}, catalog())

	got := f.ids("movieIds")
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("searched movie IDs = %v, want [1 2]", got)
	}
	if f.commands[0]["name"] != "MoviesSearch" {
		t.Errorf("command = %v, want MoviesSearch", f.commands[0]["name"])
	}
}

func TestRadarrFallsBackToMovieList(t *testing.T) {
	// Older Radarr without wanted/missing: use the full list and filter.
	f := &fakeArr{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/movie":
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "tmdbId": 8587, "monitored": true, "hasFile": false, "isAvailable": true},
				{"id": 2, "tmdbId": 8587, "monitored": true, "hasFile": true, "isAvailable": true},
				{"id": 3, "tmdbId": 8587, "monitored": false, "hasFile": false, "isAvailable": true},
			})
		case "/api/v3/command":
			f.record(r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	inst := config.ArrInstance{Name: "Radarr", Type: "radarr", URL: srv.URL, APIKey: "k"}

	NewSearcher().Run(context.Background(), []config.ArrInstance{inst}, catalog())

	if got := f.ids("movieIds"); len(got) != 1 || got[0] != 1 {
		t.Errorf("searched movie IDs = %v, want [1]", got)
	}
}

func TestUnreachableInstanceDoesNotStopOthers(t *testing.T) {
	srv, f := newSonarr(t, []map[string]any{episode(101, 76156, 1, 1)})
	down := config.ArrInstance{Name: "Down", Type: "radarr", URL: "http://127.0.0.1:1", APIKey: "k"}
	up := config.ArrInstance{Name: "Sonarr", Type: "sonarr", URL: srv.URL, APIKey: "k"}

	res := NewSearcher().Run(context.Background(), []config.ArrInstance{down, up}, catalog())
	if len(res) != 1 || len(f.commands) != 1 {
		t.Errorf("results %+v, commands %d; want the healthy instance to still search", res, len(f.commands))
	}
}
