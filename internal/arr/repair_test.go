package arr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/vodarr/vodarr/internal/config"
	"github.com/vodarr/vodarr/internal/strm"
)

func TestRepairImports(t *testing.T) {
	history := []map[string]any{
		{"episodeId": 101, "seriesId": 7, "eventType": "downloadFolderImported",
			"data": map[string]string{"droppedPath": "/data/strm/a.mkv", "importedPath": "/media/tv/strm/A/S01E01.mkv"}},
		{"episodeId": 102, "seriesId": 7, "eventType": "downloadFolderImported",
			"data": map[string]string{"droppedPath": "/data/strm/b.mkv", "importedPath": "/media/tv/strm/A/S01E02.mkv"}},
		// Same file imported twice (e.g. a re-grab): handled once.
		{"episodeId": 102, "seriesId": 7, "eventType": "downloadFolderImported",
			"data": map[string]string{"droppedPath": "/data/strm/b.mkv", "importedPath": "/media/tv/strm/A/S01E02.mkv"}},
		{"episodeId": 201, "seriesId": 8, "eventType": "downloadFolderImported",
			"data": map[string]string{"droppedPath": "/downloads/real.mkv", "importedPath": "/media/tv/B/S01E01.mkv"}},
		{"episodeId": 301, "seriesId": 9, "eventType": "downloadFolderImported",
			"data": map[string]string{"droppedPath": "/data/strm/c.mkv", "importedPath": "/media/tv/strm/C/S01E01.mkv"}},
		{"episodeId": 999, "seriesId": 7, "eventType": "grabbed", "data": map[string]string{}},
	}

	var mu sync.Mutex
	var commands []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/history":
			json.NewEncoder(w).Encode(map[string]any{"totalRecords": len(history), "records": history})
		case "/api/v3/command":
			var c map[string]any
			json.NewDecoder(r.Body).Decode(&c)
			mu.Lock()
			commands = append(commands, c)
			mu.Unlock()
			w.Write([]byte(`{}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	// Fake filesystem outcomes per imported file.
	outcomes := map[string]error{
		"/media/tv/strm/A/S01E01.mkv": nil,
		"/media/tv/strm/A/S01E02.mkv": nil,
		"/media/tv/B/S01E01.mkv":      strm.ErrNotStub,    // a real download, ignored
		"/media/tv/strm/C/S01E01.mkv": strm.ErrNotVisible, // not mounted
	}
	var finished []string
	finish := func(lib, src string) error {
		finished = append(finished, lib)
		return outcomes[lib]
	}

	inst := config.ArrInstance{Name: "Sonarr", Type: "sonarr", URL: srv.URL, APIKey: "k"}
	res := NewSearcher().RepairImports(context.Background(), inst, finish, true)

	if res.Error != "" {
		t.Fatalf("error: %s", res.Error)
	}
	if res.Repaired != 2 || res.NotVisible != 1 || res.Imports != 3 {
		t.Errorf("result = %+v, want 2 repaired, 1 not visible, 3 VODarr imports", res)
	}
	if len(finished) != 4 {
		t.Errorf("finish called for %v, want each imported file once", finished)
	}
	// One rescan for series 7, none for the unrepaired series.
	mu.Lock()
	defer mu.Unlock()
	if len(commands) != 1 || commands[0]["name"] != "RescanSeries" || commands[0]["seriesId"] != float64(7) {
		t.Errorf("commands = %v, want one RescanSeries for series 7", commands)
	}
}
