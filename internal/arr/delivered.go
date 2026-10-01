package arr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/vodarr/vodarr/internal/config"
)

// Delivery is an import VODarr finished by replacing arr's .mkv stub with a
// .strm in the library. Sonarr/Radarr cannot use a .strm as a media file, so
// from their side the item is now missing.
type Delivery struct {
	SeriesID   int   // Sonarr series ID
	EpisodeIDs []int // Sonarr episode IDs
	MovieID    int   // Radarr movie ID
}

// Delivered brings arr up to date after VODarr deleted the stub: it
// unmonitors the item (when unmonitor is set) so it is not searched and
// grabbed again, by VODarr's auto-search or by any other indexer, and then
// rescans that one series or movie so arr notices the file is gone now
// rather than at its next scheduled refresh.
func (s *Searcher) Delivered(ctx context.Context, inst config.ArrInstance, d Delivery, unmonitor bool) error {
	switch inst.Type {
	case "sonarr":
		if d.SeriesID <= 0 {
			return fmt.Errorf("no series ID in webhook")
		}
		if unmonitor && len(d.EpisodeIDs) > 0 {
			body, _ := json.Marshal(map[string]interface{}{"episodeIds": d.EpisodeIDs, "monitored": false})
			if err := s.do(ctx, inst, http.MethodPut, "/api/v3/episode/monitor", nil, body, nil); err != nil {
				return fmt.Errorf("unmonitor episodes: %w", err)
			}
		}
		body, _ := json.Marshal(map[string]interface{}{"name": "RescanSeries", "seriesId": d.SeriesID})
		if err := s.do(ctx, inst, http.MethodPost, "/api/v3/command", nil, body, nil); err != nil {
			return fmt.Errorf("rescan series: %w", err)
		}
	case "radarr":
		if d.MovieID <= 0 {
			return fmt.Errorf("no movie ID in webhook")
		}
		if unmonitor {
			body, _ := json.Marshal(map[string]interface{}{"movieIds": []int{d.MovieID}, "monitored": false})
			if err := s.do(ctx, inst, http.MethodPut, "/api/v3/movie/editor", nil, body, nil); err != nil {
				return fmt.Errorf("unmonitor movie: %w", err)
			}
		}
		body, _ := json.Marshal(map[string]interface{}{"name": "RescanMovie", "movieId": d.MovieID})
		if err := s.do(ctx, inst, http.MethodPost, "/api/v3/command", nil, body, nil); err != nil {
			return fmt.Errorf("rescan movie: %w", err)
		}
	default:
		return fmt.Errorf("unknown instance type %q", inst.Type)
	}
	return nil
}

// PickInstance finds the configured instance a webhook came from. arrType is
// "sonarr" or "radarr" (from the payload shape). With one instance of that
// type it is used; with several, the payload's instance name or application
// URL must match one.
func PickInstance(instances []config.ArrInstance, arrType, instanceName, applicationURL string) (config.ArrInstance, bool) {
	var ofType []config.ArrInstance
	for _, inst := range instances {
		if inst.Type == arrType {
			ofType = append(ofType, inst)
		}
	}
	if len(ofType) == 1 {
		return ofType[0], true
	}
	norm := func(u string) string { return strings.TrimRight(strings.ToLower(strings.TrimSpace(u)), "/") }
	for _, inst := range ofType {
		if instanceName != "" && strings.EqualFold(inst.Name, instanceName) {
			return inst, true
		}
		if applicationURL != "" && norm(inst.URL) == norm(applicationURL) {
			return inst, true
		}
	}
	return config.ArrInstance{}, false
}
