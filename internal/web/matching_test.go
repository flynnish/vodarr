package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vodarr/vodarr/internal/config"
)

func TestPutCategoriesSavesConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yml")
	h := makeHandler(minimalCfg(), cfgPath)

	body := `{"excluded":["vod:2","series:7","vod:2"]}`
	w := httptest.NewRecorder()
	h.handlePutCategories(w, httptest.NewRequest("PUT", "/api/categories", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}

	if got := h.cfg.Sync.ExcludedCategories; len(got) != 2 || got[0] != "series:7" || got[1] != "vod:2" {
		t.Errorf("ExcludedCategories = %v, want [series:7 vod:2]", got)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config not saved: %v", err)
	}
	if !bytes.Contains(data, []byte("excluded_categories")) {
		t.Errorf("saved config lacks excluded_categories:\n%s", data)
	}
}

func TestPutCategoriesRejectsBadKey(t *testing.T) {
	h := makeHandler(minimalCfg(), filepath.Join(t.TempDir(), "config.yml"))
	w := httptest.NewRecorder()
	h.handlePutCategories(w, httptest.NewRequest("PUT", "/api/categories", strings.NewReader(`{"excluded":["live:3"]}`)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestMatchEndpointsValidate(t *testing.T) {
	h := makeHandler(minimalCfg(), "")

	w := httptest.NewRecorder()
	h.handlePutMatch(w, httptest.NewRequest("PUT", "/api/match", strings.NewReader(`{"type":"music","xtream_id":1,"tmdb_id":"5"}`)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad type: status = %d, want 400", w.Code)
	}

	w = httptest.NewRecorder()
	h.handleDeleteMatch(w, httptest.NewRequest("DELETE", "/api/match?type=movie", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing id: status = %d, want 400", w.Code)
	}

	w = httptest.NewRecorder()
	h.handleMatchSearch(w, httptest.NewRequest("GET", "/api/match/search?type=movie", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing q: status = %d, want 400", w.Code)
	}
}

func TestFixDownloadClientUpdatesExistingClient(t *testing.T) {
	var put map[string]interface{}
	arr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/downloadclient/4" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPut {
			json.NewDecoder(r.Body).Decode(&put)
			w.Write([]byte(`{}`))
			return
		}
		w.Write([]byte(`{"id":4,"name":"VODarr","priority":3,"removeCompletedDownloads":false,"removeFailedDownloads":true,
			"fields":[{"name":"host","value":"vodarr"},{"name":"tvCategory","value":""}]}`))
	}))
	defer arr.Close()
	do := func(method, url string, body []byte) (*http.Response, error) {
		req, _ := http.NewRequest(method, url, bytes.NewReader(body))
		return http.DefaultClient.Do(req)
	}

	res := fixDownloadClient(do, arr.URL, 4, "sonarr")
	if res["success"] != true {
		t.Fatalf("result = %v", res)
	}
	if put["removeCompletedDownloads"] != true || put["removeFailedDownloads"] != true {
		t.Errorf("remove flags not set: %v", put)
	}
	if put["priority"] != float64(3) || put["name"] != "VODarr" {
		t.Errorf("other settings changed: %v", put)
	}
	fields := put["fields"].([]interface{})
	if cat := fields[1].(map[string]interface{})["value"]; cat != "vodarr-tv" {
		t.Errorf("tvCategory = %v, want vodarr-tv", cat)
	}
	if host := fields[0].(map[string]interface{})["value"]; host != "vodarr" {
		t.Errorf("host changed to %v", host)
	}
}

func TestFixDownloadClientLeavesGoodClientAlone(t *testing.T) {
	puts := 0
	arr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts++
		}
		w.Write([]byte(`{"id":4,"removeCompletedDownloads":true,"removeFailedDownloads":true,
			"fields":[{"name":"movieCategory","value":"radarr"}]}`))
	}))
	defer arr.Close()
	do := func(method, url string, body []byte) (*http.Response, error) {
		req, _ := http.NewRequest(method, url, bytes.NewReader(body))
		return http.DefaultClient.Do(req)
	}
	if res := fixDownloadClient(do, arr.URL, 4, "radarr"); res["skipped"] == nil || puts != 0 {
		t.Errorf("result %v, PUTs %d; want skipped and no PUT (an existing category is kept)", res, puts)
	}
}

func TestProviderOf(t *testing.T) {
	providers := []config.Provider{
		{ID: "", URL: "http://primary.example.com", Username: "alice"},
		{ID: "provb", URL: "http://provb.example.com:8080", Username: "bob"},
		{ID: "provc", URL: "http://shared.example.com", Username: "carol"},
		{ID: "provd", URL: "http://shared.example.com", Username: "dave"},
	}
	cases := []struct {
		host, user string
		want       string
		ok         bool
	}{
		{"primary.example.com", "alice", "", true},
		{"provb.example.com:8080", "bob", "provb", true},
		{"provb.example.com:8080", "oldname", "provb", true}, // login changed: the only one on that host
		{"shared.example.com", "dave", "provd", true},
		{"shared.example.com", "eve", "", false}, // two providers there, neither account
		{"unknown.example.com", "x", "", false},
	}
	for _, c := range cases {
		got, ok := providerOf(providers, c.host, c.user)
		if got != c.want || ok != c.ok {
			t.Errorf("providerOf(%s, %s) = %q, %v; want %q, %v", c.host, c.user, got, ok, c.want, c.ok)
		}
	}
}

func TestMatchDetailsFromProvider(t *testing.T) {
	prov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("action") {
		case "get_series_info":
			w.Write([]byte(`{"info":{"name":"1923","plot":"The Duttons","cast":"Harrison Ford, Helen Mirren","cover":"http://img/1923.jpg","tmdb_id":"157744"},
				"episodes":{"2":[{"id":"9","episode_num":1,"title":"S2 opener"}],"1":[{"id":"2","episode_num":2,"title":"B"},{"id":"1","episode_num":1,"title":"A","info":{"plot":"First"}}]}}`))
		case "get_vod_info":
			w.Write([]byte(`{"info":{"plot":"A film","movie_image":"http://img/f.jpg"},"movie_data":{"stream_id":5,"name":"1985"}}`))
		default:
			http.Error(w, "unknown", http.StatusBadRequest)
		}
	}))
	defer prov.Close()
	cfg := minimalCfg()
	cfg.Xtream.URL = prov.URL
	h := makeHandler(cfg, "")

	get := func(query string) (int, providerDetails) {
		w := httptest.NewRecorder()
		h.handleMatchDetails(w, httptest.NewRequest("GET", "/api/match/details?"+query, nil))
		var d providerDetails
		json.Unmarshal(w.Body.Bytes(), &d)
		return w.Code, d
	}

	code, d := get("type=series&xtream_id=7")
	if code != http.StatusOK || d.Cast != "Harrison Ford, Helen Mirren" || d.Poster != "http://img/1923.jpg" || d.TMDBId != 157744 {
		t.Fatalf("series details: %d %+v", code, d)
	}
	if len(d.Episodes) != 3 || d.Episodes[0].Title != "A" || d.Episodes[0].Plot != "First" || d.Episodes[2].Season != 2 {
		t.Errorf("episodes not in season/episode order: %+v", d.Episodes)
	}

	if code, d = get("type=movie&xtream_id=5"); code != http.StatusOK || d.Name != "1985" || d.Poster != "http://img/f.jpg" {
		t.Errorf("movie details: %d %+v", code, d)
	}
	if code, _ = get("type=movie&xtream_id=5&provider=nope"); code != http.StatusNotFound {
		t.Errorf("unknown provider: status %d, want 404", code)
	}
}
