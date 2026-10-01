package qbit

import (
	"encoding/json"
	"net/http/httptest"
	"sort"
	"testing"
)

func listHashes(t *testing.T, h *Handler, query string) []string {
	t.Helper()
	w := httptest.NewRecorder()
	h.handleTorrentsInfo(w, httptest.NewRequest("GET", "/api/v2/torrents/info"+query, nil))
	var out []struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	var hashes []string
	for _, o := range out {
		hashes = append(hashes, o.Hash)
	}
	sort.Strings(hashes)
	return hashes
}

func TestTorrentsInfoFiltersByCategory(t *testing.T) {
	h := makeQbitHandler(t.TempDir())
	h.store.Add(&Torrent{Hash: "ep1", Category: "vodarr-tv"})
	h.store.Add(&Torrent{Hash: "ep2", Category: "vodarr-tv"})
	h.store.Add(&Torrent{Hash: "mov", Category: "vodarr-movies"})
	h.store.Add(&Torrent{Hash: "none"})

	cases := map[string][]string{
		"?category=vodarr-tv":     {"ep1", "ep2"}, // Sonarr
		"?category=vodarr-movies": {"mov"},        // Radarr
		"?category=":              {"none"},       // uncategorised only
		"":                        {"ep1", "ep2", "mov", "none"},
	}
	for q, want := range cases {
		got := listHashes(t, h, q)
		if len(got) != len(want) {
			t.Errorf("%q: got %v, want %v", q, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%q: got %v, want %v", q, got, want)
				break
			}
		}
	}
}
