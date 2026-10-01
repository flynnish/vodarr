package arr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/vodarr/vodarr/internal/config"
)

type call struct {
	Method, Path string
	Body         map[string]any
}

func recordingArr(t *testing.T) (*httptest.Server, func() []call) {
	var mu sync.Mutex
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		calls = append(calls, call{r.Method, r.URL.Path, body})
		mu.Unlock()
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []call {
		mu.Lock()
		defer mu.Unlock()
		return append([]call(nil), calls...)
	}
}

func TestDeliveredSonarrUnmonitorsThenRescans(t *testing.T) {
	srv, calls := recordingArr(t)
	inst := config.ArrInstance{Name: "Sonarr", Type: "sonarr", URL: srv.URL, APIKey: "k"}

	err := NewSearcher().Delivered(context.Background(), inst, Delivery{SeriesID: 7, EpisodeIDs: []int{101, 102}}, true)
	if err != nil {
		t.Fatalf("Delivered: %v", err)
	}
	got := calls()
	if len(got) != 2 {
		t.Fatalf("calls = %+v, want 2", got)
	}
	if got[0].Method != "PUT" || got[0].Path != "/api/v3/episode/monitor" || got[0].Body["monitored"] != false {
		t.Errorf("first call = %+v, want PUT episode/monitor monitored=false", got[0])
	}
	if got[1].Path != "/api/v3/command" || got[1].Body["name"] != "RescanSeries" || got[1].Body["seriesId"] != float64(7) {
		t.Errorf("second call = %+v, want RescanSeries for series 7", got[1])
	}
}

func TestDeliveredRadarrWithoutUnmonitor(t *testing.T) {
	srv, calls := recordingArr(t)
	inst := config.ArrInstance{Name: "Radarr", Type: "radarr", URL: srv.URL, APIKey: "k"}

	if err := NewSearcher().Delivered(context.Background(), inst, Delivery{MovieID: 3}, false); err != nil {
		t.Fatalf("Delivered: %v", err)
	}
	got := calls()
	if len(got) != 1 || got[0].Body["name"] != "RescanMovie" || got[0].Body["movieId"] != float64(3) {
		t.Errorf("calls = %+v, want only RescanMovie for movie 3", got)
	}
}

func TestPickInstance(t *testing.T) {
	sonarrHD := config.ArrInstance{Name: "Sonarr", Type: "sonarr", URL: "http://sonarr:8989/"}
	sonarr4K := config.ArrInstance{Name: "Sonarr 4K", Type: "sonarr", URL: "http://sonarr4k:8989"}
	radarr := config.ArrInstance{Name: "Radarr", Type: "radarr", URL: "http://radarr:7878"}

	if inst, ok := PickInstance([]config.ArrInstance{sonarrHD, radarr}, "radarr", "", ""); !ok || inst.Name != "Radarr" {
		t.Errorf("single radarr: got %+v %v", inst, ok)
	}
	all := []config.ArrInstance{sonarrHD, sonarr4K, radarr}
	if inst, ok := PickInstance(all, "sonarr", "sonarr 4k", ""); !ok || inst.Name != "Sonarr 4K" {
		t.Errorf("by name: got %+v %v", inst, ok)
	}
	if inst, ok := PickInstance(all, "sonarr", "", "http://SONARR:8989"); !ok || inst.Name != "Sonarr" {
		t.Errorf("by URL: got %+v %v", inst, ok)
	}
	if _, ok := PickInstance(all, "sonarr", "Unknown", ""); ok {
		t.Error("ambiguous instance should not be picked")
	}
	if _, ok := PickInstance([]config.ArrInstance{radarr}, "sonarr", "", ""); ok {
		t.Error("no sonarr configured should not pick one")
	}
}
