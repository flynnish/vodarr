package newznab

import (
	"strings"
	"testing"

	"github.com/vodarr/vodarr/internal/index"
)

func TestCategoryLanguage(t *testing.T) {
	cases := map[string]string{
		"ES | Películas":       "es",
		"|DE| Filme HD":        "de",
		"[FR] Séries":          "fr",
		"NL - Films":           "nl",
		"EN ▸ Series":          "en",
		"Spanish Movies":       "es",
		"Películas Español":    "es",
		"Deutsch Serien":       "de",
		"Nederlands gesproken": "nl",
		"4K | Movies":          "", // not a language
		"NF | Netflix":         "",
		"VIP | Series":         "",
		"Documentaries":        "",
		"":                     "",
	}
	for cat, want := range cases {
		if got := categoryLanguage(cat); got != want {
			t.Errorf("categoryLanguage(%q) = %q, want %q", cat, got, want)
		}
	}
}

func TestItemLanguagePrefersNameCode(t *testing.T) {
	// A code in the stream's own name wins over its group.
	it := &index.Item{Name: "┃NL┃ Show", Category: "ES | Series"}
	if got := itemLanguage(it); got != "nl" {
		t.Errorf("itemLanguage = %q, want nl", got)
	}
	it = &index.Item{Name: "Show", Category: "ES | Series"}
	if got := itemLanguage(it); got != "es" {
		t.Errorf("itemLanguage = %q, want es", got)
	}
}

func TestReleaseGroup(t *testing.T) {
	cases := map[string]string{
		"ES | Películas":                  "ESPeliculas",
		"EN | Netflix 4K":                 "ENNetflix4K",
		"[DE] Filme & Serien":             "DEFilmeSerien",
		"┃NL┃ Kids":                       "NLKids",
		"":                                "",
		"|||":                             "",
		"A very long provider group name": "AVeryLongProviderGroupName",
		"An extremely long provider group name that keeps going": "AnExtremelyLongProviderGroupNa",
	}
	for cat, want := range cases {
		if got := releaseGroup(cat); got != want {
			t.Errorf("releaseGroup(%q) = %q, want %q", cat, got, want)
		}
	}
}

func TestReleaseTitlesCarryGroupAndLanguage(t *testing.T) {
	idx := index.New()
	idx.Replace([]*index.Item{
		{Type: index.TypeSeries, XtreamID: 1, Name: "Scrubs", CanonicalName: "Scrubs", TVDBId: "465690",
			Year: "2026", Category: "ES | Series",
			Episodes: []index.EpisodeItem{{EpisodeID: 10, Season: 1, EpisodeNum: 1, Ext: "mp4"}}},
		{Type: index.TypeSeries, XtreamID: 2, Name: "Scrubs", CanonicalName: "Scrubs", TVDBId: "465690",
			Year: "2026", Category: "EN | Series",
			Episodes: []index.EpisodeItem{{EpisodeID: 20, Season: 1, EpisodeNum: 1, Ext: "mp4"}}},
		{Type: index.TypeMovie, XtreamID: 3, Name: "Inception", Year: "2010", IMDBId: "tt1375666",
			Category: "Películas Español", ContainerExt: "mkv"},
	})
	h := NewHandler(idx, "", "http://vodarr:9091", noopURLBuilder{})

	tv := getBody(t, h, "/api?t=tvsearch&tvdbid=465690")
	for _, want := range []string{
		"<title>Scrubs.2026.S01E01.SPANISH.WEB-DL-ESSeries.mp4</title>",
		"<title>Scrubs.2026.S01E01.WEB-DL-ENSeries.mp4</title>", // English needs no token
		`name="language" value="es"`,
		`name="language" value="en"`,
	} {
		if !strings.Contains(tv, want) {
			t.Errorf("tv feed missing %s:\n%s", want, tv)
		}
	}

	movie := getBody(t, h, "/api?t=movie&imdbid=tt1375666")
	if want := "<title>Inception.2010.SPANISH.WEB-DL-PeliculasEspanol.mkv</title>"; !strings.Contains(movie, want) {
		t.Errorf("movie feed missing %s:\n%s", want, movie)
	}
}
