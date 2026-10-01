package web

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vodarr/vodarr/internal/arr"
	"github.com/vodarr/vodarr/internal/config"
	"github.com/vodarr/vodarr/internal/index"
	"github.com/vodarr/vodarr/internal/strm"
	vodarrsync "github.com/vodarr/vodarr/internal/sync"
	"github.com/vodarr/vodarr/internal/tmdb"
	"github.com/vodarr/vodarr/internal/xtream"
)

// categoryKeyRe validates excluded group keys ("vod:<id>" / "series:<id>").
var categoryKeyRe = regexp.MustCompile(`^(vod|series):[^\s]{1,64}$`)

type categoryResp struct {
	Key      string `json:"key"`
	Type     string `json:"type"` // "movie" or "series"
	Name     string `json:"name"`
	Count    int    `json:"count"` // items currently indexed from this group
	Excluded bool   `json:"excluded"`
}

// handleGetCategories lists the provider's groups, live from Xtream, with
// how many indexed items each holds and whether it is excluded. Excluded
// groups have no indexed items, so their count is 0.
func (h *Handler) handleGetCategories(w http.ResponseWriter, r *http.Request) {
	h.cfgMu.RLock()
	xcfg := h.cfg.Xtream
	excluded := make(map[string]bool)
	for _, k := range h.cfg.Sync.ExcludedCategories {
		excluded[k] = true
	}
	h.cfgMu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	xc := xtream.NewClient(xcfg.URL, xcfg.Username, xcfg.Password)

	vod, err := xc.GetVODCategories(ctx)
	if err != nil {
		h.writeJSONStatus(w, http.StatusBadGateway, map[string]string{"error": "provider: " + err.Error()})
		return
	}
	series, err := xc.GetSeriesCategories(ctx)
	if err != nil {
		h.writeJSONStatus(w, http.StatusBadGateway, map[string]string{"error": "provider: " + err.Error()})
		return
	}

	counts := make(map[string]int)
	for _, item := range h.idx.All() {
		if item.CategoryKey != "" {
			counts[item.CategoryKey]++
		}
	}

	out := make([]categoryResp, 0, len(vod)+len(series))
	for _, c := range vod {
		k := "vod:" + c.ID
		out = append(out, categoryResp{Key: k, Type: "movie", Name: c.Name, Count: counts[k], Excluded: excluded[k]})
	}
	for _, c := range series {
		k := "series:" + c.ID
		out = append(out, categoryResp{Key: k, Type: "series", Name: c.Name, Count: counts[k], Excluded: excluded[k]})
	}
	h.writeJSON(w, map[string]interface{}{"categories": out})
}

// handlePutCategories saves the excluded groups to config.yml and removes
// their items from the live index straight away. No restart is needed.
func (h *Handler) handlePutCategories(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		Excluded []string `json:"excluded"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	seen := make(map[string]bool)
	keys := []string{}
	for _, k := range req.Excluded {
		if !categoryKeyRe.MatchString(k) {
			h.writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid group key: " + k})
			return
		}
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	h.cfgMu.Lock()
	newCfg := *h.cfg
	newCfg.Sync.ExcludedCategories = keys
	if err := config.Save(h.cfgPath, &newCfg); err != nil {
		h.cfgMu.Unlock()
		slog.Error("failed to save config", "error", err)
		h.writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "failed to save config: " + err.Error()})
		return
	}
	h.cfg = &newCfg
	h.cfgMu.Unlock()

	removed := 0
	if h.scheduler != nil {
		removed = h.scheduler.SetExcludedCategories(keys)
	}
	h.writeJSON(w, map[string]interface{}{"excluded": len(keys), "removed": removed})
}

type matchCandidate struct {
	ID       int    `json:"id"`
	Title    string `json:"title"`
	Original string `json:"original_title,omitempty"`
	Year     string `json:"year"`
	Overview string `json:"overview"`
	Poster   string `json:"poster,omitempty"`
}

// handleMatchSearch searches TMDB for manual-match candidates.
// Query: type=movie|series, q=title, year=optional.
func (h *Handler) handleMatchSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := strings.TrimSpace(q.Get("q"))
	if query == "" {
		h.writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "q is required"})
		return
	}
	year, _ := strconv.Atoi(q.Get("year"))

	h.cfgMu.RLock()
	apiKey := h.cfg.TMDB.APIKey
	h.cfgMu.RUnlock()
	if apiKey == "" {
		h.writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "no TMDB API key configured"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tc := tmdb.NewClient(apiKey)
	defer tc.Stop()

	poster := func(p string) string {
		if p == "" {
			return ""
		}
		return "https://image.tmdb.org/t/p/w92" + p
	}
	year4 := func(date string) string {
		if len(date) >= 4 {
			return date[:4]
		}
		return ""
	}

	out := []matchCandidate{}
	switch q.Get("type") {
	case "movie":
		results, err := tc.SearchMovieAll(ctx, query, year)
		if err != nil {
			h.writeJSONStatus(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		for _, m := range results {
			out = append(out, matchCandidate{ID: m.ID, Title: m.Title, Original: m.OriginalTitle,
				Year: year4(m.ReleaseDate), Overview: m.Overview, Poster: poster(m.PosterPath)})
		}
	case "series":
		results, err := tc.SearchTVAll(ctx, query, year)
		if err != nil {
			h.writeJSONStatus(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		for _, t := range results {
			out = append(out, matchCandidate{ID: t.ID, Title: t.Name, Original: t.OriginalName,
				Year: year4(t.FirstAirDate), Overview: t.Overview, Poster: poster(t.PosterPath)})
		}
	default:
		h.writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "type must be movie or series"})
		return
	}
	h.writeJSON(w, map[string]interface{}{"results": out})
}

type matchRequest struct {
	Type     string `json:"type"` // provider type, "movie" or "series": identifies the item
	XtreamID int    `json:"xtream_id"`
	TMDBId   string `json:"tmdb_id"`
	TVDBId   string `json:"tvdb_id"`

	// Optional type change, see vodarrsync.MatchOverride.
	AsType    string `json:"as_type"`
	Season    int    `json:"season"`
	Episode   int    `json:"episode"`
	EpisodeID int    `json:"episode_id"`

	// Optional renumbering of a provider series' seasons to TheTVDB's.
	SeasonOffset int `json:"season_offset"`
}

// handlePutMatch sets a manual match and returns the updated item.
func (h *Handler) handlePutMatch(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req matchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	mt, ok := parseMediaType(req.Type)
	if !ok || req.XtreamID <= 0 {
		h.writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "type and xtream_id are required"})
		return
	}
	if h.scheduler == nil {
		h.writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "scheduler not running"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	item, err := h.scheduler.SetMatch(ctx, mt, req.XtreamID, vodarrsync.MatchOverride{
		TMDBId: req.TMDBId, TVDBId: req.TVDBId,
		AsType: req.AsType, Season: req.Season, Episode: req.Episode, EpisodeID: req.EpisodeID,
		SeasonOffset: req.SeasonOffset,
	})
	h.writeMatchResult(w, item, err)
}

// handleDeleteMatch clears a manual match. Query: type, xtream_id.
func (h *Handler) handleDeleteMatch(w http.ResponseWriter, r *http.Request) {
	mt, ok := parseMediaType(r.URL.Query().Get("type"))
	id, _ := strconv.Atoi(r.URL.Query().Get("xtream_id"))
	if !ok || id <= 0 {
		h.writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "type and xtream_id are required"})
		return
	}
	if h.scheduler == nil {
		h.writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "scheduler not running"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	item, err := h.scheduler.ClearMatch(ctx, mt, id)
	h.writeMatchResult(w, item, err)
}

func (h *Handler) writeMatchResult(w http.ResponseWriter, item *index.Item, err error) {
	switch {
	case errors.Is(err, vodarrsync.ErrItemNotFound):
		h.writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case err != nil:
		h.writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		h.writeJSON(w, map[string]interface{}{"item": item})
	}
}

// handleArrRepair finishes past imports the webhook never handled, for every
// configured Sonarr/Radarr instance (see arr.RepairImports).
func (h *Handler) handleArrRepair(w http.ResponseWriter, r *http.Request) {
	h.cfgMu.RLock()
	instances := h.cfg.Arr.Instances
	outputPath := h.cfg.Output.Path
	mode := h.cfg.Output.Mode
	unmonitor := h.cfg.Arr.UnmonitorDelivered
	h.cfgMu.RUnlock()

	if mode == "download" {
		h.writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "download mode imports real files; there is nothing to repair"})
		return
	}
	if len(instances) == 0 {
		h.writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "no Sonarr/Radarr instances configured"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	finish := func(libMkv, sourceMkv string) error {
		return strm.FinishImport(libMkv, sourceMkv, outputPath)
	}
	searcher := arr.NewSearcher()
	results := make([]arr.RepairResult, 0, len(instances))
	for _, inst := range instances {
		results = append(results, searcher.RepairImports(ctx, inst, finish, unmonitor))
	}
	h.writeJSON(w, map[string]interface{}{"results": results})
}

func parseMediaType(s string) (index.MediaType, bool) {
	switch s {
	case "movie":
		return index.TypeMovie, true
	case "series":
		return index.TypeSeries, true
	}
	return "", false
}

func (h *Handler) writeJSONStatus(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
