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
		"German Series":        "de",
		"Series German":        "de",
		"GER | Series":         "de",
		"DEU - Serien":         "de",
		"DE Series":            "de",
		"GER Movies":           "de",
		"Germany Series":       "de",
		"Deutschland Filme":    "de",
		"ENG | Series":         "en",
		"UK Drama":             "en",
		"Series Drama":         "", // names no language: English by default elsewhere
		"NEW Series":           "", // a word, not a known code
		"TOP Movies":           "",
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
		"<title>Scrubs.2026.S01E01.ENGLISH.WEB-DL-ENSeries.mp4</title>",
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

func TestDubbedCopyLabelledByItsGroup(t *testing.T) {
	// One English show in two groups: the original and a German dub. The
	// German group's copy is labelled German; the original's group names no
	// language, so it is left to arr's original-language assumption.
	idx := index.New()
	idx.Replace([]*index.Item{
		{Type: index.TypeSeries, XtreamID: 1, Name: "Suits", CanonicalName: "Suits", TVDBId: "247808",
			Year: "2011", Category: "Series Drama",
			Episodes: []index.EpisodeItem{{EpisodeID: 10, Season: 1, EpisodeNum: 1, Ext: "mkv"}}},
		{Type: index.TypeSeries, XtreamID: 2, Name: "Suits", CanonicalName: "Suits", TVDBId: "247808",
			Year: "2011", Category: "GER | Serien",
			Episodes: []index.EpisodeItem{{EpisodeID: 20, Season: 1, EpisodeNum: 1, Ext: "mkv"}}},
	})
	h := NewHandler(idx, "", "http://vodarr:9091", noopURLBuilder{})
	body := getBody(t, h, "/api?t=tvsearch&tvdbid=247808")
	for _, want := range []string{
		"<title>Suits.2011.S01E01.WEB-DL-SeriesDrama.mkv</title>",
		"<title>Suits.2011.S01E01.GERMAN.WEB-DL-GERSerien.mkv</title>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in\n%s", want, body)
		}
	}
}

func TestDefaultLanguageSetting(t *testing.T) {
	defer SetDefaultLanguage("original")
	plain := &index.Item{Type: index.TypeMovie, XtreamID: 1, Name: "Film", Year: "2020", ContainerExt: "mkv"}
	spanish := &index.Item{Type: index.TypeMovie, XtreamID: 2, Name: "Film", Year: "2020", ContainerExt: "mkv", Category: "ES | Películas"}

	SetDefaultLanguage("de")
	if got := buildTitle(plain); got != "Film.2020.GERMAN.WEB-DL.mkv" {
		t.Errorf("default de: %q", got)
	}
	if got := buildTitle(spanish); got != "Film.2020.SPANISH.WEB-DL-ESPeliculas.mkv" {
		t.Errorf("a named group still wins: %q", got)
	}

	SetDefaultLanguage("en")
	if got := buildTitle(plain); got != "Film.2020.ENGLISH.WEB-DL.mkv" {
		t.Errorf("default en: %q", got)
	}

	SetDefaultLanguage("original")
	if got := buildTitle(plain); got != "Film.2020.WEB-DL.mkv" {
		t.Errorf("default original: %q", got)
	}
	for _, a := range buildMovieRSSItems("http://localhost", []*index.Item{plain})[0].Attrs {
		if a.Name == "language" {
			t.Errorf("default original: unexpected language attr %q", a.Value)
		}
	}
}
