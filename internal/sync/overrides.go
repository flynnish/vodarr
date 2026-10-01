package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vodarr/vodarr/internal/index"
)

// MatchOverride is a user's manual match for one catalog item. At least one
// ID is set. TVDBId lets a series be matched when TMDB lacks a TVDB link (or
// the show is not on TMDB at all), since Sonarr searches by TVDB ID.
//
// AsType changes what the item is offered as, when the provider filed it
// under the wrong type:
//   - a VOD "movie" that is a TV episode (e.g. one documentary of a series
//     such as Storyville) becomes episode Season x Episode of the TMDB/TVDB
//     series, for Sonarr;
//   - a provider "series" that is a film (e.g. a special) becomes that TMDB
//     movie for Radarr, streaming provider episode EpisodeID.
//
// The IDs then refer to the new type: a TMDB TV ID for an episode, a TMDB
// movie ID for a film.
//
// SeasonOffset renumbers a provider series' seasons to TheTVDB's, which
// Sonarr searches by: a provider carrying only The Great British Bake Off's
// Channel 4 years numbers them 1-9, TheTVDB continues from the BBC years, so
// provider season 1 is season 8 and the offset is 7. Specials (season 0)
// keep their number.
type MatchOverride struct {
	TMDBId string `json:"tmdb_id,omitempty"`
	TVDBId string `json:"tvdb_id,omitempty"`

	AsType       string `json:"as_type,omitempty"`       // "movie" or "series"; "" = provider type
	Season       int    `json:"season,omitempty"`        // movie -> episode
	Episode      int    `json:"episode,omitempty"`       // movie -> episode
	EpisodeID    int    `json:"episode_id,omitempty"`    // series -> movie
	SeasonOffset int    `json:"season_offset,omitempty"` // provider series only
}

// maxSeasonOffset bounds SeasonOffset to a sane range.
const maxSeasonOffset = 100

// shiftSeasons returns a copy of eps with by added to every season but 0.
// It never edits eps in place: the slice can be shared with the cache or
// the live index.
func shiftSeasons(eps []index.EpisodeItem, by int) []index.EpisodeItem {
	if by == 0 || len(eps) == 0 {
		return eps
	}
	out := make([]index.EpisodeItem, len(eps))
	copy(out, eps)
	for i := range out {
		if out[i].Season > 0 {
			out[i].Season += by
		}
	}
	return out
}

// ErrItemNotFound is returned when a manual match names an unknown item.
var ErrItemNotFound = errors.New("item not found")

func itemKey(t index.MediaType, xtreamID int) string {
	return fmt.Sprintf("%s:%d", t, xtreamID)
}

// LoadOverrides reads manual matches from path (a missing file is fine) and
// remembers path for saving. Call before Start.
func (s *Scheduler) LoadOverrides(path string) error {
	s.overridesMu.Lock()
	defer s.overridesMu.Unlock()
	s.overridesPath = path

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	loaded := make(map[string]MatchOverride)
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	s.overrides = loaded
	slog.Info("loaded manual matches", "count", len(loaded))
	return nil
}

// saveOverridesLocked writes the overrides atomically. Caller holds overridesMu.
func (s *Scheduler) saveOverridesLocked() error {
	if s.overridesPath == "" {
		return nil
	}
	data, err := json.MarshalIndent(s.overrides, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.overridesPath), ".matches-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), s.overridesPath)
}

func (s *Scheduler) override(key string) (MatchOverride, bool) {
	s.overridesMu.Lock()
	defer s.overridesMu.Unlock()
	ov, ok := s.overrides[key]
	return ov, ok
}

// revertType restores an item's provider type and season numbering after a
// manual match changed them.
func revertType(item *index.Item) {
	if item.SeasonOffset != 0 {
		item.Episodes = shiftSeasons(item.Episodes, -item.SeasonOffset)
		item.SeasonOffset = 0
	}
	switch item.SourceType {
	case index.TypeMovie:
		item.Type = index.TypeMovie
		item.Episodes = nil
	case index.TypeSeries:
		item.Type = index.TypeSeries
		item.StreamEpisodeID = 0
	}
	item.SourceType = ""
}

// convertType changes item to the override's type, starting from the
// provider's shape. The stream stays the provider's (see Item.SourceType).
func convertType(item *index.Item, ov MatchOverride) {
	revertType(item)
	if ov.SeasonOffset != 0 && item.Type == index.TypeSeries {
		item.Episodes = shiftSeasons(item.Episodes, ov.SeasonOffset)
		item.SeasonOffset = ov.SeasonOffset
	}
	target := index.MediaType(ov.AsType)
	if target == "" || target == item.Type {
		return
	}
	switch target {
	case index.TypeSeries: // VOD stream offered as one episode
		item.Episodes = []index.EpisodeItem{{
			EpisodeID:  item.XtreamID,
			Season:     ov.Season,
			EpisodeNum: ov.Episode,
			Ext:        item.ContainerExt,
			Duration:   item.Duration,
			FileSize:   item.FileSize,
		}}
	case index.TypeMovie: // one provider episode offered as a film
		ep, ok := findEpisode(item.Episodes, ov.EpisodeID)
		if !ok {
			if len(item.Episodes) == 0 {
				slog.Warn("type change to movie skipped: series has no episodes", "name", item.Name)
				return
			}
			// The chosen episode vanished from the provider; use the first.
			slog.Warn("chosen episode missing, using the first", "name", item.Name, "episode_id", ov.EpisodeID)
			ep = item.Episodes[0]
		}
		item.StreamEpisodeID = ep.EpisodeID
		item.ContainerExt = ep.Ext
		item.Duration = ep.Duration
		item.FileSize = ep.FileSize
	}
	item.SourceType = item.Type
	item.Type = target
}

func findEpisode(eps []index.EpisodeItem, id int) (index.EpisodeItem, bool) {
	for _, ep := range eps {
		if ep.EpisodeID == id {
			return ep, true
		}
	}
	return index.EpisodeItem{}, false
}

// applyOverride sets item's type and external IDs from a manual match,
// fetching the IMDB/TVDB cross-references and canonical title from TMDB.
func (s *Scheduler) applyOverride(ctx context.Context, item *index.Item, ov MatchOverride) error {
	convertType(item, ov)
	item.TMDBId = ov.TMDBId
	item.IMDBId = ""
	item.TVDBId = ""
	item.CanonicalName = ""
	item.EnrichFailReason = ""
	item.ManualMatch = true
	item.MatchVersion = matchVersion

	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if id, _ := strconv.Atoi(ov.TMDBId); id > 0 && s.tmdb != nil && s.cfg.TMDB.APIKey != "" {
		switch item.Type {
		case index.TypeMovie:
			ext, err := s.tmdb.GetMovieExternalIDs(ctx, id)
			keep(err)
			if ext != nil {
				item.IMDBId = ext.IMDBID
			}
			details, err := s.tmdb.GetMovieDetails(ctx, id)
			keep(err)
			if details != nil {
				item.CanonicalName = details.Title
				item.RuntimeMins = details.RuntimeMins
				// The user picked this exact release, so its year wins over
				// the provider's: Radarr matches release titles on it.
				if len(details.ReleaseDate) >= 4 {
					item.ReleaseDate = details.ReleaseDate
					item.Year = details.ReleaseDate[:4]
				}
			}
		case index.TypeSeries:
			ext, err := s.tmdb.GetTVExternalIDs(ctx, id)
			keep(err)
			if ext != nil {
				item.IMDBId = ext.IMDBID
				if ext.TVDBID > 0 {
					item.TVDBId = strconv.Itoa(ext.TVDBID)
				}
			}
			name, err := s.tmdb.GetTVTitle(ctx, id)
			keep(err)
			item.CanonicalName = name
		}
	}
	if ov.TVDBId != "" {
		item.TVDBId = ov.TVDBId
	}
	if item.IMDBId == "" && item.TVDBId == "" {
		item.EnrichFailReason = "manual match has no IMDB/TVDB cross-reference"
	}
	return firstErr
}

// reapplyOverrides makes sure every item with a manual match carries it.
func (s *Scheduler) reapplyOverrides(ctx context.Context, items []*index.Item) {
	for _, item := range items {
		ov, ok := s.override(item.Key())
		if !ok {
			continue
		}
		sameType := ov.AsType == "" || item.Type == index.MediaType(ov.AsType)
		sameSeasons := item.SeasonOffset == ov.SeasonOffset
		if item.ManualMatch && sameType && sameSeasons && item.TMDBId == ov.TMDBId && (ov.TVDBId == "" || item.TVDBId == ov.TVDBId) {
			continue
		}
		if err := s.applyOverride(ctx, item, ov); err != nil {
			slog.Warn("manual match re-apply failed", "name", item.Name, "error", err)
		}
	}
}

// SetMatch stores a manual match for an item and applies it to the live
// index immediately. It is kept across syncs until ClearMatch. mediaType is
// the provider type, which identifies the item.
func (s *Scheduler) SetMatch(ctx context.Context, mediaType index.MediaType, xtreamID int, ov MatchOverride) (*index.Item, error) {
	ov.TMDBId = strings.TrimSpace(ov.TMDBId)
	ov.TVDBId = strings.TrimSpace(ov.TVDBId)
	for _, id := range []string{ov.TMDBId, ov.TVDBId} {
		if n, err := strconv.Atoi(id); id != "" && (err != nil || n <= 0) {
			return nil, fmt.Errorf("invalid ID %q", id)
		}
	}
	if ov.TMDBId == "" && ov.TVDBId == "" {
		return nil, errors.New("a TMDB or TVDB ID is required")
	}

	item := s.idx.SearchByXtreamID(xtreamID, string(mediaType))
	if item == nil {
		return nil, ErrItemNotFound
	}

	target := mediaType
	switch ov.AsType {
	case "", string(mediaType):
		ov.AsType = ""
	case string(index.TypeMovie), string(index.TypeSeries):
		target = index.MediaType(ov.AsType)
	default:
		return nil, fmt.Errorf("invalid type %q", ov.AsType)
	}
	if ov.TVDBId != "" && target != index.TypeSeries {
		return nil, errors.New("a TVDB ID only applies to series")
	}
	switch {
	case mediaType == index.TypeMovie && target == index.TypeSeries:
		if ov.Season < 0 || ov.Episode < 1 {
			return nil, errors.New("a season (0 for specials) and episode number are required")
		}
		ov.EpisodeID = 0
	case mediaType == index.TypeSeries && target == index.TypeMovie:
		if ov.TMDBId == "" {
			return nil, errors.New("a TMDB movie ID is required")
		}
		if ov.EpisodeID == 0 && len(item.Episodes) == 1 {
			ov.EpisodeID = item.Episodes[0].EpisodeID
		}
		if _, ok := findEpisode(item.Episodes, ov.EpisodeID); !ok {
			return nil, errors.New("choose which episode is the film")
		}
		ov.Season, ov.Episode = 0, 0
	default:
		ov.Season, ov.Episode, ov.EpisodeID = 0, 0, 0
	}

	if mediaType != index.TypeSeries || target != index.TypeSeries {
		ov.SeasonOffset = 0
	} else if ov.SeasonOffset != 0 {
		if ov.SeasonOffset < -maxSeasonOffset || ov.SeasonOffset > maxSeasonOffset {
			return nil, fmt.Errorf("season offset must be between %d and %d", -maxSeasonOffset, maxSeasonOffset)
		}
		// item may already carry an offset; check against provider numbering.
		for _, ep := range item.Episodes {
			if provider := ep.Season - item.SeasonOffset; provider > 0 && provider+ov.SeasonOffset < 1 {
				return nil, fmt.Errorf("season offset %d would move provider season %d below season 1", ov.SeasonOffset, provider)
			}
		}
	}

	s.overridesMu.Lock()
	if s.overrides == nil {
		s.overrides = make(map[string]MatchOverride)
	}
	s.overrides[itemKey(mediaType, xtreamID)] = ov
	err := s.saveOverridesLocked()
	s.overridesMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("save manual matches: %w", err)
	}

	updated := *item
	if err := s.applyOverride(ctx, &updated, ov); err != nil {
		slog.Warn("manual match lookup incomplete", "name", item.Name, "error", err)
	}
	s.replaceIndexItem(&updated)
	slog.Info("manual match set", "name", item.Name, "type", updated.Type, "tmdb_id", ov.TMDBId, "tvdb_id", ov.TVDBId,
		"imdb_id", updated.IMDBId, "resolved_tvdb_id", updated.TVDBId)
	return &updated, nil
}

// ClearMatch removes a manual match and re-runs automatic matching for the
// item, starting from the TMDB ID the provider tagged it with (if any).
func (s *Scheduler) ClearMatch(ctx context.Context, mediaType index.MediaType, xtreamID int) (*index.Item, error) {
	item := s.idx.SearchByXtreamID(xtreamID, string(mediaType))
	if item == nil {
		return nil, ErrItemNotFound
	}

	s.overridesMu.Lock()
	delete(s.overrides, itemKey(mediaType, xtreamID))
	err := s.saveOverridesLocked()
	s.overridesMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("save manual matches: %w", err)
	}

	updated := *item
	revertType(&updated)
	updated.ManualMatch = false
	updated.TMDBId = updated.ProviderTMDBId
	updated.IMDBId = ""
	updated.TVDBId = ""
	updated.CanonicalName = ""
	if _, err := s.enrich(ctx, []*index.Item{&updated}, nil); err != nil {
		slog.Warn("automatic re-match incomplete", "name", item.Name, "error", err)
	}
	s.replaceIndexItem(&updated)
	slog.Info("manual match cleared", "name", item.Name, "tmdb_id", updated.TMDBId)
	return &updated, nil
}

// replaceIndexItem swaps one item in the live index and persists the cache.
func (s *Scheduler) replaceIndexItem(updated *index.Item) {
	s.indexEditMu.Lock()
	all := s.idx.All()
	for i, it := range all {
		if it.Key() == updated.Key() {
			all[i] = updated
		}
	}
	s.idx.Replace(all)
	s.indexEditMu.Unlock()
	s.persistIndex()
}

// persistIndex saves the live index to the cache file so a manual edit
// survives a restart. When a sync is running it is skipped: the sync saves
// the cache itself when it finishes, with the edit applied.
func (s *Scheduler) persistIndex() {
	if !s.syncMu.TryLock() {
		return
	}
	defer s.syncMu.Unlock()
	// Keep the real last-sync time (zero if none yet): a restart decides
	// from it whether a sync is due.
	s.mu.RLock()
	lastSync := s.status.LastSync
	s.mu.RUnlock()
	if err := SaveIndexCache(s.cachePath, s.idx.All(), s.syncGen, lastSync, s.SyncHistory()); err != nil {
		slog.Warn("index cache save failed", "error", err)
	}
}
