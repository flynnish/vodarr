package newznab

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vodarr/vodarr/internal/bencode"
	"github.com/vodarr/vodarr/internal/index"
)

// typeChangedHandler indexes a VOD stream turned into a Storyville episode
// and a provider series turned into a film, both with ID 4242, next to an
// untouched VOD movie that also has ID 4242.
func typeChangedHandler() *Handler {
	idx := index.New()
	idx.Replace([]*index.Item{
		{
			Type: index.TypeSeries, SourceType: index.TypeMovie, XtreamID: 4242,
			Name: "Storyville The Cleaners", CanonicalName: "Storyville", TVDBId: "72125",
			Episodes: []index.EpisodeItem{{EpisodeID: 4242, Season: 2018, EpisodeNum: 12, Ext: "mp4"}},
		},
		{
			Type: index.TypeMovie, SourceType: index.TypeSeries, XtreamID: 4243,
			Name: "Muppets Christmas Special", CanonicalName: "A Muppets Christmas: Letters to Santa",
			Year: "2008", IMDBId: "tt1316541", TMDBId: "777", StreamEpisodeID: 9001, ContainerExt: "mkv",
			Episodes: []index.EpisodeItem{{EpisodeID: 9001, Season: 1, EpisodeNum: 1}},
		},
	})
	return NewHandler(idx, "", "http://vodarr:9091", noopURLBuilder{})
}

func getBody(t *testing.T, h *Handler, url string) string {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", url, w.Code, w.Body)
	}
	return w.Body.String()
}

func descriptorFrom(t *testing.T, torrent []byte) map[string]any {
	t.Helper()
	decoded, err := bencode.Decode(torrent)
	if err != nil {
		t.Fatalf("bencode: %v", err)
	}
	var desc map[string]any
	if err := json.Unmarshal([]byte(decoded.(map[string]any)["comment"].(string)), &desc); err != nil {
		t.Fatalf("descriptor: %v", err)
	}
	return desc
}

func TestVODEpisodeOfferedToSonarr(t *testing.T) {
	h := typeChangedHandler()
	body := getBody(t, h, "/api?t=tvsearch&tvdbid=72125")

	if !strings.Contains(body, "Storyville.S2018E12") {
		t.Errorf("release title should use the series name and episode tag:\n%s", body)
	}
	// The download link identifies the item by its provider type.
	link := "http://vodarr:9091/api?t=get&amp;id=4242&amp;type=movie&amp;episode_id=4242"
	if !strings.Contains(body, link) {
		t.Fatalf("download link %q not in feed:\n%s", link, body)
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api?t=get&id=4242&type=movie&episode_id=4242", nil))
	desc := descriptorFrom(t, w.Body.Bytes())
	if desc["type"] != "series" || desc["source_type"] != "movie" || desc["name"] != "Storyville" {
		t.Errorf("descriptor = %v, want series from movie named Storyville", desc)
	}
}

func TestSeriesFilmOfferedToRadarr(t *testing.T) {
	h := typeChangedHandler()
	body := getBody(t, h, "/api?t=movie&imdbid=tt1316541")

	link := "http://vodarr:9091/api?t=get&amp;id=4243&amp;type=series"
	if !strings.Contains(body, link) {
		t.Fatalf("download link %q not in feed:\n%s", link, body)
	}
	// It is a film now: Sonarr must not be offered it.
	if tv := getBody(t, h, "/api?t=tvsearch&q=Muppets"); strings.Contains(tv, "Muppets") {
		t.Error("converted film still offered as a series")
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api?t=get&id=4243&type=series", nil))
	desc := descriptorFrom(t, w.Body.Bytes())
	if desc["type"] != "movie" || desc["source_type"] != "series" || desc["stream_episode_id"] != float64(9001) {
		t.Errorf("descriptor = %v, want movie from series streaming episode 9001", desc)
	}
}

func TestDescriptorYearMatchesReleaseTitle(t *testing.T) {
	// No Year, only a release date: the release title says 2026, so the
	// descriptor (which names the download Radarr parses) must too.
	idx := index.New()
	idx.Replace([]*index.Item{{
		Type: index.TypeMovie, XtreamID: 77, Name: "Scary Movie",
		ReleaseDate: "2026-06-12", IMDBId: "tt32093575", ContainerExt: "mp4",
	}})
	h := NewHandler(idx, "", "http://vodarr:9091", noopURLBuilder{})

	if body := getBody(t, h, "/api?t=movie&imdbid=tt32093575"); !strings.Contains(body, "Scary.Movie.2026.WEB-DL.mp4") {
		t.Fatalf("release title missing year:\n%s", body)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api?t=get&id=77&type=movie", nil))
	if desc := descriptorFrom(t, w.Body.Bytes()); desc["year"] != "2026" {
		t.Errorf("descriptor year = %v, want 2026", desc["year"])
	}
}

func TestEpisodeTitleCarriesSeriesYear(t *testing.T) {
	// Without the year, Sonarr's scene mapping hands "Scrubs" to the 2001
	// show and the search for Scrubs (2026) discards the release.
	idx := index.New()
	idx.Replace([]*index.Item{
		{Type: index.TypeSeries, XtreamID: 1, Name: "Scrubs", CanonicalName: "Scrubs", TVDBId: "465690",
			ReleaseDate: "2026-02-25", Episodes: []index.EpisodeItem{{EpisodeID: 10, Season: 1, EpisodeNum: 1, Ext: "mp4"}}},
		{Type: index.TypeSeries, XtreamID: 2, Name: "1883", CanonicalName: "1883", TVDBId: "403245",
			Year: "2021", Episodes: []index.EpisodeItem{{EpisodeID: 20, Season: 1, EpisodeNum: 1}}},
		{Type: index.TypeSeries, XtreamID: 3, Name: "No Year Show", TVDBId: "999",
			Episodes: []index.EpisodeItem{{EpisodeID: 30, Season: 1, EpisodeNum: 1}}},
	})
	h := NewHandler(idx, "", "http://vodarr:9091", noopURLBuilder{})

	cases := map[string]string{
		"/api?t=tvsearch&tvdbid=465690": "<title>Scrubs.2026.S01E01.WEB-DL.mp4</title>",
		"/api?t=tvsearch&tvdbid=403245": "<title>1883.2021.S01E01.WEB-DL.mkv</title>",
		"/api?t=tvsearch&tvdbid=999":    "<title>No.Year.Show.S01E01.WEB-DL.mkv</title>",
	}
	for url, want := range cases {
		if body := getBody(t, h, url); !strings.Contains(body, want) {
			t.Errorf("%s: want %s in\n%s", url, want, body)
		}
	}
}
