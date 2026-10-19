package streamproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// provider serves /movie/user/secret/42.mkv (after a redirect, like real
// providers often do) with Range support.
func provider(t *testing.T) *httptest.Server {
	t.Helper()
	const body = "0123456789abcdefghij"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/movie/user/secret/42.mkv":
			http.Redirect(w, r, "/cdn/user/secret/42.mkv?token=x", http.StatusFound)
		case "/cdn/user/secret/42.mkv":
			http.ServeContent(w, r, "42.mkv", time.Time{}, strings.NewReader(body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newProxy(t *testing.T, upstreamBase string) *Proxy {
	t.Helper()
	key, err := LoadOrCreateKey(filepath.Join(t.TempDir(), "stream.key"))
	if err != nil {
		t.Fatal(err)
	}
	return New(key, "http://vodarr:9090/", func(kind string, id int, ext string) string {
		return upstreamBase + "/" + kind + "/user/secret/" + strconv.Itoa(id) + "." + ext
	})
}

func TestURLHidesCredentials(t *testing.T) {
	p := newProxy(t, "http://provider.example.com")
	u := p.URL("movie", 42, "mkv")
	if !strings.HasPrefix(u, "http://vodarr:9090/play/") || !strings.HasSuffix(u, "/movie/42.mkv") {
		t.Errorf("URL = %q", u)
	}
	if strings.Contains(u, "secret") || strings.Contains(u, "user") {
		t.Errorf("URL leaks the login: %q", u)
	}
}

func TestRelaysWithRangeAndFollowsRedirects(t *testing.T) {
	up := provider(t)
	p := newProxy(t, up.URL)
	path := strings.TrimPrefix(p.URL("movie", 42, "mkv"), "http://vodarr:9090")

	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Range", "bytes=5-9")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", w.Code)
	}
	if got := w.Body.String(); got != "56789" {
		t.Errorf("body = %q, want 56789", got)
	}
	if cr := w.Header().Get("Content-Range"); cr != "bytes 5-9/20" {
		t.Errorf("Content-Range = %q", cr)
	}
	// The provider's redirect was followed inside VODarr, not passed on.
	if loc := w.Header().Get("Location"); loc != "" {
		t.Errorf("leaked a provider redirect: %q", loc)
	}

	head := httptest.NewRecorder()
	p.ServeHTTP(head, httptest.NewRequest("HEAD", path, nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "20" {
		t.Errorf("HEAD: status %d, body %d bytes, length %q", head.Code, head.Body.Len(), head.Header().Get("Content-Length"))
	}
}

func TestRejectsForgedAndOtherStreams(t *testing.T) {
	p := newProxy(t, provider(t).URL)
	good := strings.TrimPrefix(p.URL("movie", 42, "mkv"), "http://vodarr:9090")

	for _, path := range []string{
		strings.Replace(good, "/42.mkv", "/43.mkv", 1),        // another stream, same signature
		strings.Replace(good, "/movie/", "/series/", 1),       // another kind
		"/play/00000000000000000000000000000000/movie/42.mkv", // forged
		"/play/" + strings.Repeat("a", 32) + "/live/1.ts",     // not a VOD kind
	} {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("POST", good, nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: status %d, want 405", w.Code)
	}
}

func TestKeyPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stream.key")
	k1, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(k1) != string(k2) {
		t.Error("key changed between loads: every proxied .strm would break")
	}
	if fi, _ := os.Stat(path); fi == nil {
		t.Error("key file not written")
	}
}

func TestStreamOutlivesServerWriteTimeout(t *testing.T) {
	// A provider that trickles a long stream; the server in front has a short
	// write timeout like VODarr's web server. The relay must not be cut off.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f := w.(http.Flusher)
		for i := 0; i < 6; i++ {
			w.Write([]byte("x"))
			f.Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer slow.Close()

	p := newProxy(t, slow.URL)
	front := httptest.NewUnstartedServer(p)
	front.Config.WriteTimeout = 200 * time.Millisecond
	front.Start()
	defer front.Close()

	resp, err := http.Get(front.URL + strings.TrimPrefix(p.URL("movie", 1, "mkv"), "http://vodarr:9090"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "xxxxxx" {
		t.Errorf("got %q, %v; want the whole stream past the write timeout", body, err)
	}
}
