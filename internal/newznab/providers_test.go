package newznab

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vodarr/vodarr/internal/index"
)

func TestExtraProviderReleases(t *testing.T) {
	// The same show from two providers, both with series ID 10.
	ep := []index.EpisodeItem{{EpisodeID: 500, Season: 1, EpisodeNum: 1, Ext: "mkv"}}
	idx := index.New()
	idx.Replace([]*index.Item{
		{Type: index.TypeSeries, XtreamID: 10, Name: "Suits", CanonicalName: "Suits", TVDBId: "247808",
			Category: "Series Drama", Episodes: ep},
		{Type: index.TypeSeries, XtreamID: 10, Name: "Suits", CanonicalName: "Suits", TVDBId: "247808",
			Category: "Series Drama", Provider: "provb", ProviderName: "ProviderB", Episodes: ep},
	})
	h := NewHandler(idx, "", "http://vodarr:9091", noopURLBuilder{})
	body := getBody(t, h, "/api?t=tvsearch&tvdbid=247808")

	for _, want := range []string{
		"<title>Suits.S01E01.WEB-DL-SeriesDrama.mkv</title>",          // primary: unchanged
		"<title>Suits.S01E01.WEB-DL-ProviderBSeriesDrama.mkv</title>", // extra: provider in the group
		"type=series&amp;episode_id=500&amp;provider=provb",           // download link names it
		"vodarr-ep-provb-10-1-1",                                      // separate GUID
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in\n%s", want, body)
		}
	}

	// The grab resolves to the extra provider's item.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api?t=get&id=10&type=series&episode_id=500&provider=provb", nil))
	if desc := descriptorFrom(t, w.Body.Bytes()); desc["provider"] != "provb" {
		t.Errorf("descriptor provider = %v, want provb", desc["provider"])
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api?t=get&id=10&type=series&episode_id=500", nil))
	if desc := descriptorFrom(t, w.Body.Bytes()); desc["provider"] != nil {
		t.Errorf("primary descriptor should name no provider, got %v", desc["provider"])
	}
}
