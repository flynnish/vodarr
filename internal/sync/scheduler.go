package sync

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	gosync "sync"
	"sync/atomic"
	"time"

	"github.com/vodarr/vodarr/internal/config"
	"github.com/vodarr/vodarr/internal/index"
	"github.com/vodarr/vodarr/internal/strm"
	"github.com/vodarr/vodarr/internal/tmdb"
	"github.com/vodarr/vodarr/internal/tvdb"
	"github.com/vodarr/vodarr/internal/xtream"
)

// iptvPrefixRe matches one or more stacked leading IPTV category prefixes such as
// "| NL |", "| NL | HD |", "┃NL┃", etc. in a single pass.
// It handles both ASCII pipe (|, U+007C) and the Unicode box-drawing vertical
// bar (┃, U+2503) that some providers use, plus any leading whitespace/tabs.
var iptvPrefixRe = regexp.MustCompile(`^[\s]*[|┃]\s*(?:[^|┃]+[|┃]\s*)+`)

// yearInParensRe matches a trailing parenthesised 4-digit year, e.g. "(1993)".
var yearInParensRe = regexp.MustCompile(`\s*\((\d{4})\)\s*$`)

// yearDashRe matches a trailing dash-separated 4-digit year, e.g. "Movie - 2021".
var yearDashRe = regexp.MustCompile(`\s*-\s*(\d{4})\s*$`)

// yearBracketRe matches a trailing bracket-enclosed 4-digit year, e.g. "Movie [2021]".
var yearBracketRe = regexp.MustCompile(`\s*\[(\d{4})\]\s*$`)

// hevcRe matches HEVC codec markers in stream names.
var hevcRe = regexp.MustCompile(`(?i)\bHEVC\b`)

// fourKRe matches 4K resolution markers in stream names.
var fourKRe = regexp.MustCompile(`\b4K\b`)

// dolbyRe matches common Dolby markers in stream names, e.g. "[DOLBY]", "(DOLBY)", "DOLBY".
var dolbyRe = regexp.MustCompile(`(?i)[\[(]DOLBY[^\])\[(]*[\])]|\bDOLBY\b`)

// nlGespokenRe matches Dutch audio markers in stream names, e.g. "(NL GESPROKEN)" or "[NL Gesproken]".
var nlGespokenRe = regexp.MustCompile(`(?i)[(\[]NL\s+GESPROKEN[)\]]`)

// langTagRe matches a bare language/country tag such as "EN" or "NL".
var langTagRe = regexp.MustCompile(`^[A-Z]{2,3}$`)

// splitTrailingYear splits one trailing 4-digit year (parens, dash, or
// bracket) off a name. Only one: in "1985 (2023)" the title is "1985". And
// not when only a language tag is left, as in "EN - 1923", where the number is
// the title. Returns the name unchanged and "" when there is no such year.
func splitTrailingYear(name string) (rest, year string) {
	for _, re := range []*regexp.Regexp{yearInParensRe, yearDashRe, yearBracketRe} {
		if loc := re.FindStringSubmatchIndex(name); loc != nil {
			rest = strings.TrimSpace(name[:loc[0]])
			if langTagRe.MatchString(rest) {
				return name, ""
			}
			return rest, name[loc[2]:loc[3]]
		}
	}
	return name, ""
}

// extractTrailingYear returns the 4-digit year embedded at the end of a name
// (parens, dash, or bracket), or "" if none is found.
func extractTrailingYear(name string) string {
	_, year := splitTrailingYear(name)
	return year
}

// extractNameYear extracts a trailing 4-digit year from a raw IPTV stream
// name after stripping IPTV prefixes and quality/language markers (HEVC, 4K,
// DOLBY, NL GESPROKEN, user patterns).  Unlike cleanTitleForSearch it does NOT
// strip year patterns -- it strips only the noise that follows the year, so
// that extractTrailingYear can find it at the end of the string.
// Returns "" if no trailing year pattern is present.
func extractNameYear(name string, patterns []*regexp.Regexp) string {
	title := iptvPrefixRe.ReplaceAllString(name, "")
	title = hevcRe.ReplaceAllString(title, "")
	title = fourKRe.ReplaceAllString(title, "")
	title = dolbyRe.ReplaceAllString(title, "")
	title = nlGespokenRe.ReplaceAllString(title, "")
	for _, re := range patterns {
		title = re.ReplaceAllString(title, "")
	}
	return extractTrailingYear(strings.TrimSpace(title))
}

// cleanTitleForSearch strips IPTV prefixes, quality/language markers, trailing
// year noise, and user-defined patterns from a stream name before passing it
// to TMDB search.  Quality markers (HEVC, 4K, DOLBY) are stripped first so
// that end-anchored year patterns still match when a marker follows the year.
func cleanTitleForSearch(name string, patterns []*regexp.Regexp) string {
	title := iptvPrefixRe.ReplaceAllString(name, "")
	// Strip quality/language markers before year patterns so anchors work
	// correctly when markers appear after the year (e.g. "Movie - 2021 HEVC").
	title = hevcRe.ReplaceAllString(title, "")
	title = fourKRe.ReplaceAllString(title, "")
	title = dolbyRe.ReplaceAllString(title, "")
	title = nlGespokenRe.ReplaceAllString(title, "")
	title, _ = splitTrailingYear(strings.TrimSpace(title))
	// TMDB finds "The Office", not the catalogue form "Office, The".
	title = index.MoveTrailingArticle(title)
	for _, re := range patterns {
		title = re.ReplaceAllString(title, "")
	}
	return strings.TrimSpace(title)
}

// SyncRun records the outcome of a single sync run.
type SyncRun struct {
	StartedAt  time.Time `json:"started_at"`
	DurationMs int64     `json:"duration_ms"`
	Found      int       `json:"found"`
	Enriched   int       `json:"enriched"`
	Unenriched int       `json:"unenriched"`
	Retained   int       `json:"retained"`
	Expired    int       `json:"expired"`
	Error      string    `json:"error,omitempty"`
}

// Status describes the current sync state.
type Status struct {
	Running     bool      `json:"running"`
	LastSync    time.Time `json:"last_sync"`
	NextSync    time.Time `json:"next_sync"`
	TotalMovies int       `json:"total_movies"`
	TotalSeries int       `json:"total_series"`
	Error       string    `json:"error,omitempty"`
	Progress    Progress  `json:"progress"`

	LastSyncDurationMs int64 `json:"last_sync_duration_ms"`
	UnenrichedCount    int   `json:"unenriched_count"`
	GraceRetained      int   `json:"grace_retained"`
	LastExpired        int   `json:"last_expired"`
	SyncGen            int   `json:"sync_gen"`
	GraceCycles        int   `json:"grace_cycles"`
}

// Progress tracks current sync progress.
type Progress struct {
	Stage   string `json:"stage"`
	Current int    `json:"current"`
	Total   int    `json:"total"`
}

// Scheduler manages periodic syncing of the Xtream catalog into the index.
type Scheduler struct {
	cfg          *config.Config
	xtream       *xtream.Client // primary provider
	extra        []source       // extra providers (SetProviders)

	// vodTags caches, by item key, the TMDB ID a movie's get_vod_info tagged
	// it with (0 for none), so an unmatched movie is asked about once per run.
	vodTags      gosync.Map
	tmdb         *tmdb.Client
	tvdb         *tvdb.Client // nil when tvdb_api_key is not configured
	idx          *index.Index
	writer       *strm.Writer // nil = cleanup disabled
	userPatterns []*regexp.Regexp
	cachePath    string
	syncGen      int // monotonically increasing sync generation counter

	mu          gosync.RWMutex // 3A: protects status field
	syncMu      gosync.Mutex   // 3B: serialises concurrent Sync calls
	status      Status
	syncHistory []SyncRun // rolling log, capped at syncHistoryCap, protected by mu
	cancel      context.CancelFunc

	// excluded holds the provider groups left out of the index
	// ("vod:<id>" / "series:<id>"); protected by mu.
	excluded map[string]bool

	// overrides holds manual matches keyed by "type:xtreamID", persisted to
	// overridesPath; protected by overridesMu.
	overridesMu   gosync.Mutex
	overrides     map[string]MatchOverride
	overridesPath string

	// indexEditMu serialises read-modify-write edits of the live index
	// made outside a sync (manual matches, group exclusion).
	indexEditMu gosync.Mutex

	// afterSync, when set, runs in the background after each successful sync
	// (used to ask Sonarr/Radarr to search for what is now available).
	afterSync func(context.Context)
}

// SetAfterSync registers fn to run in the background after every successful
// sync. Call before Start.
func (s *Scheduler) SetAfterSync(fn func(context.Context)) {
	s.afterSync = fn
}

const syncHistoryCap = 365

func NewScheduler(cfg *config.Config, xc *xtream.Client, tc *tmdb.Client, idx *index.Index, w *strm.Writer) *Scheduler {
	var patterns []*regexp.Regexp
	for _, p := range cfg.Sync.TitleCleanupPatterns {
		if strings.TrimSpace(p) == "" {
			continue
		}
		re, err := regexp.Compile(p)
		if err != nil {
			slog.Warn("invalid title_cleanup_pattern, skipping", "pattern", p, "error", err)
			continue
		}
		patterns = append(patterns, re)
	}
	var tvdbClient *tvdb.Client
	if cfg.TMDB.TVDBAPIKey != "" {
		tvdbClient = tvdb.NewClient(cfg.TMDB.TVDBAPIKey)
	}
	return &Scheduler{
		cfg:          cfg,
		xtream:       xc,
		tmdb:         tc,
		tvdb:         tvdbClient,
		idx:          idx,
		writer:       w,
		userPatterns: patterns,
		cachePath:    CachePath(cfg.Output.Path),
		excluded:     categorySet(cfg.Sync.ExcludedCategories),
		overrides:    make(map[string]MatchOverride),
	}
}

// Start begins the sync scheduler. If a cache exists it is loaded immediately
// so the index is populated before the first sync completes.
func (s *Scheduler) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	// Populate the index from the persisted cache so Newznab returns results
	// immediately on restart, before the first sync finishes.
	var lastSync time.Time
	cachedItems := 0
	if cached, err := LoadIndexCache(s.cachePath); err != nil {
		slog.Warn("index cache load failed, starting empty", "error", err)
	} else if cached != nil {
		if len(cached.Items) > 0 {
			s.idx.Replace(cached.Items)
			s.syncGen = cached.SyncGeneration
			movies, series := s.idx.Counts()
			slog.Info("loaded index from cache", "movies", movies, "series", series, "cached_at", cached.Timestamp, "sync_gen", s.syncGen)
		}
		cachedItems = len(cached.Items)
		lastSync = cached.LastSync
		// Restore the last sync time so the UI shows it after restart.
		if !cached.LastSync.IsZero() {
			s.mu.Lock()
			s.status.LastSync = cached.LastSync
			s.mu.Unlock()
		}
		if len(cached.SyncHistory) > 0 {
			s.mu.Lock()
			s.syncHistory = cached.SyncHistory
			s.mu.Unlock()
		}
	}

	interval := s.cfg.Sync.ParsedInterval
	delay := firstSyncDelay(s.cfg.Sync.OnStartup, interval, lastSync, cachedItems, time.Now())
	if s.cfg.Sync.OnStartup && delay > 0 {
		slog.Info("index cache is fresh, skipping startup sync",
			"last_sync", lastSync, "next_sync_in", delay.Round(time.Second))
	}
	s.mu.Lock()
	s.status.NextSync = time.Now().Add(delay)
	s.mu.Unlock()

	go s.loop(ctx, delay)
}

// firstSyncDelay returns how long to wait before the first sync after start.
//
// With on_startup, a restart used to trigger a full sync every time, even
// seconds after the previous one finished. Now a restart that finds a
// populated cache younger than one interval waits until that cache is due,
// exactly as if the process had kept running. An empty or undated cache, or
// one older than the interval, still syncs immediately. A sync can always be
// forced from the web UI.
func firstSyncDelay(onStartup bool, interval time.Duration, lastSync time.Time, cachedItems int, now time.Time) time.Duration {
	if !onStartup {
		return interval
	}
	if cachedItems == 0 || lastSync.IsZero() {
		return 0
	}
	if remaining := lastSync.Add(interval).Sub(now); remaining > 0 {
		return remaining
	}
	return 0
}

// Stop stops the scheduler.
func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
}

// Status returns the current sync status (safe for concurrent reads).
func (s *Scheduler) Status() Status {
	movies, series := s.idx.Counts()
	s.mu.RLock()
	st := s.status
	s.mu.RUnlock()
	st.TotalMovies = movies
	st.TotalSeries = series
	st.GraceCycles = s.cfg.Sync.GraceCycles
	return st
}

// SyncHistory returns a copy of the rolling sync run history (most recent first).
func (s *Scheduler) SyncHistory() []SyncRun {
	s.mu.RLock()
	out := make([]SyncRun, len(s.syncHistory))
	copy(out, s.syncHistory)
	s.mu.RUnlock()
	return out
}

// Sync performs a full catalog sync: fetch → enrich → replace index.
// If a sync is already in progress, this call returns immediately (3B).
func (s *Scheduler) Sync(ctx context.Context) error {
	// 3B: Try to acquire the sync mutex; skip if already running
	if !s.syncMu.TryLock() {
		slog.Info("sync already in progress, skipping")
		return nil
	}
	defer s.syncMu.Unlock()

	startedAt := time.Now()
	s.setRunning(true, "")
	slog.Info("sync started")

	var syncErr error
	var retainedCount, expiredCount int

	defer func() {
		durationMs := time.Since(startedAt).Milliseconds()
		s.setRunning(false, "")
		now := time.Now()
		s.mu.Lock()
		s.status.LastSync = now
		s.status.NextSync = now.Add(s.cfg.Sync.ParsedInterval)
		s.status.LastSyncDurationMs = durationMs
		s.status.GraceRetained = retainedCount
		s.status.LastExpired = expiredCount
		s.mu.Unlock()
	}()

	items, excludedKeys, err := s.fetchAll(ctx)
	if err != nil {
		errMsg := err.Error()
		s.mu.Lock()
		s.status.Error = errMsg
		s.mu.Unlock()
		s.appendSyncRun(SyncRun{
			StartedAt:  startedAt,
			DurationMs: time.Since(startedAt).Milliseconds(),
			Error:      errMsg,
		})
		return fmt.Errorf("fetch: %w", err)
	}

	// Build enrichment skip map from the previous sync's cache. Items whose
	// name and type are unchanged can reuse their IDs without hitting TMDB.
	var cachedByKey map[string]*index.Item
	var allCachedItems []*index.Item
	if cached, err := LoadIndexCache(s.cachePath); err == nil && cached != nil {
		allCachedItems = cached.Items
		cachedByKey = make(map[string]*index.Item, len(cached.Items))
		for _, ci := range cached.Items {
			cachedByKey[ci.Key()] = ci
		}
	}

	enriched, err := s.enrich(ctx, items, cachedByKey)
	if err != nil {
		// Enrichment errors are non-fatal; we log and use what we have
		slog.Warn("enrichment completed with errors", "error", err)
		syncErr = err
	}

	// Count enriched vs unenriched after enrichment step.
	enrichedCount := 0
	unenrichedCount := 0
	for _, item := range enriched {
		if item.IMDBId != "" || item.TVDBId != "" {
			enrichedCount++
		} else {
			unenrichedCount++
		}
	}

	// Advance the generation counter and stamp all fresh items as current.
	s.syncGen++
	for _, item := range enriched {
		item.LastSeenSync = s.syncGen
		item.MissingSince = 0
	}

	// Apply grace period: retain cached items not seen in this sync for
	// up to GraceCycles syncs before expiring them.
	graceCycles := s.cfg.Sync.GraceCycles
	merged := enriched

	if graceCycles > 0 && len(allCachedItems) > 0 {
		// Build a key set of items present in this sync.
		freshKeys := make(map[string]struct{}, len(enriched))
		for _, item := range enriched {
			freshKeys[item.Key()] = struct{}{}
		}

		var retained []*index.Item
		var expired []*index.Item

		for _, ci := range allCachedItems {
			key := ci.Key()
			if _, ok := freshKeys[key]; ok {
				// Item is present in this sync — already in enriched list.
				continue
			}
			if excludedKeys[key] || s.isExcluded(ci) {
				// Left out on purpose, not missing: drop it from the index
				// without a grace period and without touching files on disk
				// (an excluded copy can share a folder with a kept one).
				continue
			}
			// Item is missing from this sync.
			if ci.MissingSince == 0 {
				ci.MissingSince = s.syncGen
			}
			missedFor := s.syncGen - ci.MissingSince
			if missedFor < graceCycles {
				retained = append(retained, ci)
			} else {
				expired = append(expired, ci)
			}
		}

		retainedCount = len(retained)
		expiredCount = len(expired)

		if len(retained) > 0 {
			slog.Info("grace period: retaining items temporarily missing from provider",
				"count", len(retained))
			merged = append(merged, retained...)
		}
		if len(expired) > 0 {
			slog.Info("grace period: expiring items missing for too long",
				"count", len(expired), "grace_cycles", graceCycles)
			s.cleanupExpired(expired)
		}
	}

	// A manual match set while this sync was running may have been
	// overwritten by an enrichment that started before it; re-apply.
	s.reapplyOverrides(ctx, merged)

	s.idx.Replace(merged)
	movies, series := s.idx.Counts()
	slog.Info("sync complete", "movies", movies, "series", series)

	// Update unenriched count and sync generation in status.
	s.mu.Lock()
	s.status.UnenrichedCount = unenrichedCount
	s.status.SyncGen = s.syncGen
	s.mu.Unlock()

	// appendSyncRun before SaveIndexCache so the current run is included in
	// the persisted cache and survives a restart before the next sync.
	errStr := ""
	if syncErr != nil {
		errStr = syncErr.Error()
	}
	s.appendSyncRun(SyncRun{
		StartedAt:  startedAt,
		DurationMs: time.Since(startedAt).Milliseconds(),
		Found:      len(items),
		Enriched:   enrichedCount,
		Unenriched: unenrichedCount,
		Retained:   retainedCount,
		Expired:    expiredCount,
		Error:      errStr,
	})

	now := time.Now()

	// Bracketing lines around the cache write, with heap figures.
	//
	// This function was where the process died: encoding the whole catalogue in
	// memory OOM-killed it inside SaveIndexCache on every completed sync, and a
	// SIGKILL leaves nothing behind, so "index cache save failed" was never
	// reached and there was no evidence at all in the container log. The
	// mechanism was only ever visible in the scope unit's journald entry.
	//
	// The signal is therefore the ABSENCE of the closing line: an
	// "index cache save" with no matching "index cache saved" means the process
	// died mid-write. heap_mb is what turns the memory limit from a guess into
	// a measured number, since the live index size has never been recorded.
	var msBefore runtime.MemStats
	runtime.ReadMemStats(&msBefore)
	slog.Info("index cache save", "items", len(merged),
		"heap_mb", msBefore.HeapAlloc/(1<<20), "sys_mb", msBefore.Sys/(1<<20))

	saveStart := time.Now()
	if err := SaveIndexCache(s.cachePath, merged, s.syncGen, now, s.SyncHistory()); err != nil {
		slog.Warn("index cache save failed", "error", err)
	} else {
		var msAfter runtime.MemStats
		runtime.ReadMemStats(&msAfter)
		slog.Info("index cache saved", "items", len(merged),
			"took_ms", time.Since(saveStart).Milliseconds(),
			"heap_mb", msAfter.HeapAlloc/(1<<20), "sys_mb", msAfter.Sys/(1<<20))
	}

	if s.afterSync != nil {
		go func() {
			hookCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			s.afterSync(hookCtx)
		}()
	}

	return nil
}

// appendSyncRun prepends run to syncHistory, capping at syncHistoryCap entries.
func (s *Scheduler) appendSyncRun(run SyncRun) {
	s.mu.Lock()
	s.syncHistory = append([]SyncRun{run}, s.syncHistory...)
	if len(s.syncHistory) > syncHistoryCap {
		s.syncHistory = s.syncHistory[:syncHistoryCap]
	}
	s.mu.Unlock()
}

// cleanupExpired removes .strm/.mkv directories for items that have exceeded
// the grace period and are no longer retained in the index.
func (s *Scheduler) cleanupExpired(expired []*index.Item) {
	if s.writer == nil {
		return
	}
	for _, item := range expired {
		var err error
		switch item.Type {
		case index.TypeMovie:
			err = s.writer.RemoveMovie(item.Name, item.Year)
		case index.TypeSeries:
			err = s.writer.RemoveSeries(item.Name)
		}
		if err != nil {
			slog.Warn("cleanup expired item failed", "name", item.Name, "type", item.Type, "error", err)
		} else {
			slog.Info("cleaned up expired item", "name", item.Name, "type", item.Type)
		}
	}
}

func (s *Scheduler) setRunning(running bool, errMsg string) {
	s.mu.Lock()
	s.status.Running = running
	if !running {
		s.status.Error = errMsg
	}
	s.mu.Unlock()
}

// loop runs the first sync after firstDelay, then one every interval.
func (s *Scheduler) loop(ctx context.Context, firstDelay time.Duration) {
	timer := time.NewTimer(firstDelay)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := s.Sync(ctx); err != nil {
				slog.Error("scheduled sync failed", "error", err)
			}
			timer.Reset(s.cfg.Sync.ParsedInterval)
		}
	}
}

// fetchAll retrieves the full VOD + series catalog from every provider.
//
// Items in an excluded provider group are left out; their keys are returned
// in excluded so the grace period does not keep them alive. When one of
// several providers fails, the others are still synced and its titles are
// kept by the grace period, as for a brief outage.
func (s *Scheduler) fetchAll(ctx context.Context) (items []*index.Item, excluded map[string]bool, err error) {
	excluded = make(map[string]bool)
	excludedCats := s.excludedCategories()
	sources := s.sources()

	var firstErr error
	synced := 0
	for _, src := range sources {
		got, err := s.fetchProvider(ctx, src, excludedCats, excluded)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			if len(sources) == 1 {
				return nil, nil, err
			}
			slog.Warn("provider sync failed; its titles are kept for the grace period", "provider", src.name, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		synced++
		items = append(items, got...)
	}
	if synced == 0 {
		return nil, nil, firstErr
	}
	if len(excluded) > 0 {
		slog.Info("excluded provider groups", "items_skipped", len(excluded))
	}
	return items, excluded, nil
}

// fetchProvider retrieves one provider's VOD + series catalog. Its items
// carry the provider; keys of excluded items are added to excluded.
func (s *Scheduler) fetchProvider(ctx context.Context, src source, excludedCats, excluded map[string]bool) (items []*index.Item, err error) {
	xc := src.client
	// Group names are only for display; a failure here is not fatal.
	vodCats := s.categoryNames(ctx, xc.GetVODCategories)
	seriesCats := s.categoryNames(ctx, xc.GetSeriesCategories)
	label := func(stage string) string {
		if src.id == "" {
			return stage
		}
		return stage + " (" + src.name + ")"
	}

	// --- VOD ---
	s.setProgress(label("Fetching VOD catalog"), 0, 0)
	allStreams, err := xc.GetVODStreams(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("get vod streams: %w", err)
	}
	slog.Debug("fetched vod streams", "count", len(allStreams))

	streams := allStreams[:0:0]
	for _, st := range allStreams {
		if excludedCats[src.catKey(vodCategoryKey(st.CategoryID))] {
			excluded[src.itemKey(index.TypeMovie, st.ID.Int())] = true
			continue
		}
		streams = append(streams, st)
	}

	for i, st := range streams {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		s.setProgress(label("Fetching VOD catalog"), i+1, len(streams))

		year := st.Year.String()
		if year == "" {
			// Use extractNameYear (strips quality markers before matching) so
			// that "Movie - 2016 [DOLBY]" correctly yields "2016" rather than "".
			year = extractNameYear(st.Name, s.userPatterns)
		}
		item := &index.Item{
			Type:         index.TypeMovie,
			XtreamID:     st.ID.Int(),
			Name:         st.Name,
			Year:         year,
			Plot:         st.Plot,
			Genre:        st.Genre,
			Rating:       float64(st.Rating),
			Poster:       st.Poster,
			ReleaseDate:  st.ReleaseDate,
			ContainerExt: st.ContainerExt,
			Duration:     parseDuration(st.Duration),
			CategoryKey:  src.catKey(vodCategoryKey(st.CategoryID)),
			Category:     vodCats[st.CategoryID],
			Provider:     src.id,
			ProviderName: src.extraName(),
		}
		if st.TMDBId.Int() > 0 {
			item.TMDBId = strconv.Itoa(st.TMDBId.Int())
			item.ProviderTMDBId = item.TMDBId
		}
		items = append(items, item)
	}

	// --- Series ---
	s.setProgress(label("Fetching series catalog"), 0, 0)
	allSeries, err := xc.GetSeries(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("get series: %w", err)
	}
	slog.Debug("fetched series", "count", len(allSeries))

	// Drop excluded groups before fetching episode lists for them.
	series := allSeries[:0:0]
	for _, sr := range allSeries {
		if excludedCats[src.catKey(seriesCategoryKey(sr.CategoryID))] {
			excluded[src.itemKey(index.TypeSeries, sr.SeriesID.Int())] = true
			continue
		}
		series = append(series, sr)
	}

	// Build a lastModified lookup from the bulk response.
	lastModifiedByID := make(map[int]string, len(series))
	for _, sr := range series {
		lastModifiedByID[sr.SeriesID.Int()] = sr.LastModified
	}

	// Load previous snapshot for smart-skip.
	snapPath := src.snapshotPath(s.cfg.Output.Path)
	prevSnap, err := LoadSnapshot(snapPath)
	if err != nil {
		slog.Warn("snapshot load failed, rebuilding", "error", err)
	}

	// Partition: series we can reconstruct from the snapshot vs. those that need a fetch.
	type seriesWork struct {
		series xtream.Series
	}
	skippedItems := make(map[int]*index.Item, len(series))
	fetchedAt := make(map[int]time.Time, len(series))
	var toFetch []xtream.Series
	now := time.Now()

	for _, sr := range series {
		id := sr.SeriesID.Int()
		if prevSnap != nil {
			if prev, ok := prevSnap.Series[id]; ok {
				cs := SeriesChecksum(sr.Name, sr.LastModified, prev.EpisodeCount)
				if cs == prev.Checksum && len(prev.Episodes) > 0 && !seriesStale(id, prev.FetchedAt, now) {
					item := buildSeriesItem(sr)
					item.Episodes = prev.Episodes
					applyInfoTag(item, prev.TMDBId)
					skippedItems[id] = item
					fetchedAt[id] = prev.FetchedAt
					continue
				}
			}
		}
		toFetch = append(toFetch, sr)
	}

	slog.Info("series smart skip", "skipped", len(skippedItems), "fetch", len(toFetch))
	s.setProgress(label("Fetching series details"), 0, len(toFetch))

	// Worker pool for series that need a GetSeriesInfo call.
	parallelism := s.cfg.Sync.Parallelism
	if len(toFetch) < parallelism {
		parallelism = len(toFetch)
	}
	if parallelism < 1 {
		parallelism = 1
	}

	workCh := make(chan seriesWork, len(toFetch))
	for _, sr := range toFetch {
		workCh <- seriesWork{sr}
	}
	close(workCh)

	fetchedItems := make(map[int]*index.Item, len(toFetch))
	var fetchMu gosync.Mutex
	var progressN int64

	var wg gosync.WaitGroup
	for w := 0; w < parallelism; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for work := range workCh {
				if ctx.Err() != nil {
					return
				}
				sr := work.series
				item := buildSeriesItem(sr)

				info, err := xc.GetSeriesInfo(ctx, sr.SeriesID.Int())
				if err == nil {
					applyInfoTag(item, info.Info.TMDBId.Int())
					for seasonStr, eps := range info.Episodes {
						season, _ := strconv.Atoi(seasonStr)
						for _, ep := range eps {
							item.Episodes = append(item.Episodes, index.EpisodeItem{
								EpisodeID:  ep.ID.Int(),
								Season:     season,
								EpisodeNum: ep.EpisodeNum.Int(),
								Title:      ep.Title,
								Ext:        ep.ContainerExt,
								Duration:   parseDuration(ep.Info.Duration),
								FileSize:   estimateEpisodeFileSize(ep.Info.Bitrate.Int(), ep.Info.DurationSecs.Int()),
							})
						}
					}
					// 5D: Sort episodes by season then episode number for consistent ordering
					sort.Slice(item.Episodes, func(i, j int) bool {
						if item.Episodes[i].Season != item.Episodes[j].Season {
							return item.Episodes[i].Season < item.Episodes[j].Season
						}
						return item.Episodes[i].EpisodeNum < item.Episodes[j].EpisodeNum
					})
				} else {
					// 4D: Log series info fetch failures
					slog.Warn("series info fetch failed", "series_id", sr.SeriesID.Int(), "error", err)
				}

				n := atomic.AddInt64(&progressN, 1)
				s.setProgress(label("Fetching series details"), int(n), len(toFetch))

				fetchMu.Lock()
				fetchedItems[sr.SeriesID.Int()] = item
				if err == nil {
					fetchedAt[sr.SeriesID.Int()] = now
				} else if prev, ok := prevSnap.entry(sr.SeriesID.Int()); ok && len(prev.Episodes) > 0 {
					// A failed refetch keeps the last good list rather than
					// dropping every episode; its old fetch time retries it next sync.
					item.Episodes = prev.Episodes
					applyInfoTag(item, prev.TMDBId)
					fetchedAt[sr.SeriesID.Int()] = prev.FetchedAt
				}
				fetchMu.Unlock()
			}
		}()
	}
	wg.Wait()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Merge skipped + fetched in original bulk order.
	for _, sr := range series {
		id := sr.SeriesID.Int()
		item, ok := skippedItems[id]
		if !ok {
			item, ok = fetchedItems[id]
		}
		if ok {
			item.CategoryKey = src.catKey(seriesCategoryKey(sr.CategoryID))
			item.Category = seriesCats[sr.CategoryID]
			item.Provider = src.id
			item.ProviderName = src.extraName()
			items = append(items, item)
		}
	}

	// Persist snapshot for the next sync.
	newSnap := &Snapshot{
		Timestamp: time.Now(),
		Movies:    make(map[int]MovieEntry, len(streams)),
		Series:    make(map[int]SeriesEntry, len(series)),
	}
	for _, st := range streams {
		id := st.ID.Int()
		newSnap.Movies[id] = MovieEntry{
			Name:     st.Name,
			Checksum: MovieChecksum(st.Name, st.ContainerExt),
		}
	}
	for _, sr := range series {
		id := sr.SeriesID.Int()
		var eps []index.EpisodeItem
		if item, ok := skippedItems[id]; ok {
			eps = item.Episodes
		} else if item, ok := fetchedItems[id]; ok {
			eps = item.Episodes
		}
		lm := lastModifiedByID[id]
		newSnap.Series[id] = SeriesEntry{
			Name:         sr.Name,
			LastModified: lm,
			EpisodeCount: len(eps),
			Checksum:     SeriesChecksum(sr.Name, lm, len(eps)),
			Episodes:     eps,
			FetchedAt:    fetchedAt[id],
			TMDBId:       infoTag(skippedItems[id], fetchedItems[id]),
		}
	}
	if err := SaveSnapshot(snapPath, newSnap); err != nil {
		slog.Warn("snapshot save failed", "error", err)
	}

	return items, nil
}

// applyInfoTag gives a series the TMDB ID its get_series_info tagged it
// with, when the bulk series list (which buildSeriesItem reads) did not.
func applyInfoTag(item *index.Item, tmdbID int) {
	if item.TMDBId == "" && tmdbID > 0 {
		item.TMDBId = strconv.Itoa(tmdbID)
		item.ProviderTMDBId = item.TMDBId
	}
}

// infoTag returns the provider's TMDB tag of whichever item is set, as an int
// for the snapshot.
func infoTag(items ...*index.Item) int {
	for _, it := range items {
		if it != nil {
			n, _ := strconv.Atoi(it.ProviderTMDBId)
			return n
		}
	}
	return 0
}

// buildSeriesItem constructs a series index.Item from bulk Xtream metadata
// (without episode data, which must be added separately).
func buildSeriesItem(sr xtream.Series) *index.Item {
	item := &index.Item{
		Type:        index.TypeSeries,
		XtreamID:    sr.SeriesID.Int(),
		Name:        sr.Name,
		Plot:        sr.Plot,
		Genre:       sr.Genre,
		Rating:      float64(sr.Rating),
		Poster:      sr.Cover,
		ReleaseDate: sr.ReleaseDate,
	}
	if len(sr.ReleaseDate) >= 4 {
		item.Year = sr.ReleaseDate[:4]
	} else if y := extractTrailingYear(sr.Name); y != "" {
		item.Year = y
	}
	if sr.TMDBId.Int() > 0 {
		item.TMDBId = strconv.Itoa(sr.TMDBId.Int())
		item.ProviderTMDBId = item.TMDBId
	}
	return item
}

// enrich resolves TMDB → IMDB + TVDB IDs for items that have a TMDB ID.
// It uses a worker pool to overlap HTTP latency while the TMDB client's
// internal ticker naturally enforces the 30 req/s rate limit.
//
// cachedByKey is an optional map (keyed by "type:xtreamID") of previously
// enriched items. Items found in the cache whose name is unchanged skip TMDB.
func (s *Scheduler) enrich(ctx context.Context, items []*index.Item, cachedByKey map[string]*index.Item) ([]*index.Item, error) {
	if s.cfg.TMDB.APIKey == "" {
		slog.Warn("TMDB API key not set; skipping enrichment")
		return items, nil
	}

	conflicting := conflictingTVDBIDs(cachedByKey)
	if len(conflicting) > 0 {
		slog.Info("re-matching series that share a TVDB ID with a different show", "tvdb_ids", len(conflicting))
	}

	total := len(items)

	parallelism := s.cfg.Sync.Parallelism
	if total < parallelism {
		parallelism = total
	}
	if parallelism < 1 {
		parallelism = 1
	}

	workCh := make(chan *index.Item, total)
	for _, item := range items {
		workCh <- item
	}
	close(workCh)

	var (
		progressN int64
		errMu     gosync.Mutex
		lastErr   error
	)

	var wg gosync.WaitGroup
	for w := 0; w < parallelism; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range workCh {
				if ctx.Err() != nil {
					return
				}

				// A manual match always wins over automatic matching.
				if ov, ok := s.override(item.Key()); ok {
					if err := s.applyOverride(ctx, item, ov); err != nil {
						errMu.Lock()
						lastErr = err
						errMu.Unlock()
					}
					n := atomic.AddInt64(&progressN, 1)
					s.setProgress("Enriching via TMDB", int(n), total)
					continue
				}

				// Reuse cached IDs for unchanged items to avoid redundant TMDB calls.
				// Always copy IDs from cache to preserve them even if a fresh title
				// search later fails. Skip enrichment only when CanonicalName is
				// also set — items cached before that feature get re-enriched once.
				if cachedByKey != nil {
					key := item.Key()
					// Items matched by title under an older algorithm are
					// re-enriched from scratch so a wrong cached ID can be
					// corrected. Items the provider tags with a TMDB ID never
					// went through title matching, so they keep their cache.
					ci, ok := cachedByKey[key]
					stale := ok && item.TMDBId == "" && ci.MatchVersion < matchVersion
					// A TVDB ID shared with an entry for a different TMDB show
					// was borrowed by an older TVDB name search: re-match.
					stale = stale || (ok && conflicting[ci.TVDBId] && !ci.ManualMatch)
					// The provider has tagged the item since it was matched, or
					// changed its tag: match by the tag rather than the old result.
					stale = stale || (ok && item.TMDBId != "" && ci.ProviderTMDBId != item.TMDBId && ci.TMDBId != item.TMDBId && !ci.ManualMatch)
					if ok && ci.Name == item.Name && !stale {
						if ci.IMDBId != "" || ci.TVDBId != "" {
							item.IMDBId = ci.IMDBId
							item.TVDBId = ci.TVDBId
							item.TMDBId = ci.TMDBId
							item.CanonicalName = ci.CanonicalName
							item.RuntimeMins = ci.RuntimeMins
							if ci.Year != "" && item.Year == "" {
								item.Year = ci.Year
							}
							if ci.ReleaseDate != "" && item.ReleaseDate == "" {
								item.ReleaseDate = ci.ReleaseDate
							}
							if ci.CanonicalName != "" && item.Year != "" {
								n := atomic.AddInt64(&progressN, 1)
								s.setProgress("Enriching via TMDB", int(n), total)
								continue
							}
							// CanonicalName or Year empty: fall through to fetch it.
						}
					}
				}

				// Save the provider-supplied TMDBId before any title search so we
				// can detect later when a provider ID failed to yield an IMDB match.
				providerTMDBId := item.TMDBId

				// Title search: only for items with no TMDBId yet (new items or
				// those the provider never tagged). Never call resolveByTitle when
				// we already have a TMDBId — a title search could overwrite a
				// correct provider-supplied ID with a wrong match.
				if item.TMDBId == "" && item.Type == index.TypeMovie {
					// The bulk VOD list often omits the tag get_vod_info has.
					if tag := s.vodInfoTag(ctx, item); tag > 0 {
						item.TMDBId = strconv.Itoa(tag)
						item.ProviderTMDBId = item.TMDBId
						providerTMDBId = item.TMDBId
					}
				}
				if item.TMDBId == "" {
					if err := s.resolveByTitle(ctx, item); err != nil {
						slog.Debug("title resolve failed", "name", item.Name, "error", err)
					}
				}

				if item.TMDBId != "" {
					tmdbID, err := strconv.Atoi(item.TMDBId)
					if err == nil && tmdbID > 0 {
						var extIDs *tmdb.ExternalIDs
						switch item.Type {
						case index.TypeMovie:
							extIDs, err = s.tmdb.GetMovieExternalIDs(ctx, tmdbID)
						case index.TypeSeries:
							extIDs, err = s.tmdb.GetTVExternalIDs(ctx, tmdbID)
						}
						if err != nil {
							errMu.Lock()
							lastErr = err
							errMu.Unlock()
							slog.Debug("external ids lookup failed", "tmdb_id", tmdbID, "error", err)
						} else if extIDs != nil {
							if extIDs.IMDBID != "" {
								item.IMDBId = extIDs.IMDBID
							}
							if extIDs.TVDBID > 0 {
								item.TVDBId = strconv.Itoa(extIDs.TVDBID)
							}
						}

						// Fetch CanonicalName, RuntimeMins, and Year by ID when still
						// missing (provider supplied TMDBId directly without a title
						// search, e.g. Dutch VOD streams, or cache predates Year).
						if item.CanonicalName == "" || item.Year == "" {
							var canonTitle string
							var titleErr error
							switch item.Type {
							case index.TypeMovie:
								var movieDetails *tmdb.MovieDetails
								movieDetails, titleErr = s.tmdb.GetMovieDetails(ctx, tmdbID)
								if movieDetails != nil {
									canonTitle = movieDetails.Title
									item.RuntimeMins = movieDetails.RuntimeMins
									if item.ReleaseDate == "" && movieDetails.ReleaseDate != "" {
										item.ReleaseDate = movieDetails.ReleaseDate
									}
									if item.Year == "" && len(movieDetails.ReleaseDate) >= 4 {
										item.Year = movieDetails.ReleaseDate[:4]
									}
								}
							case index.TypeSeries:
								canonTitle, titleErr = s.tmdb.GetTVTitle(ctx, tmdbID)
							}
							if titleErr != nil {
								slog.Debug("canonical name fetch failed", "tmdb_id", tmdbID, "error", titleErr)
							} else if canonTitle != "" {
								item.CanonicalName = canonTitle
							}
						}

						// Year-conflict check: if the raw name contains an explicit trailing
						// year that differs from the TMDB result year, the provider TMDBId
						// is pointing to the wrong movie (e.g. a remake vs. the original).
						// Clear the provider IDs and retry via title search with the name year.
						if item.Type == index.TypeMovie && providerTMDBId != "" {
							nameYear := extractNameYear(item.Name, s.userPatterns)
							tmdbYear := ""
							if len(item.ReleaseDate) >= 4 {
								tmdbYear = item.ReleaseDate[:4]
							}
							if nameYear != "" && tmdbYear != "" && nameYear != tmdbYear {
								slog.Debug("provider TMDBId year conflict, retrying via title search",
									"name", item.Name,
									"name_year", nameYear,
									"tmdb_year", tmdbYear,
									"provider_tmdb_id", providerTMDBId,
								)
								item.TMDBId = ""
								item.CanonicalName = ""
								item.IMDBId = ""
								item.TVDBId = ""
								item.Year = nameYear
								if err := s.resolveByTitle(ctx, item); err != nil {
									slog.Debug("year-conflict title retry failed", "name", item.Name, "error", err)
								}
								if item.TMDBId != "" {
									retryID, err := strconv.Atoi(item.TMDBId)
									if err == nil && retryID > 0 {
										retryExtIDs, err := s.tmdb.GetMovieExternalIDs(ctx, retryID)
										if err != nil {
											slog.Debug("year-conflict external ids fetch failed", "tmdb_id", retryID, "error", err)
										} else if retryExtIDs != nil {
											if retryExtIDs.IMDBID != "" {
												item.IMDBId = retryExtIDs.IMDBID
											}
										}
									}
								} else {
									// resolveByTitle found nothing -- restore provider ID so the item is
									// still traceable and won't re-enrich on every sync.
									item.TMDBId = providerTMDBId
								}
								// Prevent the no-IMDB fallback from triggering regardless of outcome --
								// the year-conflict retry already performed a targeted title search with
								// the correct year, so a year-unguided retry would risk re-selecting the
								// original wrong movie.
								providerTMDBId = ""
							}
						}
					}
				}

				// Fallback: provider-supplied TMDBId yielded no IMDB/TVDB ID (the
				// provider ID may be wrong or stale). Clear it and try a title
				// search instead, then re-fetch external IDs with the new ID.
				if item.IMDBId == "" && providerTMDBId != "" {
					slog.Debug("provider TMDBId yielded no IMDB ID, retrying via title search", "name", item.Name, "provider_tmdb_id", providerTMDBId)
					item.TMDBId = ""
					item.CanonicalName = ""
					if err := s.resolveByTitle(ctx, item); err != nil {
						slog.Debug("title fallback failed", "name", item.Name, "error", err)
					}
					if item.TMDBId != "" {
						// Title search found a (hopefully correct) ID — fetch external IDs.
						tmdbID, err := strconv.Atoi(item.TMDBId)
						if err == nil && tmdbID > 0 {
							var extIDs *tmdb.ExternalIDs
							switch item.Type {
							case index.TypeMovie:
								extIDs, err = s.tmdb.GetMovieExternalIDs(ctx, tmdbID)
							case index.TypeSeries:
								extIDs, err = s.tmdb.GetTVExternalIDs(ctx, tmdbID)
							}
							if err != nil {
								slog.Debug("external ids fallback lookup failed", "tmdb_id", tmdbID, "error", err)
							} else if extIDs != nil {
								if extIDs.IMDBID != "" {
									item.IMDBId = extIDs.IMDBID
								}
								if extIDs.TVDBID > 0 {
									item.TVDBId = strconv.Itoa(extIDs.TVDBID)
								}
							}
						}
					} else {
						// Title search found nothing — restore provider ID so the
						// item is still traceable and won't retry on every sync.
						item.TMDBId = providerTMDBId
					}
				}

				// TVDB fallback: any series without a TVDB ID is searched
				// directly on TVDB by title — covers both the case where
				// TMDB had no TVDB cross-link and the case where TMDB
				// found nothing at all (e.g. Dutch-only shows).
				if item.Type == index.TypeSeries && item.TVDBId == "" && s.tvdb != nil {
					if err := s.resolveByTVDB(ctx, item); err != nil {
						slog.Warn("TVDB fallback failed", "name", item.Name, "error", err)
					}
				}

				item.MatchVersion = matchVersion

				// Determine enrichment failure reason from the item's final state,
				// after all fallback stages have completed. Setting this at
				// intermediate failure points would be incorrect because a later
				// fallback stage may recover the item successfully.
				item.EnrichFailReason = ""
				if item.IMDBId == "" && item.TVDBId == "" {
					if item.TMDBId == "" {
						item.EnrichFailReason = "no TMDB match"
					} else {
						item.EnrichFailReason = "TMDB has no IMDB/TVDB cross-reference"
					}
				}

				n := atomic.AddInt64(&progressN, 1)
				s.setProgress("Enriching via TMDB", int(n), total)
			}
		}()
	}
	wg.Wait()

	if ctx.Err() != nil {
		return items, ctx.Err()
	}

	errMu.Lock()
	err := lastErr
	errMu.Unlock()

	return items, err
}

// resolveByTitle searches TMDB by title to find a TMDB ID.
// ctx is passed through so that sync cancellation (e.g. SIGTERM) propagates
// to TMDB search calls and avoids blocking shutdown on large catalogs.
func (s *Scheduler) resolveByTitle(ctx context.Context, item *index.Item) error {
	year := 0
	if item.Year != "" {
		if y, err := strconv.Atoi(item.Year); err == nil {
			year = y
		}
	}

	title := cleanTitleForSearch(item.Name, s.userPatterns)

	switch item.Type {
	case index.TypeMovie:
		// searchTMDBMovie also searches without the year, so no year-retry here.
		result, err := s.searchTMDBMovie(ctx, title, year, item.Duration)
		if err != nil {
			return err
		}
		if result != nil {
			item.TMDBId = strconv.Itoa(result.ID)
			if result.Title != "" {
				item.CanonicalName = result.Title
			}
			if item.ReleaseDate == "" && result.ReleaseDate != "" {
				item.ReleaseDate = result.ReleaseDate
			}
			if item.Year == "" && len(result.ReleaseDate) >= 4 {
				item.Year = result.ReleaseDate[:4]
			}
		}
	case index.TypeSeries:
		// searchTMDBSeries also searches without the year, so no year-retry here.
		result, err := s.searchTMDBSeries(ctx, title, year, maxSeason(item))
		if err != nil {
			return err
		}
		if result != nil {
			item.TMDBId = strconv.Itoa(result.ID)
			if result.Name != "" {
				item.CanonicalName = result.Name
			}
			if item.ReleaseDate == "" && result.FirstAirDate != "" {
				item.ReleaseDate = result.FirstAirDate
			}
			if item.Year == "" && len(result.FirstAirDate) >= 4 {
				item.Year = result.FirstAirDate[:4]
			}
		}
	}
	return nil
}

// vodInfoTag returns the TMDB ID a movie's get_vod_info tags it with, or 0.
// Each item is asked about at most once per run.
func (s *Scheduler) vodInfoTag(ctx context.Context, item *index.Item) int {
	key := item.Key()
	if v, ok := s.vodTags.Load(key); ok {
		return v.(int)
	}
	var xc *xtream.Client
	for _, src := range s.sources() {
		if src.id == item.Provider {
			xc = src.client
		}
	}
	if xc == nil {
		return 0
	}
	info, err := xc.GetVODInfo(ctx, item.XtreamID)
	if err != nil {
		slog.Debug("vod info fetch failed", "name", item.Name, "error", err)
		return 0 // not cached: retried next sync
	}
	tag := info.Info.TMDBId.Int()
	s.vodTags.Store(key, tag)
	return tag
}

// resolveByTVDB searches TVDB by title to obtain a TVDB ID for a series that
// TMDB enrichment could not resolve.
func (s *Scheduler) resolveByTVDB(ctx context.Context, item *index.Item) error {
	title := cleanTitleForSearch(item.Name, s.userPatterns)
	year, _ := strconv.Atoi(item.Year)
	result, err := s.searchTVDBSeries(ctx, title, year, item.IMDBId, item.TMDBId)
	if err != nil {
		return err
	}
	if result != nil {
		item.TVDBId = strconv.Itoa(result.TVDBID)
		if result.Name != "" && item.CanonicalName == "" {
			item.CanonicalName = result.Name
		}
	}
	return nil
}

func (s *Scheduler) setProgress(stage string, current, total int) {
	s.mu.Lock()
	s.status.Progress = Progress{Stage: stage, Current: current, Total: total}
	s.mu.Unlock()
}

// estimateEpisodeFileSize computes an estimated byte count from bitrate (kbps)
// and duration (seconds), matching the formula used for movies. Returns 0 if
// either field is absent (provider did not supply metadata).
func estimateEpisodeFileSize(bitrateKbps, durationSecs int) int64 {
	if bitrateKbps <= 0 || durationSecs <= 0 {
		return 0
	}
	return int64(bitrateKbps) * 1000 / 8 * int64(durationSecs)
}

// parseDuration parses an Xtream duration string into fractional seconds.
// Accepts "HH:MM:SS", "MM:SS", or a bare integer/float minute string.
func parseDuration(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	parts := strings.Split(s, ":")
	switch len(parts) {
	case 3: // HH:MM:SS
		h, _ := strconv.Atoi(parts[0])
		m, _ := strconv.Atoi(parts[1])
		sec, _ := strconv.ParseFloat(parts[2], 64)
		return float64(h*3600+m*60) + sec
	case 2: // MM:SS
		m, _ := strconv.Atoi(parts[0])
		sec, _ := strconv.ParseFloat(parts[1], 64)
		return float64(m*60) + sec
	default: // bare minutes
		if min, err := strconv.ParseFloat(s, 64); err == nil {
			return min * 60
		}
		return 0
	}
}
