package qbit

import (
	"testing"

	"github.com/vodarr/vodarr/internal/xtream"
)

func TestStreamURLsFollowProviderType(t *testing.T) {
	h := &Handler{xtream: xtream.NewClient("http://provider.example.com", "u", "p")}

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"plain movie", h.movieStreamURL(itemDescriptor{Type: "movie", XtreamID: 42}, "mkv"),
			"http://provider.example.com/movie/u/p/42.mkv"},
		{"series episode offered as a film", h.movieStreamURL(itemDescriptor{Type: "movie", XtreamID: 4243, SourceType: "series", StreamEpisodeID: 9001}, "mkv"),
			"http://provider.example.com/series/u/p/9001.mkv"},
		{"plain episode", h.episodeStreamURL(itemDescriptor{Type: "series"}, 500, "mp4"),
			"http://provider.example.com/series/u/p/500.mp4"},
		{"VOD stream offered as an episode", h.episodeStreamURL(itemDescriptor{Type: "series", SourceType: "movie"}, 4242, "mp4"),
			"http://provider.example.com/movie/u/p/4242.mp4"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestStreamURLsUseTheDescriptorsProvider(t *testing.T) {
	h := &Handler{xtream: xtream.NewClient("http://primary.example.com", "u", "p")}
	h.SetProviders(map[string]*xtream.Client{"provb": xtream.NewClient("http://provb.example.com", "u2", "p2")})

	if got := h.movieStreamURL(itemDescriptor{Type: "movie", XtreamID: 10, Provider: "provb"}, "mkv"); got != "http://provb.example.com/movie/u2/p2/10.mkv" {
		t.Errorf("extra provider stream = %q", got)
	}
	if got := h.movieStreamURL(itemDescriptor{Type: "movie", XtreamID: 10}, "mkv"); got != "http://primary.example.com/movie/u/p/10.mkv" {
		t.Errorf("primary stream = %q", got)
	}
	// The stream proxy is asked for the descriptor's provider.
	var asked string
	h.SetStrmURL(func(provider, kind string, id int, ext string) string { asked = provider; return "x" })
	h.strmContent("provb", "movie", 10, "mkv")
	if asked != "provb" {
		t.Errorf("proxy asked for provider %q, want provb", asked)
	}
}
