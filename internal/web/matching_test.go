package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
