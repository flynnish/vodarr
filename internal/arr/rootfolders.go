package arr

import (
	"context"

	"github.com/vodarr/vodarr/internal/config"
)

// RootFolders returns the library folders an instance imports into, as arr
// sees them. VODarr has them mounted at the same paths (the import webhook
// needs that), so they can be walked to rewrite the .strm files there.
func (s *Searcher) RootFolders(ctx context.Context, inst config.ArrInstance) ([]string, error) {
	var folders []struct {
		Path string `json:"path"`
	}
	if err := s.get(ctx, inst, "/api/v3/rootfolder", nil, &folders); err != nil {
		return nil, err
	}
	var out []string
	for _, f := range folders {
		if f.Path != "" {
			out = append(out, f.Path)
		}
	}
	return out, nil
}
