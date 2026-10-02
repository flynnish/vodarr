package strm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestFinishImport(t *testing.T) {
	output := t.TempDir()
	src := filepath.Join(output, "tv", "Mr. D", "Season 07", "Mr.D.S07E07.WEB-DL.mkv")
	writeFile(t, strings.TrimSuffix(src, ".mkv")+".strm", []byte("http://provider/series/u/p/1.mkv\n"))

	library := t.TempDir()
	lib := filepath.Join(library, "Mr. D (2012)", "Season 7", "Mr. D - S07E07 - Work to Rule.mkv")
	writeFile(t, lib, BuildMKVHeader(nil))

	if err := FinishImport(lib, src, output); err != nil {
		t.Fatalf("FinishImport: %v", err)
	}
	if _, err := os.Stat(lib); !os.IsNotExist(err) {
		t.Error("stub not removed")
	}
	if got, _ := os.ReadFile(strings.TrimSuffix(lib, ".mkv") + ".strm"); string(got) != "http://provider/series/u/p/1.mkv\n" {
		t.Errorf("library .strm = %q", got)
	}

	// Running it again is harmless.
	if err := FinishImport(lib, src, output); !errors.Is(err, ErrGone) {
		t.Errorf("second run: %v, want ErrGone", err)
	}
}

func TestFinishImportReasons(t *testing.T) {
	output := t.TempDir()
	library := t.TempDir()

	// The library folder is not mounted here.
	unmounted := filepath.Join(t.TempDir(), "media", "tv", "strm", "Show", "Season 1", "Show - S01E01.mkv")
	if err := FinishImport(unmounted, "", output); !errors.Is(err, ErrNotVisible) {
		t.Errorf("unmounted: %v, want ErrNotVisible", err)
	}

	// A real video is never touched.
	real := filepath.Join(library, "Real - S01E01.mkv")
	writeFile(t, real, append([]byte{0x1A, 0x45, 0xDF, 0xA3}, []byte("libmatroska")...))
	writeFile(t, strings.TrimSuffix(real, ".mkv")+".strm", []byte("http://x"))
	if err := FinishImport(real, "", output); !errors.Is(err, ErrNotStub) {
		t.Errorf("real video: %v, want ErrNotStub", err)
	}
	if _, err := os.Stat(real); err != nil {
		t.Error("real video deleted")
	}

	// No .strm anywhere: keep the stub rather than leave nothing playable.
	stub := filepath.Join(library, "Show - S01E02.mkv")
	writeFile(t, stub, BuildMKVHeader(nil))
	if err := FinishImport(stub, filepath.Join(output, "missing.mkv"), output); !errors.Is(err, ErrNoStrm) {
		t.Errorf("no strm: %v, want ErrNoStrm", err)
	}
	if _, err := os.Stat(stub); err != nil {
		t.Error("stub deleted although no .strm exists")
	}

	// A source outside the output folder is not trusted.
	elsewhere := t.TempDir()
	writeFile(t, filepath.Join(elsewhere, "x.strm"), []byte("http://evil"))
	if err := FinishImport(stub, filepath.Join(elsewhere, "x.mkv"), output); !errors.Is(err, ErrNoStrm) {
		t.Errorf("foreign source: %v, want ErrNoStrm", err)
	}
}

func TestFinishImportGoneVersusNotVisible(t *testing.T) {
	// Library mounted, but an old history entry's season folder was renamed
	// or removed since: that is "gone", not "not visible".
	root := filepath.Join(t.TempDir(), "media", "tv", "strm")
	if err := os.MkdirAll(filepath.Join(root, "Bake Off (2010)"), 0755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(root, "Bake Off (2010)", "Season 17", "Bake Off - S17E01.mkv")
	if err := FinishImport(old, "", t.TempDir()); !errors.Is(err, ErrGone) {
		t.Errorf("removed season folder: %v, want ErrGone", err)
	}
	removedShow := filepath.Join(root, "Gone Show", "Season 1", "Gone Show - S01E01.mkv")
	if err := FinishImport(removedShow, "", t.TempDir()); !errors.Is(err, ErrGone) {
		t.Errorf("removed show folder: %v, want ErrGone", err)
	}
	if got := NearestExisting(removedShow); got != root {
		t.Errorf("NearestExisting = %q, want %q", got, root)
	}
}
