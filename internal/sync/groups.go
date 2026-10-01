package sync

import (
	"context"
	"log/slog"

	"github.com/vodarr/vodarr/internal/index"
	"github.com/vodarr/vodarr/internal/xtream"
)

// vodCategoryKey and seriesCategoryKey build the key a provider group is
// excluded by. VOD and series category IDs are separate ID spaces on most
// providers, so the type is part of the key.
func vodCategoryKey(id string) string    { return "vod:" + id }
func seriesCategoryKey(id string) string { return "series:" + id }

func categorySet(keys []string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		set[k] = true
	}
	return set
}

// excludedCategories returns a snapshot of the excluded group keys.
func (s *Scheduler) excludedCategories() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]bool, len(s.excluded))
	for k := range s.excluded {
		out[k] = true
	}
	return out
}

// isExcluded reports whether item belongs to an excluded provider group.
func (s *Scheduler) isExcluded(item *index.Item) bool {
	if item.CategoryKey == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.excluded[item.CategoryKey]
}

// SetExcludedCategories replaces the excluded provider groups and removes
// their items from the live index at once, so Sonarr/Radarr stop finding
// them without waiting for a sync. Files on disk are not touched. Items
// cached before groups were recorded are dropped by the next sync.
func (s *Scheduler) SetExcludedCategories(keys []string) (removed int) {
	s.mu.Lock()
	s.excluded = categorySet(keys)
	s.mu.Unlock()

	s.indexEditMu.Lock()
	all := s.idx.All()
	kept := all[:0:0]
	for _, item := range all {
		if s.isExcluded(item) {
			removed++
			continue
		}
		kept = append(kept, item)
	}
	if removed > 0 {
		s.idx.Replace(kept)
	}
	s.indexEditMu.Unlock()

	if removed > 0 {
		slog.Info("excluded provider groups removed from index", "items", removed)
		s.persistIndex()
	}
	return removed
}

// categoryNames fetches a provider's group list as id → name. Errors are
// logged and yield an empty map: names are only used for display.
func (s *Scheduler) categoryNames(ctx context.Context, fetch func(context.Context) ([]xtream.Category, error)) map[string]string {
	cats, err := fetch(ctx)
	if err != nil {
		slog.Warn("provider group list unavailable", "error", err)
		return map[string]string{}
	}
	names := make(map[string]string, len(cats))
	for _, c := range cats {
		names[c.ID] = c.Name
	}
	return names
}
