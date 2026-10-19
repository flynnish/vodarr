// Package streamproxy keeps provider credentials out of .strm files.
//
// An Xtream stream URL embeds the account's username and password, so a
// .strm file holding one exposes them to anyone who can read the library,
// and to media server logs. With the proxy on, a .strm holds a VODarr URL
// instead (http://vodarr:9090/play/<sig>/movie/123.mkv); VODarr builds the
// provider URL itself and relays the stream, so the credentials never leave
// VODarr. <sig> is an HMAC of the stream, so the URLs cannot be guessed or
// enumerated to stream other titles through VODarr.
package streamproxy

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Upstream builds the provider URL for a stream ("movie" or "series" kind;
// episodes use the series kind with their episode ID), as
// xtream.Client.BuildStreamURL does. It returns "" for an unknown kind.
type Upstream func(kind string, id int, ext string) string

// Proxy signs stream URLs and relays the streams they name.
type Proxy struct {
	key      []byte
	base     string
	upstream Upstream
	client   *http.Client
}

// New returns a proxy whose URLs start with base, the address the media
// server reaches VODarr's web port at (e.g. "http://vodarr:9090").
func New(key []byte, base string, upstream Upstream) *Proxy {
	return &Proxy{
		key:      key,
		base:     strings.TrimRight(base, "/"),
		upstream: upstream,
		client: &http.Client{
			// No overall timeout: a stream lasts as long as playback. Only
			// connecting and the provider's first response are bounded.
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout:   15 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				MaxIdleConnsPerHost:   4,
				IdleConnTimeout:       90 * time.Second,
			},
			// Follow the provider's redirects here: a redirect URL can carry
			// the credentials too, and the client must never see it.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("too many redirects")
				}
				return nil
			},
		},
	}
}

// LoadOrCreateKey reads the signing key from path, creating a random one on
// first use. It must persist: a new key invalidates every proxied .strm.
func LoadOrCreateKey(path string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil {
		key, decErr := hex.DecodeString(strings.TrimSpace(string(data)))
		if decErr == nil && len(key) >= 32 {
			return key, nil
		}
		return nil, fmt.Errorf("stream key %s is invalid; delete it to create a new one (proxied .strm files then need rewriting)", path)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)+"\n"), 0600); err != nil {
		return nil, fmt.Errorf("save stream key: %w", err)
	}
	return key, nil
}

func (p *Proxy) sign(kind string, id int) string {
	m := hmac.New(sha256.New, p.key)
	fmt.Fprintf(m, "%s/%d", kind, id)
	return hex.EncodeToString(m.Sum(nil))[:32]
}

// URL is the .strm content for a stream: a VODarr address instead of the
// provider's. The extension stays last; players infer the container from it.
func (p *Proxy) URL(kind string, id int, ext string) string {
	if ext == "" {
		ext = "mkv"
	}
	return fmt.Sprintf("%s/play/%s/%s/%d.%s", p.base, p.sign(kind, id), kind, id, ext)
}

var playPathRe = regexp.MustCompile(`^/play/([0-9a-f]{32})/(movie|series)/(\d+)\.([A-Za-z0-9]{1,8})$`)

// relayedHeaders are the response headers passed back to the media server;
// they are what seeking and container detection need.
var relayedHeaders = []string{
	"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges",
	"Last-Modified", "ETag", "Cache-Control",
}

// ServeHTTP relays GET/HEAD /play/<sig>/<kind>/<id>.<ext>, with Range
// support so the media server can seek.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	m := playPathRe.FindStringSubmatch(r.URL.Path)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	sig, kind, ext := m[1], m[2], m[4]
	id, err := strconv.Atoi(m[3])
	if err != nil || id <= 0 || !hmac.Equal([]byte(sig), []byte(p.sign(kind, id))) {
		http.NotFound(w, r) // never confirm which streams exist
		return
	}
	target := p.upstream(kind, id, ext)
	if target == "" {
		http.NotFound(w, r)
		return
	}

	up, err := http.NewRequestWithContext(r.Context(), r.Method, target, nil)
	if err != nil {
		http.Error(w, "bad upstream", http.StatusBadGateway)
		return
	}
	for _, h := range []string{"Range", "If-Range", "User-Agent", "Accept"} {
		if v := r.Header.Get(h); v != "" {
			up.Header.Set(h, v)
		}
	}
	resp, err := p.client.Do(up)
	if err != nil {
		if r.Context().Err() == nil {
			// The error text names the upstream URL; log only the stream.
			slog.Warn("stream proxy: provider unreachable", "kind", kind, "id", id, "error", errors.Unwrap(err))
		}
		http.Error(w, "provider unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for _, h := range relayedHeaders {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	// The web server's write timeout (two minutes) suits API responses, not
	// a stream that lasts as long as the film: lift it for this response.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	// Stream with periodic flushes so playback starts at once.
	fw := flushWriter{w: w}
	if f, ok := w.(http.Flusher); ok {
		fw.f = f
	}
	_, _ = io.Copy(fw, resp.Body)
}

type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (fw flushWriter) Write(b []byte) (int, error) {
	n, err := fw.w.Write(b)
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}
