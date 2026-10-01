// Package arr asks Sonarr/Radarr to search for wanted items VODarr can
// supply.
//
// Sonarr and Radarr only grab on their own through RSS sync, which reads an
// indexer's feed of *recent* releases. VODarr's catalog is not a stream of
// new releases, so RSS never surfaces most of it, and a series or movie added
// without "search on add" would sit missing until searched by hand. After
// each catalog sync, Searcher reads every instance's wanted/missing list,
// keeps only what is in VODarr's index, and starts a search for exactly
// those items, instead of a blanket "search all missing" that would also hit
// every other indexer for things VODarr does not have.
package arr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	gosync "sync"
	"time"

	"github.com/vodarr/vodarr/internal/config"
	"github.com/vodarr/vodarr/internal/index"
)

const (
	pageSize = 250
	// maxPages bounds how much of a huge wanted list is read per run.
	maxPages = 40
	// batchSize is the most IDs sent in one search command.
	batchSize = 50
	// cooldown stops re-triggering the same item every sync when arr
	// rejected the release (e.g. quality or language), which would otherwise
	// repeat a pointless search across all of arr's indexers.
	cooldown = 24 * time.Hour
)

// Catalog is the part of the index the searcher needs.
type Catalog interface {
	SearchByTVDB(id string) []*index.Item
	SearchByTMDB(id string) []*index.Item
	SearchByIMDB(id string) []*index.Item
}

// Searcher triggers targeted arr searches. Safe for concurrent use.
type Searcher struct {
	http *http.Client
	now  func() time.Time

	mu     gosync.Mutex
	recent map[string]time.Time // "<instance>:<kind>:<id>" → last searched
}

func NewSearcher() *Searcher {
	return &Searcher{
		http:   &http.Client{Timeout: 30 * time.Second},
		now:    time.Now,
		recent: make(map[string]time.Time),
	}
}

// Result is what one instance run did.
type Result struct {
	Instance string
	Wanted   int // missing items arr reported
	Searched int // of those, in VODarr and searched now
	Skipped  int // in VODarr but searched within the cooldown
}

// Run checks every instance. Errors are logged per instance and do not stop
// the others.
func (s *Searcher) Run(ctx context.Context, instances []config.ArrInstance, cat Catalog) []Result {
	var results []Result
	for _, inst := range instances {
		var res Result
		var err error
		switch inst.Type {
		case "sonarr":
			res, err = s.runSonarr(ctx, inst, cat)
		case "radarr":
			res, err = s.runRadarr(ctx, inst, cat)
		default:
			continue
		}
		if err != nil {
			slog.Warn("arr auto-search failed", "instance", inst.Name, "error", err)
			continue
		}
		slog.Info("arr auto-search", "instance", inst.Name,
			"wanted", res.Wanted, "searched", res.Searched, "recently_searched", res.Skipped)
		results = append(results, res)
	}
	return results
}

type sonarrEpisode struct {
	ID            int `json:"id"`
	SeasonNumber  int `json:"seasonNumber"`
	EpisodeNumber int `json:"episodeNumber"`
	Series        struct {
		TVDBId int `json:"tvdbId"`
	} `json:"series"`
}

func (s *Searcher) runSonarr(ctx context.Context, inst config.ArrInstance, cat Catalog) (Result, error) {
	res := Result{Instance: inst.Name}
	var ids []int
	for page := 1; page <= maxPages; page++ {
		var resp struct {
			TotalRecords int             `json:"totalRecords"`
			Records      []sonarrEpisode `json:"records"`
		}
		q := url.Values{"page": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(pageSize)},
			"includeSeries": {"true"}, "monitored": {"true"}}
		if err := s.get(ctx, inst, "/api/v3/wanted/missing", q, &resp); err != nil {
			return res, err
		}
		for _, ep := range resp.Records {
			res.Wanted++
			if !hasEpisode(cat, ep) {
				continue
			}
			if s.recentlySearched(inst.Name, "ep", ep.ID) {
				res.Skipped++
				continue
			}
			ids = append(ids, ep.ID)
		}
		if len(resp.Records) == 0 || page*pageSize >= resp.TotalRecords {
			break
		}
	}
	if err := s.command(ctx, inst, "EpisodeSearch", "episodeIds", ids); err != nil {
		return res, err
	}
	s.markSearched(inst.Name, "ep", ids)
	res.Searched = len(ids)
	return res, nil
}

// hasEpisode reports whether VODarr's index holds that episode.
func hasEpisode(cat Catalog, ep sonarrEpisode) bool {
	if ep.Series.TVDBId <= 0 {
		return false
	}
	for _, item := range cat.SearchByTVDB(strconv.Itoa(ep.Series.TVDBId)) {
		if item.Type != index.TypeSeries {
			continue
		}
		for _, e := range item.Episodes {
			if e.Season == ep.SeasonNumber && e.EpisodeNum == ep.EpisodeNumber {
				return true
			}
		}
	}
	return false
}

type radarrMovie struct {
	ID          int    `json:"id"`
	TMDBId      int    `json:"tmdbId"`
	IMDBId      string `json:"imdbId"`
	Monitored   bool   `json:"monitored"`
	HasFile     bool   `json:"hasFile"`
	IsAvailable bool   `json:"isAvailable"`
}

func (s *Searcher) runRadarr(ctx context.Context, inst config.ArrInstance, cat Catalog) (Result, error) {
	res := Result{Instance: inst.Name}
	movies, err := s.radarrMissing(ctx, inst)
	if err != nil {
		return res, err
	}
	var ids []int
	for _, m := range movies {
		res.Wanted++
		if !hasMovie(cat, m) {
			continue
		}
		if s.recentlySearched(inst.Name, "movie", m.ID) {
			res.Skipped++
			continue
		}
		ids = append(ids, m.ID)
	}
	if err := s.command(ctx, inst, "MoviesSearch", "movieIds", ids); err != nil {
		return res, err
	}
	s.markSearched(inst.Name, "movie", ids)
	res.Searched = len(ids)
	return res, nil
}

// radarrMissing returns monitored, available movies without a file. It uses
// wanted/missing and falls back to the full movie list on Radarr versions
// that lack that endpoint.
func (s *Searcher) radarrMissing(ctx context.Context, inst config.ArrInstance) ([]radarrMovie, error) {
	var out []radarrMovie
	for page := 1; page <= maxPages; page++ {
		var resp struct {
			TotalRecords int           `json:"totalRecords"`
			Records      []radarrMovie `json:"records"`
		}
		q := url.Values{"page": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(pageSize)}, "monitored": {"true"}}
		err := s.get(ctx, inst, "/api/v3/wanted/missing", q, &resp)
		var se *statusError
		if page == 1 && asStatus(err, &se) && se.code == http.StatusNotFound {
			return s.radarrMissingFromList(ctx, inst)
		}
		if err != nil {
			return nil, err
		}
		for _, m := range resp.Records {
			if m.IsAvailable {
				out = append(out, m)
			}
		}
		if len(resp.Records) == 0 || page*pageSize >= resp.TotalRecords {
			break
		}
	}
	return out, nil
}

func (s *Searcher) radarrMissingFromList(ctx context.Context, inst config.ArrInstance) ([]radarrMovie, error) {
	var all []radarrMovie
	if err := s.get(ctx, inst, "/api/v3/movie", nil, &all); err != nil {
		return nil, err
	}
	var out []radarrMovie
	for _, m := range all {
		if m.Monitored && !m.HasFile && m.IsAvailable {
			out = append(out, m)
		}
	}
	return out, nil
}

func hasMovie(cat Catalog, m radarrMovie) bool {
	var items []*index.Item
	if m.TMDBId > 0 {
		items = cat.SearchByTMDB(strconv.Itoa(m.TMDBId))
	}
	if m.IMDBId != "" {
		items = append(items, cat.SearchByIMDB(m.IMDBId)...)
	}
	for _, item := range items {
		if item.Type == index.TypeMovie {
			return true
		}
	}
	return false
}

func (s *Searcher) recentlySearched(instance, kind string, id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.recent[fmt.Sprintf("%s:%s:%d", instance, kind, id)]
	return ok && s.now().Sub(t) < cooldown
}

func (s *Searcher) markSearched(instance, kind string, ids []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, t := range s.recent {
		if now.Sub(t) >= cooldown {
			delete(s.recent, k)
		}
	}
	for _, id := range ids {
		s.recent[fmt.Sprintf("%s:%s:%d", instance, kind, id)] = now
	}
}

// command posts search commands in batches. No IDs, no request.
func (s *Searcher) command(ctx context.Context, inst config.ArrInstance, name, field string, ids []int) error {
	for start := 0; start < len(ids); start += batchSize {
		end := min(start+batchSize, len(ids))
		body, _ := json.Marshal(map[string]interface{}{"name": name, field: ids[start:end]})
		if err := s.do(ctx, inst, http.MethodPost, "/api/v3/command", nil, body, nil); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func (s *Searcher) get(ctx context.Context, inst config.ArrInstance, path string, q url.Values, out interface{}) error {
	return s.do(ctx, inst, http.MethodGet, path, q, nil, out)
}

type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.code, e.body) }

func asStatus(err error, target **statusError) bool {
	se, ok := err.(*statusError)
	if ok {
		*target = se
	}
	return ok
}

func (s *Searcher) do(ctx context.Context, inst config.ArrInstance, method, path string, q url.Values, body []byte, out interface{}) error {
	u := strings.TrimRight(inst.URL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", inst.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &statusError{code: resp.StatusCode, body: strings.TrimSpace(string(b))}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
