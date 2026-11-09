package sync

import (
	"encoding/json"
	"hash/fnv"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vodarr/vodarr/internal/index"
)

// Snapshot persists the last-known catalog state so that unchanged series
// can be skipped on the next sync without re-fetching their episode lists.
type Snapshot struct {
	Timestamp time.Time           `json:"timestamp"`
	Movies    map[int]MovieEntry  `json:"movies"`
	Series    map[int]SeriesEntry `json:"series"`
}

// MovieEntry holds the minimal state needed to detect changes in a VOD item.
type MovieEntry struct {
	Name     string `json:"name"`
	Checksum string `json:"checksum"`
}

// SeriesEntry holds the state needed to detect changes in a series and to
// reconstruct its episode list without a GetSeriesInfo API call.
type SeriesEntry struct {
	Name         string              `json:"name"`
	LastModified string              `json:"last_modified"`
	EpisodeCount int                 `json:"episode_count"`
	Checksum     string              `json:"checksum"`
	Episodes     []index.EpisodeItem `json:"episodes"`
	// FetchedAt is when the episode list was last fetched from the provider.
	// Zero in snapshots written before it existed, which makes them stale.
	FetchedAt time.Time `json:"fetched_at,omitempty"`
	// TMDBId is the TMDB ID the series' info tagged it with, which the bulk
	// series list often leaves out.
	TMDBId int `json:"tmdb_id,omitempty"`
}

// seriesRefreshAge is how long a series' episode list is reused before it is
// fetched again. Providers often add episodes without touching the series'
// last_modified, so the checksum alone would keep a stale list for ever.
const seriesRefreshAge = 24 * time.Hour

// seriesStale reports whether a series' cached episode list is due a refetch.
// The limit is spread over a further day by ID, so a catalog fetched in one
// go does not all come due in the same sync.
func seriesStale(id int, fetchedAt, now time.Time) bool {
	if id < 0 {
		id = -id
	}
	limit := seriesRefreshAge + time.Duration(id%24)*time.Hour
	return now.Sub(fetchedAt) >= limit
}

// MovieChecksum returns an FNV-1a fingerprint of a movie's key fields.
func MovieChecksum(name, ext string) string {
	h := fnv.New64a()
	h.Write([]byte(name))
	h.Write([]byte{0})
	h.Write([]byte(ext))
	return fmt.Sprintf("%016x", h.Sum64())
}

// SeriesChecksum returns an FNV-1a fingerprint of a series' key fields.
func SeriesChecksum(name, lastModified string, episodeCount int) string {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s\x00%s\x00%d", name, lastModified, episodeCount)
	return fmt.Sprintf("%016x", h.Sum64())
}

// entry returns the snapshot's entry for a series; s may be nil.
func (s *Snapshot) entry(id int) (SeriesEntry, bool) {
	if s == nil {
		return SeriesEntry{}, false
	}
	e, ok := s.Series[id]
	return e, ok
}

// SnapshotPath returns the canonical path for the snapshot file given the
// output directory.
func SnapshotPath(outputPath string) string {
	return filepath.Join(outputPath, ".vodarr-snapshot.json")
}

// LoadSnapshot reads a snapshot from path. Returns nil, nil if the file does
// not exist.
func LoadSnapshot(path string) (*Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// SaveSnapshot writes snap to path atomically (temp file + rename).
func SaveSnapshot(path string, snap *Snapshot) error {
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".vodarr-snapshot-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if err := os.Chmod(tmpName, 0600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
