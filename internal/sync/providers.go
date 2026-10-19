package sync

import (
	"path/filepath"

	"github.com/vodarr/vodarr/internal/index"
	"github.com/vodarr/vodarr/internal/xtream"
)

// source is one Xtream provider the sync reads. The primary provider has
// id ""; its keys, group keys and snapshot file are exactly as they were
// before extra providers, so existing caches and settings carry over.
type source struct {
	id     string
	name   string
	client *xtream.Client
}

// catKey qualifies a group key ("vod:12") with the provider: extra
// providers' group IDs are their own ID space.
func (src source) catKey(k string) string {
	if src.id == "" {
		return k
	}
	return src.id + "|" + k
}

// itemKey is the index key of this provider's item.
func (src source) itemKey(t index.MediaType, id int) string {
	return (&index.Item{Provider: src.id, Type: t, XtreamID: id}).Key()
}

// extraName is the name items carry for release groups: none for the
// primary provider, whose release names stay as they were.
func (src source) extraName() string {
	if src.id == "" {
		return ""
	}
	return src.name
}

// snapshotPath is the provider's series snapshot file. Each provider needs
// its own: snapshots are keyed by series ID, which providers share.
func (src source) snapshotPath(outputPath string) string {
	if src.id == "" {
		return SnapshotPath(outputPath)
	}
	return filepath.Join(outputPath, ".vodarr-snapshot-"+src.id+".json")
}

// sources lists the providers to sync: the primary first, then the extra
// ones from config.
func (s *Scheduler) sources() []source {
	out := []source{{id: "", name: s.primaryName(), client: s.xtream}}
	return append(out, s.extra...)
}

func (s *Scheduler) primaryName() string {
	if s.cfg != nil && s.cfg.Xtream.Name != "" {
		return s.cfg.Xtream.Name
	}
	return "Primary"
}

// SetProviders sets the extra providers' clients by provider ID, in config
// order. Call before Start.
func (s *Scheduler) SetProviders(ids, names []string, clients []*xtream.Client) {
	s.extra = nil
	for i := range ids {
		s.extra = append(s.extra, source{id: ids[i], name: names[i], client: clients[i]})
	}
}
