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

func TestEpisodeTitleCarriesYearOnlyForSharedTitles(t *testing.T) {
	// Two different shows called Scrubs: without the year, Sonarr's scene
	// mapping hands "Scrubs" to the 2001 show and the search for Scrubs (2026)
	// discards the release. Any other show gets no year: Sonarr names it
	// without one, and a provider year that is off would make it unknown.
	ep := func(id int) []index.EpisodeItem {
		return []index.EpisodeItem{{EpisodeID: id, Season: 1, EpisodeNum: 1, Ext: "mkv"}}
	}
	idx := index.New()
	idx.Replace([]*index.Item{
		{Type: index.TypeSeries, XtreamID: 1, Name: "Scrubs", CanonicalName: "Scrubs", TVDBId: "465690",
			ReleaseDate: "2026-02-25", Episodes: ep(10)},
		{Type: index.TypeSeries, XtreamID: 2, Name: "Scrubs", CanonicalName: "Scrubs", TVDBId: "76156",
			Year: "2001", Episodes: ep(20)},
		// Two provider copies of one show are not a shared title.
		{Type: index.TypeSeries, XtreamID: 3, Name: "Normal People", CanonicalName: "Normal People", TVDBId: "366431",
			Year: "2021", Category: "Series Drama", Episodes: ep(30)},
		{Type: index.TypeSeries, XtreamID: 4, Name: "Normal People", CanonicalName: "Normal People", TVDBId: "366431",
			Year: "2020", Category: "Germany Series", Episodes: ep(40)},
	})
	h := NewHandler(idx, "", "http://vodarr:9091", noopURLBuilder{})

	cases := map[string][]string{
		"/api?t=tvsearch&tvdbid=465690": {"<title>Scrubs.2026.S01E01.WEB-DL.mkv</title>"},
		"/api?t=tvsearch&tvdbid=76156":  {"<title>Scrubs.2001.S01E01.WEB-DL.mkv</title>"},
		"/api?t=tvsearch&tvdbid=366431": {
			"<title>Normal.People.S01E01.WEB-DL-SeriesDrama.mkv</title>",
			"<title>Normal.People.S01E01.GERMAN.WEB-DL-GermanySeries.mkv</title>",
		},
	}
	for url, wants := range cases {
		body := getBody(t, h, url)
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s: want %s in\n%s", url, want, body)
			}
		}
	}
}

func TestNameSearchSkipsItemsWithIDs(t *testing.T) {
	// Two shows called Normal People: the one Sonarr has, and a different
	// one the provider carries. A name search must not offer the matched
	// one (Sonarr's ID search already finds it, and rejects the other as
	// unknown series); an unmatched entry is still found by name.
	ep := []index.EpisodeItem{{EpisodeID: 1, Season: 1, EpisodeNum: 12}}
	idx := index.New()
	idx.Replace([]*index.Item{
		{Type: index.TypeSeries, XtreamID: 1, Name: "Normal People", TVDBId: "999001", Episodes: ep},
		{Type: index.TypeSeries, XtreamID: 2, Name: "Normal People Unmatched Copy", Episodes: ep},
		{Type: index.TypeMovie, XtreamID: 3, Name: "Scary Movie", Year: "2000", IMDBId: "tt0175142"},
		{Type: index.TypeMovie, XtreamID: 4, Name: "Scary Movie", Year: "2000"},
	})
	h := NewHandler(idx, "", "http://vodarr:9091", noopURLBuilder{})

	tv := getBody(t, h, "/api?t=tvsearch&q=Normal%20People&season=1&ep=12")
	if strings.Contains(tv, "id=1&amp;") {
		t.Errorf("name search offered the series with a TVDB ID:\n%s", tv)
	}
	if !strings.Contains(tv, "id=2&amp;") {
		t.Errorf("name search lost the unmatched series:\n%s", tv)
	}
	// The ID search still finds the matched one.
	if byID := getBody(t, h, "/api?t=tvsearch&tvdbid=999001"); !strings.Contains(byID, "id=1&amp;") {
		t.Errorf("ID search lost the matched series:\n%s", byID)
	}

	movies := getBody(t, h, "/api?t=movie&q=Scary%20Movie")
	if strings.Contains(movies, "id=3&amp;") || !strings.Contains(movies, "id=4&amp;") {
		t.Errorf("movie name search should return only the unmatched film:\n%s", movies)
	}
}
