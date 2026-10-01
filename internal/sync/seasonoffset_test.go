package sync

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vodarr/vodarr/internal/index"
)

func seasons(eps []index.EpisodeItem) []int {
	out := make([]int, len(eps))
	for i, ep := range eps {
		out[i] = ep.Season
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSeasonOffsetRenumbersToTVDB(t *testing.T) {
	ctx := context.Background()
	gbbo := tvShow{id: 87012, name: "The Great British Bake Off", aired: "2010-08-17", seasons: 16, tvdbID: 184871}
	sched := movieScheduler(newTVMock(t, nil, []tvShow{gbbo}))
	sched.idx = index.New()
	sched.cachePath = filepath.Join(t.TempDir(), ".vodarr-cache.json")
	if err := sched.LoadOverrides(filepath.Join(t.TempDir(), "matches.json")); err != nil {
		t.Fatal(err)
	}

	// The provider only carries the Channel 4 years, numbered 1-9, plus a special.
	providerEps := []index.EpisodeItem{
		{EpisodeID: 1, Season: 0, EpisodeNum: 1},
		{EpisodeID: 2, Season: 1, EpisodeNum: 1},
		{EpisodeID: 3, Season: 9, EpisodeNum: 10},
	}
	item := &index.Item{Type: index.TypeSeries, XtreamID: 50, Name: "Great British Bake Off", Episodes: providerEps}
	sched.idx.Replace([]*index.Item{item})

	got, err := sched.SetMatch(ctx, index.TypeSeries, 50, MatchOverride{TMDBId: "87012", SeasonOffset: 7})
	if err != nil {
		t.Fatalf("SetMatch: %v", err)
	}
	if want := []int{0, 8, 16}; !equalInts(seasons(got.Episodes), want) {
		t.Errorf("seasons = %v, want %v (specials stay 0)", seasons(got.Episodes), want)
	}
	if got.TVDBId != "184871" {
		t.Errorf("TVDB = %q, want 184871", got.TVDBId)
	}
	// The provider's episode list is shared state: it must not be edited.
	if want := []int{0, 1, 9}; !equalInts(seasons(providerEps), want) {
		t.Errorf("provider episodes changed in place: %v", seasons(providerEps))
	}

	// Changing the offset works from the provider's numbering, not on top.
	got, err = sched.SetMatch(ctx, index.TypeSeries, 50, MatchOverride{TMDBId: "87012", SeasonOffset: 6})
	if err != nil {
		t.Fatalf("SetMatch 6: %v", err)
	}
	if want := []int{0, 7, 15}; !equalInts(seasons(got.Episodes), want) {
		t.Errorf("after changing offset: %v, want %v", seasons(got.Episodes), want)
	}

	// A sync rebuilds the item from the provider; the offset is re-applied once.
	fresh := &index.Item{Type: index.TypeSeries, XtreamID: 50, Name: item.Name, Episodes: providerEps}
	out, _ := sched.enrich(ctx, []*index.Item{fresh}, nil)
	if want := []int{0, 7, 15}; !equalInts(seasons(out[0].Episodes), want) {
		t.Errorf("after sync: %v, want %v", seasons(out[0].Episodes), want)
	}

	// Reset restores the provider's numbering.
	got, err = sched.ClearMatch(ctx, index.TypeSeries, 50)
	if err != nil {
		t.Fatalf("ClearMatch: %v", err)
	}
	if want := []int{0, 1, 9}; !equalInts(seasons(got.Episodes), want) || got.SeasonOffset != 0 {
		t.Errorf("after reset: %v offset %d, want %v offset 0", seasons(got.Episodes), got.SeasonOffset, want)
	}
}

func TestSeasonOffsetValidation(t *testing.T) {
	sched := movieScheduler(nil)
	sched.idx = index.New()
	sched.idx.Replace([]*index.Item{
		{Type: index.TypeSeries, XtreamID: 1, Name: "Show", Episodes: []index.EpisodeItem{{EpisodeID: 1, Season: 2, EpisodeNum: 1}}},
		{Type: index.TypeMovie, XtreamID: 2, Name: "Film"},
	})
	ctx := context.Background()

	if _, err := sched.SetMatch(ctx, index.TypeSeries, 1, MatchOverride{TVDBId: "5", SeasonOffset: -2}); err == nil {
		t.Error("offset moving season 2 to 0 should be rejected")
	}
	if _, err := sched.SetMatch(ctx, index.TypeSeries, 1, MatchOverride{TVDBId: "5", SeasonOffset: 500}); err == nil {
		t.Error("out-of-range offset should be rejected")
	}
	// An offset on a movie is meaningless and dropped, not an error.
	got, err := sched.SetMatch(ctx, index.TypeMovie, 2, MatchOverride{TMDBId: "9", SeasonOffset: 3})
	if err != nil {
		t.Fatalf("movie with offset: %v", err)
	}
	if got.SeasonOffset != 0 {
		t.Errorf("movie SeasonOffset = %d, want 0", got.SeasonOffset)
	}
}
