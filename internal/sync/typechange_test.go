package sync

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vodarr/vodarr/internal/index"
)

// typeChangeScheduler has a TMDB mock for the Storyville series.
func typeChangeScheduler(t *testing.T) *Scheduler {
	t.Helper()
	storyville := tvShow{id: 1405, name: "Storyville", aired: "1997-01-01", seasons: 28, tvdbID: 72125}
	sched := movieScheduler(newTVMock(t, nil, []tvShow{storyville}))
	sched.idx = index.New()
	sched.cachePath = filepath.Join(t.TempDir(), ".vodarr-cache.json")
	if err := sched.LoadOverrides(filepath.Join(t.TempDir(), "matches.json")); err != nil {
		t.Fatal(err)
	}
	return sched
}

func TestMovieBecomesEpisode(t *testing.T) {
	ctx := context.Background()
	sched := typeChangeScheduler(t)
	vod := &index.Item{Type: index.TypeMovie, XtreamID: 4242,
		Name: "Storyville The Internets Dirtiest Secrets The Cleaners", ContainerExt: "mp4", Duration: 5400}
	sched.idx.Replace([]*index.Item{vod})

	got, err := sched.SetMatch(ctx, index.TypeMovie, 4242, MatchOverride{
		TMDBId: "1405", AsType: "series", Season: 2018, Episode: 12,
	})
	if err != nil {
		t.Fatalf("SetMatch: %v", err)
	}
	if got.Type != index.TypeSeries || got.SourceType != index.TypeMovie {
		t.Fatalf("type = %q (source %q), want series from movie", got.Type, got.SourceType)
	}
	if got.TVDBId != "72125" || got.CanonicalName != "Storyville" {
		t.Errorf("TVDB %q / name %q, want 72125 / Storyville", got.TVDBId, got.CanonicalName)
	}
	if len(got.Episodes) != 1 {
		t.Fatalf("episodes = %d, want 1", len(got.Episodes))
	}
	ep := got.Episodes[0]
	if ep.EpisodeID != 4242 || ep.Season != 2018 || ep.EpisodeNum != 12 || ep.Ext != "mp4" {
		t.Errorf("episode = %+v, want stream 4242 as S2018E12 .mp4", ep)
	}

	// Still found by its provider identity, and now by the series' TVDB ID.
	if sched.idx.SearchByXtreamID(4242, "movie") == nil {
		t.Error("item no longer found by its VOD stream ID")
	}
	if hits := sched.idx.SearchByTVDB("72125"); len(hits) != 1 {
		t.Errorf("SearchByTVDB hits = %d, want 1", len(hits))
	}

	// A sync re-creates the item from the provider as a movie; the override
	// must turn it into the episode again.
	fresh := &index.Item{Type: index.TypeMovie, XtreamID: 4242, Name: vod.Name, ContainerExt: "mp4"}
	out, _ := sched.enrich(ctx, []*index.Item{fresh}, nil)
	if out[0].Type != index.TypeSeries || len(out[0].Episodes) != 1 || out[0].Episodes[0].Season != 2018 {
		t.Errorf("after sync: %+v, want the S2018E12 episode", out[0])
	}

	// Reset brings back the provider's movie.
	got, err = sched.ClearMatch(ctx, index.TypeMovie, 4242)
	if err != nil {
		t.Fatalf("ClearMatch: %v", err)
	}
	if got.Type != index.TypeMovie || got.SourceType != "" || got.Episodes != nil {
		t.Errorf("after reset: type %q source %q episodes %d, want a plain movie", got.Type, got.SourceType, len(got.Episodes))
	}
}

func TestSeriesBecomesMovie(t *testing.T) {
	ctx := context.Background()
	special := movieEntry{id: 777, title: "A Muppets Christmas: Letters to Santa", release: "2008-12-17", runtime: 42, imdb: "tt1316541"}
	tc, _ := newMovieMock(t, nil, []movieEntry{special})
	sched := movieScheduler(tc)
	sched.idx = index.New()
	sched.cachePath = filepath.Join(t.TempDir(), ".vodarr-cache.json")

	series := &index.Item{Type: index.TypeSeries, XtreamID: 4242, Name: "Muppets Christmas Special",
		Episodes: []index.EpisodeItem{
			{EpisodeID: 9001, Season: 1, EpisodeNum: 1, Ext: "mkv", Duration: 2520},
			{EpisodeID: 9002, Season: 1, EpisodeNum: 2, Ext: "mp4", Duration: 120}, // a trailer
		}}
	// A VOD stream with the same numeric ID must stay separate.
	vod := &index.Item{Type: index.TypeMovie, XtreamID: 4242, Name: "Unrelated Film"}
	sched.idx.Replace([]*index.Item{series, vod})

	// With two episodes, which one is the film must be chosen.
	if _, err := sched.SetMatch(ctx, index.TypeSeries, 4242, MatchOverride{TMDBId: "777", AsType: "movie"}); err == nil {
		t.Error("expected an error when the film episode is not chosen")
	}

	got, err := sched.SetMatch(ctx, index.TypeSeries, 4242, MatchOverride{TMDBId: "777", AsType: "movie", EpisodeID: 9001})
	if err != nil {
		t.Fatalf("SetMatch: %v", err)
	}
	if got.Type != index.TypeMovie || got.SourceType != index.TypeSeries || got.StreamEpisodeID != 9001 {
		t.Fatalf("got type %q source %q stream %d, want movie from series streaming 9001", got.Type, got.SourceType, got.StreamEpisodeID)
	}
	if got.IMDBId != "tt1316541" || got.Year != "2008" || got.ContainerExt != "mkv" {
		t.Errorf("IMDB %q year %q ext %q, want tt1316541 / 2008 / mkv", got.IMDBId, got.Year, got.ContainerExt)
	}

	// Both items keep their own identity.
	if it := sched.idx.SearchByXtreamID(4242, "series"); it == nil || it.Type != index.TypeMovie {
		t.Error("converted series not found by its series ID")
	}
	if it := sched.idx.SearchByXtreamID(4242, "movie"); it == nil || it.Name != "Unrelated Film" {
		t.Error("VOD stream with the same numeric ID was overwritten")
	}
	if hits := sched.idx.SearchByIMDB("tt1316541"); len(hits) != 1 {
		t.Errorf("SearchByIMDB hits = %d, want 1", len(hits))
	}
}

func TestTypeChangeValidation(t *testing.T) {
	sched := movieScheduler(nil)
	sched.idx = index.New()
	sched.idx.Replace([]*index.Item{{Type: index.TypeMovie, XtreamID: 1, Name: "X"}})
	ctx := context.Background()

	for name, ov := range map[string]MatchOverride{
		"episode without number": {TMDBId: "1405", AsType: "series", Season: 1},
		"unknown type":           {TMDBId: "1405", AsType: "music"},
	} {
		if _, err := sched.SetMatch(ctx, index.TypeMovie, 1, ov); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
