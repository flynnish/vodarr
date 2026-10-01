package qbit

import "testing"

func TestTorrentNameCarriesMovieYear(t *testing.T) {
	cases := []struct {
		desc itemDescriptor
		want string
	}{
		{itemDescriptor{Type: "movie", Name: "Scary Movie", Year: "2000"}, "Scary Movie (2000)"},
		{itemDescriptor{Type: "movie", Name: "The Eagle", Year: "2011"}, "The Eagle (2011)"},
		{itemDescriptor{Type: "movie", Name: "Blade Runner 2049", Year: "2017"}, "Blade Runner 2049 (2017)"},
		{itemDescriptor{Type: "movie", Name: "Scary Movie 2000", Year: "2000"}, "Scary Movie 2000"}, // year already in name
		{itemDescriptor{Type: "movie", Name: "Unknown Year"}, "Unknown Year"},
		{itemDescriptor{Type: "series", Name: "Scrubs", Year: "2001"}, "Scrubs"},
	}
	for _, c := range cases {
		if got := torrentName(c.desc); got != c.want {
			t.Errorf("torrentName(%+v) = %q, want %q", c.desc, got, c.want)
		}
	}
}
