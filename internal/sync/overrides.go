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
type MatchOverride struct {
	TMDBId string `json:"tmdb_id,omitempty"`
	TVDBId string `json:"tvdb_id,omitempty"`
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

// applyOverride sets item's external IDs from a manual match, fetching the
// IMDB/TVDB cross-references and canonical title from TMDB.
func (s *Scheduler) applyOverride(ctx context.Context, item *index.Item, ov MatchOverride) error {
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
		ov, ok := s.override(itemKey(item.Type, item.XtreamID))
		if !ok {
			continue
		}
		if item.ManualMatch && item.TMDBId == ov.TMDBId && (ov.TVDBId == "" || item.TVDBId == ov.TVDBId) {
			continue
		}
		if err := s.applyOverride(ctx, item, ov); err != nil {
			slog.Warn("manual match re-apply failed", "name", item.Name, "error", err)
		}
	}
}

// SetMatch stores a manual match for an item and applies it to the live
// index immediately. It is kept across syncs until ClearMatch.
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
	if ov.TVDBId != "" && mediaType != index.TypeSeries {
		return nil, errors.New("a TVDB ID only applies to series")
	}

	item := s.idx.SearchByXtreamID(xtreamID, string(mediaType))
	if item == nil {
		return nil, ErrItemNotFound
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
	slog.Info("manual match set", "name", item.Name, "tmdb_id", ov.TMDBId, "tvdb_id", ov.TVDBId,
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
		if it.Type == updated.Type && it.XtreamID == updated.XtreamID {
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
