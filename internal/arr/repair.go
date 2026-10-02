package arr

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"github.com/vodarr/vodarr/internal/config"
	"github.com/vodarr/vodarr/internal/strm"
)

// RepairResult is what one instance's repair did.
type RepairResult struct {
	Instance   string `json:"instance"`
	Imports    int    `json:"imports"`     // imports found in arr's history
	Repaired   int    `json:"repaired"`    // .strm placed and stub removed now
	Done       int    `json:"done"`        // already finished (or renamed/removed since)
	NotVisible int    `json:"not_visible"` // library not mounted into VODarr
	NoStrm     int    `json:"no_strm"`     // no .strm to place (output folder path mismatch)
	Other      int    `json:"other"`       // imports from other download clients, skipped
	Failed     int    `json:"failed"`
	Error      string `json:"error,omitempty"`
}

// FinishFunc completes one import (strm.FinishImport bound to output.path).
type FinishFunc func(libMkv, sourceMkv string) error

type historyRecord struct {
	EpisodeID int               `json:"episodeId"`
	SeriesID  int               `json:"seriesId"`
	MovieID   int               `json:"movieId"`
	EventType string            `json:"eventType"`
	Data      map[string]string `json:"data"`
}

func dataValue(data map[string]string, key string) string {
	for k, v := range data {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

// RepairImports finishes past imports the webhook never handled (because it
// was not set up, could not see the library, or the import predates it). It
// reads the instance's import history, which records for every import where
// the file came from and where it went, finishes each VODarr stub still in
// the library, then unmonitors and rescans what it repaired. Real videos
// are never touched.
func (s *Searcher) RepairImports(ctx context.Context, inst config.ArrInstance, finish FinishFunc, unmonitor bool) RepairResult {
	res := RepairResult{Instance: inst.Name}
	seen := make(map[string]bool)
	episodes := make(map[int][]int) // series ID → repaired episode IDs
	var movies []int
	var notVisibleExample string

	for page := 1; page <= maxPages; page++ {
		var resp struct {
			TotalRecords int             `json:"totalRecords"`
			Records      []historyRecord `json:"records"`
		}
		q := url.Values{
			"page": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(pageSize)},
			"eventType": {"3"}, // DownloadFolderImported in both Sonarr and Radarr
			"sortKey":   {"date"}, "sortDirection": {"descending"},
		}
		if err := s.get(ctx, inst, "/api/v3/history", q, &resp); err != nil {
			res.Error = err.Error()
			return res
		}
		for _, rec := range resp.Records {
			if !strings.EqualFold(rec.EventType, "downloadFolderImported") {
				continue
			}
			imported := dataValue(rec.Data, "importedPath")
			if !strings.HasSuffix(imported, ".mkv") || seen[imported] {
				continue
			}
			seen[imported] = true
			err := finish(imported, dataValue(rec.Data, "droppedPath"))
			switch {
			case errors.Is(err, strm.ErrNotOurs):
				res.Other++
				continue // another download client's import
			case errors.Is(err, strm.ErrNotStub):
				continue // a real download from another client, not ours
			case err == nil:
				res.Repaired++
				if inst.Type == "sonarr" {
					episodes[rec.SeriesID] = append(episodes[rec.SeriesID], rec.EpisodeID)
				} else {
					movies = append(movies, rec.MovieID)
				}
			case errors.Is(err, strm.ErrGone):
				res.Done++
			case errors.Is(err, strm.ErrNotVisible):
				res.NotVisible++
				if notVisibleExample == "" {
					notVisibleExample = imported
				}
			case errors.Is(err, strm.ErrNoStrm):
				res.NoStrm++
			default:
				res.Failed++
				slog.Warn("repair: could not finish import", "instance", inst.Name, "path", imported, "error", err)
			}
			res.Imports++
		}
		if len(resp.Records) == 0 || page*pageSize >= resp.TotalRecords {
			break
		}
	}

	if notVisibleExample != "" {
		slog.Warn("repair: library not visible to VODarr; mount it at the same path arr uses",
			"instance", inst.Name, "count", res.NotVisible, "example", notVisibleExample,
			"nearest_visible_folder", strm.NearestExisting(notVisibleExample))
	}
	for seriesID, eps := range episodes {
		if err := s.Delivered(ctx, inst, Delivery{SeriesID: seriesID, EpisodeIDs: eps}, unmonitor); err != nil {
			slog.Warn("repair: could not update arr", "instance", inst.Name, "series_id", seriesID, "error", err)
		}
	}
	for _, movieID := range movies {
		if err := s.Delivered(ctx, inst, Delivery{MovieID: movieID}, unmonitor); err != nil {
			slog.Warn("repair: could not update arr", "instance", inst.Name, "movie_id", movieID, "error", err)
		}
	}
	slog.Info("repair past imports", "instance", inst.Name, "imports", res.Imports, "repaired", res.Repaired,
		"already_done", res.Done, "not_visible", res.NotVisible, "no_strm", res.NoStrm, "failed", res.Failed,
		"other_clients", res.Other)
	return res
}
