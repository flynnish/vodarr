package strm

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Reasons FinishImport left an import alone.
var (
	// ErrNotVisible: the imported file's folder does not exist here, which
	// almost always means the arr library is not mounted into VODarr at the
	// path arr uses.
	ErrNotVisible = errors.New("imported file not visible to VODarr")
	// ErrGone: the folder exists but the file does not: already finished,
	// renamed or deleted since.
	ErrGone = errors.New("imported file no longer exists")
	// ErrNotStub: the file is a real video, never touched.
	ErrNotStub = errors.New("imported file is not a VODarr stub")
	// ErrNoStrm: there is no .strm to put in its place.
	ErrNoStrm = errors.New("no .strm to place next to the imported file")
)

// FinishImport turns arr's import of a VODarr stub into a playable library
// entry: it copies the .strm VODarr wrote beside the source stub
// (sourceMkv, which must lie inside outputPath) next to the imported file
// (libMkv), then deletes the imported stub, so the .strm is the episode or
// movie for Jellyfin.
//
// arr must not import the .strm as an "extra file": arr deletes a file's
// extras whenever that file is deleted or found missing, which would take
// the .strm along with the stub. A .strm arr does not know about is left
// alone.
//
// Only VODarr's own stubs are ever deleted (see IsStub), and never when no
// .strm would remain.
func FinishImport(libMkv, sourceMkv, outputPath string) error {
	if !filepath.IsAbs(libMkv) || !strings.HasSuffix(libMkv, ".mkv") {
		return fmt.Errorf("not an absolute .mkv path: %q", libMkv)
	}
	if _, err := os.Stat(libMkv); err != nil {
		if libraryVisible(libMkv) {
			return ErrGone
		}
		return ErrNotVisible
	}
	if !IsStub(libMkv) {
		return ErrNotStub
	}

	strmPath := strings.TrimSuffix(libMkv, ".mkv") + ".strm"
	if src := sourceStrm(sourceMkv, outputPath); src != "" {
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read source .strm: %w", err)
		}
		if err := os.WriteFile(strmPath, data, 0644); err != nil {
			return fmt.Errorf("write library .strm: %w", err)
		}
	}
	if _, err := os.Stat(strmPath); err != nil {
		return ErrNoStrm
	}
	if err := os.Remove(libMkv); err != nil {
		return fmt.Errorf("remove stub: %w", err)
	}
	return nil
}

// libraryVisible reports whether the library a missing file belonged to is
// visible here at all. arr lays files out as root/Show/Season NN/file or
// root/Movie/file, so the root is at most three levels up. If any of those
// folders exists the library is mounted and the file has merely gone
// (finished, renamed, or its show or season removed since), which is common
// for old history entries; only when none exists is the mount missing.
func libraryVisible(path string) bool {
	dir := filepath.Dir(path)
	for i := 0; i < 3; i++ {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return false
}

// NearestExisting returns the deepest existing folder above path, showing
// where a missing mount stops (e.g. "/media" when "/media/tv/strm" is not
// mounted).
func NearestExisting(path string) string {
	dir := filepath.Dir(path)
	for {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

// IsStub reports whether path is an .mkv stub written by Writer: a Matroska
// header whose muxing/writing app is "vodarr", followed by padding. Real
// media files never carry that marker.
func IsStub(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 4096)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	return bytes.HasPrefix(head, []byte{0x1A, 0x45, 0xDF, 0xA3}) && bytes.Contains(head, []byte("vodarr"))
}

// sourceStrm returns the .strm VODarr wrote beside the source stub arr
// imported from, or "" if there is none. It must lie inside outputPath:
// the path comes from an unauthenticated webhook.
func sourceStrm(sourcePath, outputPath string) string {
	if sourcePath == "" || outputPath == "" || !filepath.IsAbs(sourcePath) || !strings.HasSuffix(sourcePath, ".mkv") {
		return ""
	}
	src := filepath.Clean(strings.TrimSuffix(sourcePath, ".mkv") + ".strm")
	absOutput, err := filepath.Abs(outputPath)
	if err != nil {
		return ""
	}
	sep := string(filepath.Separator)
	if !strings.HasPrefix(src+sep, absOutput+sep) {
		return ""
	}
	if _, err := os.Stat(src); err != nil {
		return ""
	}
	return src
}
