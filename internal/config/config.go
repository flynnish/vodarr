package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Xtream XtreamConfig `yaml:"xtream"`
	// ExtraProviders are further Xtream accounts whose catalogs are synced
	// alongside the primary (xtream) one, e.g. for titles it lacks.
	ExtraProviders []ProviderConfig `yaml:"extra_providers,omitempty"`
	TMDB           TMDBConfig       `yaml:"tmdb"`
	Output  OutputConfig  `yaml:"output"`
	Sync    SyncConfig    `yaml:"sync"`
	Server  ServerConfig  `yaml:"server"`
	Logging LoggingConfig `yaml:"logging"`
	Arr     ArrConfig     `yaml:"arr"`
	Update  UpdateConfig  `yaml:"update"`
}

type ArrConfig struct {
	Instances []ArrInstance `yaml:"instances"`
	// AutoSearch makes VODarr ask each instance, after every sync, to search
	// for its wanted items that VODarr has. Default true.
	AutoSearch bool `yaml:"auto_search"`
	// UnmonitorDelivered makes VODarr unmonitor an episode/movie in arr once
	// its .mkv stub is replaced by a .strm, so arr does not grab it again.
	// Default true.
	UnmonitorDelivered bool `yaml:"unmonitor_delivered"`
}

type ArrInstance struct {
	Name   string `yaml:"name"`
	Type   string `yaml:"type"`    // "sonarr" or "radarr"
	URL    string `yaml:"url"`
	APIKey string `yaml:"api_key"`
}

// ProviderConfig is an extra Xtream provider. Name identifies it (its
// entries, release groups and .strm URLs carry it) and must stay stable.
type ProviderConfig struct {
	Name     string `yaml:"name"`
	URL      string `yaml:"url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// Provider is one Xtream source as the rest of VODarr sees it. The primary
// provider (xtream) has ID ""; its entries keep their original keys, so
// caches, manual matches and .strm files from before extra providers carry
// over unchanged.
type Provider struct {
	ID       string // "" for the primary provider, else ProviderID(Name)
	Name     string // for display and release groups
	URL      string
	Username string
	Password string
}

// ProviderID turns a provider name into its ID: lowercase letters and
// digits only.
func ProviderID(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Providers lists the primary provider first, then the extra ones.
func (c *Config) Providers() []Provider {
	name := c.Xtream.Name
	if name == "" {
		name = "Primary"
	}
	out := []Provider{{Name: name, URL: c.Xtream.URL, Username: c.Xtream.Username, Password: c.Xtream.Password}}
	for _, p := range c.ExtraProviders {
		out = append(out, Provider{ID: ProviderID(p.Name), Name: p.Name, URL: p.URL, Username: p.Username, Password: p.Password})
	}
	return out
}

type XtreamConfig struct {
	// Name labels the primary provider in the UI (default "Primary").
	Name     string `yaml:"name,omitempty"`
	URL      string `yaml:"url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type TMDBConfig struct {
	APIKey     string `yaml:"api_key"`
	TVDBAPIKey string `yaml:"tvdb_api_key"` // optional; enables TVDB series fallback
}

type OutputConfig struct {
	Path                   string `yaml:"path"`
	MoviesDir              string `yaml:"movies_dir"`
	SeriesDir              string `yaml:"series_dir"`
	Mode                   string `yaml:"mode"`                     // "strm" (default) or "download"
	MaxConcurrentDownloads int    `yaml:"max_concurrent_downloads"` // download mode only; default 1
	DownloadDelay          string `yaml:"download_delay"`           // pause between downloads; Go duration (default "30s")
	BandwidthLimit         string `yaml:"bandwidth_limit"`          // max download speed e.g. "20M" (MB/s); empty = unlimited
	// DefaultLanguage labels releases whose stream name and provider group
	// name no language: "original" (default) leaves them unlabelled, so arr
	// assumes the title's original language; or an ISO 639-1 code ("en",
	// "de", ...) to label them all with that one language.
	DefaultLanguage string `yaml:"default_language"`
	// StreamProxy writes VODarr URLs into .strm files instead of provider
	// URLs (which embed the account's username and password); VODarr relays
	// the stream. StreamProxyURL is how the media server reaches VODarr's
	// web port, e.g. "http://vodarr:9090".
	StreamProxy    bool   `yaml:"stream_proxy"`
	StreamProxyURL string `yaml:"stream_proxy_url,omitempty"`

	// Parsed values (not from YAML directly)
	ParsedDownloadDelay  time.Duration `yaml:"-"`
	ParsedBandwidthLimit int64         `yaml:"-"` // bytes/sec
}

type SyncConfig struct {
	Interval             string   `yaml:"interval"`
	OnStartup            bool     `yaml:"on_startup"`
	Parallelism          int      `yaml:"parallelism"`
	GraceCycles          int      `yaml:"grace_cycles"`
	TitleCleanupPatterns []string `yaml:"title_cleanup_patterns,omitempty"`
	// ExcludedCategories lists provider groups to leave out of the index, as
	// "vod:<category_id>" or "series:<category_id>".
	ExcludedCategories []string `yaml:"excluded_categories,omitempty"`

	// Parsed interval (not from YAML directly)
	ParsedInterval time.Duration `yaml:"-"`
}

type ServerConfig struct {
	NewznabPort int    `yaml:"newznab_port"`
	QbitPort    int    `yaml:"qbit_port"`
	WebPort     int    `yaml:"web_port"`
	ExternalURL string `yaml:"external_url"` // 1B: e.g. "http://vodarr:7878" — overrides localhost fallback
	APIKey      string `yaml:"api_key"`      // 2C: Newznab API key; empty = no auth
	QbitUsername string `yaml:"qbit_username"` // 2D: qBit web UI credentials
	QbitPassword string `yaml:"qbit_password"`
	WebUsername string `yaml:"web_username"` // 2E: web UI basic auth
	WebPassword string `yaml:"web_password"`
}

type LoggingConfig struct {
	Level string `yaml:"level"`
}

type UpdateConfig struct {
	BetaChannel bool `yaml:"beta_channel"`
}

// Save marshals cfg to YAML and writes it atomically to path.
func Save(path string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Explicitly restrict config file to owner-only before writing credentials
	if err := os.Chmod(tmpName, 0600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("renaming temp file: %w", err)
	}
	return nil
}

// CheckWritable verifies that path is (or can be created as) a writable directory
// by creating and immediately removing a probe file. Returns nil on success.
func CheckWritable(path string) error {
	if err := os.MkdirAll(path, 0755); err != nil {
		return fmt.Errorf("output path not writable: %w", err)
	}
	probe, err := os.CreateTemp(path, ".vodarr-write-test-*")
	if err != nil {
		return fmt.Errorf("output path not writable: %w", err)
	}
	probe.Close()
	os.Remove(probe.Name())
	return nil
}

// Validate checks that all required config fields are set and valid.
func (c *Config) Validate() error {
	return c.validate()
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	cfg := defaults()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

func defaults() *Config {
	return &Config{
		Output: OutputConfig{
			MoviesDir:              "movies",
			SeriesDir:              "tv",
			Mode:                   "strm",
			MaxConcurrentDownloads: 1,
			DownloadDelay:          "30s",
			DefaultLanguage:        "original",
		},
		Sync: SyncConfig{
			Interval:    "6h",
			OnStartup:   true,
			Parallelism: 10,
			GraceCycles: 3,
		},
		Server: ServerConfig{
			NewznabPort: 9091,
			QbitPort:    9092,
			WebPort:     9090,
		},
		Logging: LoggingConfig{
			Level: "info",
		},
		Arr: ArrConfig{
			AutoSearch:         true,
			UnmonitorDelivered: true,
		},
	}
}

func (c *Config) validate() error {
	if c.Xtream.URL == "" {
		return fmt.Errorf("xtream.url is required")
	}
	if c.Xtream.Username == "" {
		return fmt.Errorf("xtream.username is required")
	}
	if c.Xtream.Password == "" {
		return fmt.Errorf("xtream.password is required")
	}
	seen := map[string]bool{}
	for i, p := range c.ExtraProviders {
		id := ProviderID(p.Name)
		switch {
		case id == "":
			return fmt.Errorf("extra_providers[%d]: name is required (letters or digits)", i)
		case seen[id] || id == "primary":
			return fmt.Errorf("extra_providers[%d]: name %q is already used", i, p.Name)
		case p.URL == "" || p.Username == "" || p.Password == "":
			return fmt.Errorf("extra provider %q: url, username and password are required", p.Name)
		}
		seen[id] = true
	}
	if c.TMDB.APIKey == "" {
		return fmt.Errorf("tmdb.api_key is required")
	}
	if c.Output.Path == "" {
		return fmt.Errorf("output.path is required")
	}
	if !filepath.IsAbs(c.Output.Path) {
		return fmt.Errorf("output.path must be absolute")
	}
	cleaned := filepath.Clean(c.Output.Path)
	systemDirs := []string{"/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64", "/boot", "/dev", "/proc", "/sys", "/var/run"}
	for _, blocked := range systemDirs {
		if cleaned == blocked || strings.HasPrefix(cleaned, blocked+"/") {
			return fmt.Errorf("output.path must not be a system directory")
		}
	}

	d, err := time.ParseDuration(c.Sync.Interval)
	if err != nil {
		return fmt.Errorf("sync.interval is invalid: %w", err)
	}
	if d <= 0 {
		return fmt.Errorf("sync.interval must be positive")
	}
	c.Sync.ParsedInterval = d

	if c.Sync.Parallelism < 1 {
		c.Sync.Parallelism = 1
	}
	if c.Sync.Parallelism > 20 {
		c.Sync.Parallelism = 20
	}

	switch c.Output.Mode {
	case "", "strm", "download":
	default:
		return fmt.Errorf("output.mode must be \"strm\" or \"download\", got %q", c.Output.Mode)
	}
	if c.Output.Mode == "" {
		c.Output.Mode = "strm"
	}
	switch c.Output.DefaultLanguage {
	case "", "none": // older spellings
		c.Output.DefaultLanguage = "original"
	}
	if c.Output.StreamProxy {
		u := c.Output.StreamProxyURL
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("output.stream_proxy_url must be the http(s) address your media server reaches VODarr's web port at (e.g. http://vodarr:9090) when output.stream_proxy is on")
		}
	}
	if dl := c.Output.DefaultLanguage; dl != "original" && !isLanguageCode(dl) {
		return fmt.Errorf("output.default_language must be \"original\" or a two-letter language code (e.g. en, de), got %q", dl)
	}
	if c.Output.Mode == "download" {
		if c.Output.MaxConcurrentDownloads < 1 {
			c.Output.MaxConcurrentDownloads = 1
		}
		if c.Output.MaxConcurrentDownloads > 5 {
			c.Output.MaxConcurrentDownloads = 5
		}

		if c.Output.DownloadDelay == "" {
			c.Output.DownloadDelay = "30s"
		}
		dd, err := time.ParseDuration(c.Output.DownloadDelay)
		if err != nil {
			return fmt.Errorf("output.download_delay is invalid: %w", err)
		}
		if dd < 0 {
			dd = 0
		}
		c.Output.ParsedDownloadDelay = dd

		if c.Output.BandwidthLimit != "" {
			bw, err := parseBandwidth(c.Output.BandwidthLimit)
			if err != nil {
				return fmt.Errorf("output.bandwidth_limit is invalid: %w", err)
			}
			c.Output.ParsedBandwidthLimit = bw
		}
	}

	return nil
}

// isLanguageCode reports whether s looks like an ISO 639-1 code: two
// lowercase letters.
func isLanguageCode(s string) bool {
	return len(s) == 2 && s[0] >= 'a' && s[0] <= 'z' && s[1] >= 'a' && s[1] <= 'z'
}

// parseBandwidth parses a human-friendly bandwidth string like "20M" (20 MB/s),
// "500K" (500 KB/s), or a plain number (bytes/sec).
func parseBandwidth(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	multiplier := int64(1)
	upper := strings.ToUpper(s)
	switch {
	case strings.HasSuffix(upper, "M"):
		multiplier = 1024 * 1024
		s = s[:len(s)-1]
	case strings.HasSuffix(upper, "K"):
		multiplier = 1024
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("cannot parse %q as bandwidth", s)
	}
	if n <= 0 {
		return 0, nil
	}
	return int64(n * float64(multiplier)), nil
}
