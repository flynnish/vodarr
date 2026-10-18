package newznab

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vodarr/vodarr/internal/index"
)

// iptvPrefixRe matches IPTV provider prefixes like "┃NL┃", "| NL | HD |", etc.
// Replicates the pattern used in sync/scheduler.go for title cleaning.
var iptvPrefixRe = regexp.MustCompile(`^[\s]*[|┃]\s*(?:[^|┃]+[|┃]\s*)+`)

// nlGespokenRe matches Dutch audio markers, e.g. "(NL GESPROKEN)" or "[NL Gesproken]".
var nlGespokenRe = regexp.MustCompile(`(?i)[(\[]NL\s+GESPROKEN[)\]]`)

// iptvLangRe captures the first 2-3 letter country/region code from an IPTV
// category prefix, e.g. "┃NL┃", "| FR |", "\t┃DE┃ HD┃ Series".
var iptvLangRe = regexp.MustCompile(`(?i)[|┃]\s*([A-Za-z]{2,3})\s*[|┃\s]`)

// iptvLangMap maps IPTV country/region codes found in category prefixes to
// ISO 639-1 language codes used by Sonarr/Radarr for language matching.
var iptvLangMap = map[string]string{
	"NL": "nl", // Dutch / Netherlands
	"DE": "de", // German / Germany
	"FR": "fr", // French / France
	"UK": "en", // English / United Kingdom
	"US": "en", // English / United States
	"EN": "en", // English (explicit code)
	"ES": "es", // Spanish / Spain
	"MX": "es", // Mexican Spanish
	"IT": "it", // Italian
	"PT": "pt", // Portuguese / Portugal
	"BR": "pt", // Brazilian Portuguese
	"SE": "sv", // Swedish
	"DK": "da", // Danish
	"NO": "no", // Norwegian
	"FI": "fi", // Finnish
	"PL": "pl", // Polish
	"RU": "ru", // Russian
	"TR": "tr", // Turkish
	"HU": "hu", // Hungarian
	"CZ": "cs", // Czech
	"SK": "sk", // Slovak
	"RO": "ro", // Romanian
	"BG": "bg", // Bulgarian
	"HR": "hr", // Croatian
	"GR": "el", // Greek
	"IL": "he", // Hebrew (Israel)
	"HE": "he", // Hebrew (explicit code)
	"AR": "ar", // Arabic
	"JP": "ja", // Japanese
	"KR": "ko", // Korean
	"CN": "zh", // Chinese
}

// detectIPTVLanguage returns the ISO 639-1 language code for a stream name
// that contains a recognised IPTV category prefix (e.g. "┃NL┃ Series Name").
// Returns "" if no known code is detected.
func detectIPTVLanguage(name string) string {
	m := iptvLangRe.FindStringSubmatch(name)
	if len(m) < 2 {
		return ""
	}
	return iptvLangMap[strings.ToUpper(m[1])]
}

// categoryCodeRe captures a leading 2-3 letter code in a provider group name
// followed by a separator, e.g. "ES | Películas", "[DE] Filme", "FR: Séries",
// "NL - Films", "EN ▸ Series".
var categoryCodeRe = regexp.MustCompile(`^\s*(?:[\[(]\s*([A-Za-z]{2,3})\s*[\])]|([A-Za-z]{2,3})\s*[|┃:\-–—▸►•/])`)

// languageWordRe matches a language named in full in a provider group name
// ("Spanish Movies", "Películas Español", "Deutsch Serien").
var languageWordRe = regexp.MustCompile(`(?i)\b(english|spanish|espa[nñ]ol|castellano|latino|german|deutsch|french|fran[cç]ais|dutch|nederlands|vlaams|italian|italiano|portuguese|portugu[eê]s|swedish|svenska|danish|dansk|norwegian|norsk|finnish|suomi|polish|polski|russian|turkish|t[uü]rk[cç]e|hungarian|magyar|czech|romanian|greek|hebrew|arabic|japanese|korean|chinese|hindi)\b`)

var languageWords = map[string]string{
	"english": "en", "spanish": "es", "espanol": "es", "español": "es", "castellano": "es", "latino": "es",
	"german": "de", "deutsch": "de", "french": "fr", "francais": "fr", "français": "fr",
	"dutch": "nl", "nederlands": "nl", "vlaams": "nl", "italian": "it", "italiano": "it",
	"portuguese": "pt", "portugues": "pt", "português": "pt", "swedish": "sv", "svenska": "sv",
	"danish": "da", "dansk": "da", "norwegian": "no", "norsk": "no", "finnish": "fi", "suomi": "fi",
	"polish": "pl", "polski": "pl", "russian": "ru", "turkish": "tr", "turkce": "tr", "türkçe": "tr",
	"turkçe": "tr", "türkce": "tr", "hungarian": "hu", "magyar": "hu", "czech": "cs", "romanian": "ro",
	"greek": "el", "hebrew": "he", "arabic": "ar", "japanese": "ja", "korean": "ko", "chinese": "zh",
	"hindi": "hi",
}

// categoryLanguage returns the ISO 639-1 language a provider group name
// names, as a code prefix or a word, or "" when it names none. Unknown codes
// ("4K | …", "NF | Netflix", "VIP | …") are ignored.
func categoryLanguage(category string) string {
	if category == "" {
		return ""
	}
	if lang := detectIPTVLanguage(category); lang != "" {
		return lang
	}
	if m := categoryCodeRe.FindStringSubmatch(category); m != nil {
		if lang := iptvLangMap[strings.ToUpper(m[1]+m[2])]; lang != "" {
			return lang
		}
	}
	if m := languageWordRe.FindStringSubmatch(category); m != nil {
		return languageWords[strings.ToLower(m[1])]
	}
	return ""
}

// itemLanguage is the language of an item's stream: from a code in its own
// name ("┃NL┃ Show"), else from its provider group ("ES | Películas").
func itemLanguage(item *index.Item) string {
	if lang := detectIPTVLanguage(item.Name); lang != "" {
		return lang
	}
	return categoryLanguage(item.Category)
}

// defaultLanguage is assumed when neither a stream's name nor its provider
// group names a language.
const defaultLanguage = "en"

// releaseLanguage is the language a release is labelled with: from the
// stream's name or group (itemLanguage), else a Dutch audio marker, else
// English. Labelling English explicitly matters: arr treats an unlabelled
// release as the title's original language, so an English dub of a Spanish
// show would otherwise count as Spanish.
func releaseLanguage(item *index.Item) string {
	if lang := itemLanguage(item); lang != "" {
		return lang
	}
	if nlGespokenRe.MatchString(item.Name) {
		return "nl"
	}
	return defaultLanguage
}

// groupFolds transliterates common accented letters for release group names.
var groupFolds = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a", "å", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "ö", "o", "õ", "o", "ø", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u",
	"ñ", "n", "ç", "c", "ß", "ss",
	"Á", "A", "À", "A", "Â", "A", "Ä", "A", "Ã", "A", "Å", "A",
	"É", "E", "È", "E", "Ê", "E", "Ë", "E",
	"Í", "I", "Ì", "I", "Î", "I", "Ï", "I",
	"Ó", "O", "Ò", "O", "Ô", "O", "Ö", "O", "Õ", "O", "Ø", "O",
	"Ú", "U", "Ù", "U", "Û", "U", "Ü", "U",
	"Ñ", "N", "Ç", "C",
)

// maxReleaseGroup bounds the release group so titles stay readable.
const maxReleaseGroup = 30

// releaseGroup turns a provider group name into a release group, the part
// of a release title after the last dash ("…WEB-DL-ESPeliculas"). Sonarr and
// Radarr show it in search results and custom formats can score it, so two
// copies of a title from different groups can be told apart and ranked.
// Words are joined with capitalised first letters, keeping only ASCII
// letters and digits, which is all arr's release group parsing accepts.
func releaseGroup(category string) string {
	var b strings.Builder
	for _, word := range strings.FieldsFunc(groupFolds.Replace(category), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	}) {
		b.WriteString(strings.ToUpper(word[:1]) + word[1:])
	}
	g := b.String()
	if len(g) > maxReleaseGroup {
		g = g[:maxReleaseGroup]
	}
	return g
}

// webDL is the source tag ending a release title, with the item's provider
// group as release group when it has one: "WEB-DL" or "WEB-DL-ESPeliculas".
func webDL(item *index.Item) string {
	if g := releaseGroup(item.Category); g != "" {
		return "WEB-DL-" + g
	}
	return "WEB-DL"
}

// langTitleTokens maps ISO 639-1 language codes to the uppercase title tokens
// recognised by Sonarr/Radarr custom format rules (e.g. "DUTCH", "GERMAN").
var langTitleTokens = map[string]string{
	"en": "ENGLISH",
	"nl": "DUTCH",
	"de": "GERMAN",
	"fr": "FRENCH",
	"es": "SPANISH",
	"it": "ITALIAN",
	"pt": "PORTUGUESE",
	"sv": "SWEDISH",
	"da": "DANISH",
	"no": "NORWEGIAN",
	"fi": "FINNISH",
	"pl": "POLISH",
	"ru": "RUSSIAN",
	"tr": "TURKISH",
	"hu": "HUNGARIAN",
	"cs": "CZECH",
	"sk": "SLOVAK",
	"ro": "ROMANIAN",
	"bg": "BULGARIAN",
	"hr": "CROATIAN",
	"el": "GREEK",
	"he": "HEBREW",
	"ar": "ARABIC",
	"ja": "JAPANESE",
	"ko": "KOREAN",
	"zh": "CHINESE",
	"hi": "HINDI",
}

// langTitleToken returns the Sonarr/Radarr-compatible uppercase title token
// for a given ISO 639-1 code (e.g. "nl" → "DUTCH", "en" → "ENGLISH").
// Returns "" for any unmapped code.
func langTitleToken(iso639 string) string {
	return langTitleTokens[iso639]
}

// yearDashRe matches a trailing dash-separated year, e.g. "Movie - 2021".
var yearDashRe = regexp.MustCompile(`\s*-\s*\d{4}\s*$`)

// yearBracketRe matches a trailing bracket-enclosed year, e.g. "Movie [2021]".
var yearBracketRe = regexp.MustCompile(`\s*\[\d{4}\]\s*$`)

// yearParenRe matches a trailing parenthesised year, e.g. "Movie (2021)".
var yearParenRe = regexp.MustCompile(`\s*\(\d{4}\)\s*$`)

// hevcRe matches HEVC codec markers for quality attribute generation.
var hevcRe = regexp.MustCompile(`(?i)\bHEVC\b`)

// fourKRe matches 4K resolution markers for quality attribute generation.
var fourKRe = regexp.MustCompile(`\b4K\b`)

// stripIPTVPrefix removes leading IPTV category prefixes from a name.
func stripIPTVPrefix(s string) string {
	return strings.TrimSpace(iptvPrefixRe.ReplaceAllString(s, ""))
}

// sanitizeNameForTitle prepares a stream name for use in an RSS title.
// It strips the IPTV prefix, removes trailing year noise (year is re-added
// separately by buildTitle via item.Year), and normalises Dutch audio markers
// to the Sonarr/Radarr-compatible "DUTCH" token.
// Quality markers (HEVC, 4K, DOLBY) are intentionally kept because
// Radarr/Sonarr parse them for quality profile matching.
func sanitizeNameForTitle(name string) string {
	s := strings.TrimSpace(iptvPrefixRe.ReplaceAllString(name, ""))
	s = yearDashRe.ReplaceAllString(s, "")
	s = yearBracketRe.ReplaceAllString(s, "")
	s = yearParenRe.ReplaceAllString(s, "")
	s = nlGespokenRe.ReplaceAllString(s, "DUTCH")
	// Normalise any double spaces left after stripping.
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// RSS is the root RSS 2.0 document.
type RSS struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	NZB     string   `xml:"xmlns:nzb,attr"`
	Newznab string   `xml:"xmlns:newznab,attr"`
	Channel Channel  `xml:"channel"`
}

// Channel holds the feed metadata and items.
type Channel struct {
	Title       string   `xml:"title"`
	Description string   `xml:"description"`
	Link        string   `xml:"link"`
	Language    string   `xml:"language"`
	NewznabResp Response `xml:"newznab:response"`
	Items       []Item   `xml:"item"`
}

// Response holds paging info.
type Response struct {
	Offset int `xml:"offset,attr"`
	Total  int `xml:"total,attr"`
}

// GUID is a Newznab RSS guid element with an isPermaLink attribute.
// Setting isPermaLink="false" prevents clients from treating the value as a URL.
type GUID struct {
	Value       string `xml:",chardata"`
	IsPermaLink string `xml:"isPermaLink,attr"`
}

// Item represents a single search result.
type Item struct {
	Title       string    `xml:"title"`
	GUID        GUID      `xml:"guid"`
	Link        string    `xml:"link"`
	PubDate     string    `xml:"pubDate"`
	Description string    `xml:"description"`
	Size        int64     `xml:"size"`
	Enclosure   Enclosure `xml:"enclosure"`
	Attrs       []Attr    `xml:"newznab:attr"`
}

// Enclosure mimics an NZB file link.
type Enclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

// Attr is a newznab:attr element.
type Attr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

// CapsResponse is the response to t=caps.
type CapsResponse struct {
	XMLName  xml.Name    `xml:"caps"`
	Server   CapsServer  `xml:"server"`
	Limits   CapsLimits  `xml:"limits"`
	Searching CapsSearcing `xml:"searching"`
	Categories CapsCategories `xml:"categories"`
}

type CapsServer struct {
	Version string `xml:"version,attr"`
	Title   string `xml:"title,attr"`
	Email   string `xml:"email,attr"`
}

type CapsLimits struct {
	Max     int `xml:"max,attr"`
	Default int `xml:"default,attr"`
}

type CapsSearcing struct {
	Search   CapsSearch `xml:"search"`
	TVSearch CapsSearch `xml:"tv-search"`
	Movie    CapsSearch `xml:"movie-search"`
}

type CapsSearch struct {
	Available      string `xml:"available,attr"`
	SupportedParams string `xml:"supportedParams,attr"`
}

type CapsCategories struct {
	Category []CapsCategory `xml:"category"`
}

type CapsCategory struct {
	ID          string          `xml:"id,attr"`
	Name        string          `xml:"name,attr"`
	SubCategory []CapsCategory  `xml:"subcat"`
}

func buildCaps(serverURL string) *CapsResponse {
	return &CapsResponse{
		Server: CapsServer{
			Version: "1.0",
			Title:   "VODarr",
			Email:   "vodarr@localhost",
		},
		Limits: CapsLimits{Max: 100, Default: 100},
		Searching: CapsSearcing{
			Search:   CapsSearch{Available: "yes", SupportedParams: "q,year"},
			TVSearch: CapsSearch{Available: "yes", SupportedParams: "q,tvdbid,tmdbid,season,ep"},
			Movie:    CapsSearch{Available: "yes", SupportedParams: "q,imdbid,tmdbid,year"},
		},
		Categories: CapsCategories{
			Category: []CapsCategory{
				{ID: "2000", Name: "Movies", SubCategory: []CapsCategory{
					{ID: "2040", Name: "HD"},
					{ID: "2045", Name: "UHD"},
					{ID: "2010", Name: "Foreign"},
				}},
				{ID: "5000", Name: "TV", SubCategory: []CapsCategory{
					{ID: "5040", Name: "HD"},
					{ID: "5045", Name: "UHD"},
				}},
			},
		},
	}
}

// buildRSS constructs an RSS feed from pre-expanded RSS items.
// Callers are responsible for pagination slicing; offset/total are for the <newznab:response> element.
func buildRSS(serverURL string, rssItems []Item, offset, total int) *RSS {
	return &RSS{
		Version: "2.0",
		NZB:     "http://www.newzbin.com/DTD/2007/feeds/NZB/",
		Newznab: "http://www.newznab.com/DTD/2010/feeds/attributes/",
		Channel: Channel{
			Title:       "VODarr",
			Description: "Xtream Codes IPTV via VODarr",
			Link:        serverURL,
			Language:    "en-gb",
			NewznabResp: Response{Offset: offset, Total: total},
			Items:       rssItems,
		},
	}
}

// buildMovieRSSItems converts movie index items to RSS items.
func buildMovieRSSItems(serverURL string, items []*index.Item) []Item {
	out := make([]Item, 0, len(items))
	for _, item := range items {
		out = append(out, itemToRSS(serverURL, item))
	}
	return out
}

// buildEpisodeRSSItems expands series index items into per-episode RSS items.
// seasonFilter / epFilter == 0 means no filter.
func buildEpisodeRSSItems(serverURL string, items []*index.Item, seasonFilter, epFilter int) []Item {
	var out []Item
	for _, item := range items {
		if len(item.Episodes) == 0 {
			// No episode data yet (e.g. Xtream fetch failed). In browse mode
			// (no season/ep filter) emit a series-level placeholder so that
			// Prowlarr's sync-test query returns non-zero results. Actual grab
			// requests for these items will fail gracefully in handleGet.
			if seasonFilter == 0 && epFilter == 0 {
				out = append(out, itemToRSS(serverURL, item))
			}
			continue
		}
		for _, ep := range item.Episodes {
			if seasonFilter > 0 && ep.Season != seasonFilter {
				continue
			}
			if epFilter > 0 && ep.EpisodeNum != epFilter {
				continue
			}
			out = append(out, episodeToRSS(serverURL, item, ep))
		}
	}
	return out
}

// episodeToRSS converts a single episode to an RSS item.
func episodeToRSS(serverURL string, series *index.Item, ep index.EpisodeItem) Item {
	size := int64(1024 * 1024 * 1024) // 1 GB fallback
	if ep.FileSize > 0 {
		size = ep.FileSize
	}
	epTag := fmt.Sprintf("S%02dE%02d", ep.Season, ep.EpisodeNum)
	guid := fmt.Sprintf("vodarr-ep-%d-%d-%d", series.XtreamID, ep.Season, ep.EpisodeNum)
	if series.SourceType != "" {
		// A VOD stream offered as an episode: its ID is a VOD stream ID and
		// could equal a real series ID, so keep its GUID apart.
		guid = fmt.Sprintf("vodarr-ep-%s-%d-%d-%d", series.SourceType, series.XtreamID, ep.Season, ep.EpisodeNum)
	}
	// type= names the provider type, which is what identifies the item.
	downloadURL := fmt.Sprintf("%s/api?t=get&id=%d&type=%s&episode_id=%d",
		serverURL, series.XtreamID, series.ProviderType(), ep.EpisodeID)

	// Use canonical TMDB/TVDB title when available so Sonarr/Radarr can match
	// by title (e.g. "Loving Ibiza: Series" instead of "Verliefd op Ibiza").
	titleSource := series.Name
	if series.CanonicalName != "" {
		titleSource = series.CanonicalName
	}
	seriesSafe := strings.ReplaceAll(sanitizeNameForTitle(titleSource), " ", ".")
	seriesSafe = strings.ReplaceAll(seriesSafe, ":", "")
	seriesSafe = strings.ReplaceAll(seriesSafe, "/", "")

	// Add the series year, as for movies. Sonarr resolves a release's title
	// through its scene mappings before comparing TVDB IDs, so a bare shared
	// title ("Scrubs") is claimed by the show that owns that title (Scrubs
	// 2001) and the search for "Scrubs (2026)" discards it. "Scrubs.2026"
	// matches "Scrubs (2026)" by title; for any show whose year is off,
	// Sonarr's TVDB ID check still finds it.
	year := series.Year
	if year == "" && len(series.ReleaseDate) >= 4 {
		year = series.ReleaseDate[:4]
	}
	if year != "" && !strings.Contains(seriesSafe, year) {
		seriesSafe += "." + year
	}

	ext := ep.Ext
	if ext == "" {
		ext = "mkv"
	}
	// Inject language token between episode tag and WEB-DL so Sonarr/Radarr
	// custom format rules (which scan the title, not the language attr) can
	// match the language. Skip if sanitizeNameForTitle already added the token.
	token := ""
	if t := langTitleToken(releaseLanguage(series)); t != "" && !strings.Contains(seriesSafe, t) {
		token = t
	}
	var title string
	if token != "" {
		title = fmt.Sprintf("%s.%s.%s.%s.%s", seriesSafe, epTag, token, webDL(series), ext)
	} else {
		title = fmt.Sprintf("%s.%s.%s.%s", seriesSafe, epTag, webDL(series), ext)
	}

	rssItem := Item{
		Title:       title,
		GUID:        GUID{Value: guid, IsPermaLink: "false"},
		Link:        downloadURL,
		PubDate:     stableDate(series.ReleaseDate),
		Description: series.Plot,
		Size:        size,
		Enclosure: Enclosure{
			URL:    downloadURL,
			Length: size,
			Type:   "application/x-bittorrent",
		},
		Attrs: []Attr{
			{Name: "category", Value: "5000"},
			{Name: "category", Value: "TV"},
			{Name: "size", Value: strconv.FormatInt(size, 10)},
			{Name: "season", Value: strconv.Itoa(ep.Season)},
			{Name: "episode", Value: strconv.Itoa(ep.EpisodeNum)},
		},
	}

	if series.TVDBId != "" {
		rssItem.Attrs = append(rssItem.Attrs, Attr{Name: "tvdbid", Value: series.TVDBId})
	}
	if series.TMDBId != "" {
		rssItem.Attrs = append(rssItem.Attrs, Attr{Name: "tmdbid", Value: series.TMDBId})
	}
	if series.IMDBId != "" {
		rssItem.Attrs = append(rssItem.Attrs, Attr{Name: "imdbid", Value: series.IMDBId})
	}
	if ep.Duration > 0 {
		rssItem.Attrs = append(rssItem.Attrs, Attr{Name: "duration", Value: fmt.Sprintf("%.0f", ep.Duration)})
	}
	if hevcRe.MatchString(series.Name) {
		rssItem.Attrs = append(rssItem.Attrs, Attr{Name: "video_codec", Value: "x265"})
	}
	if fourKRe.MatchString(series.Name) {
		rssItem.Attrs = append(rssItem.Attrs, Attr{Name: "resolution", Value: "2160p"})
	}
	rssItem.Attrs = append(rssItem.Attrs, Attr{Name: "language", Value: releaseLanguage(series)})

	return rssItem
}

func itemToRSS(serverURL string, item *index.Item) Item {
	size := int64(1024 * 1024 * 1024) // 1 GB fallback
	if item.FileSize > 0 {
		size = item.FileSize
	}
	category := "5000"
	categoryName := "TV"
	if item.Type == index.TypeMovie {
		category = "2000"
		categoryName = "Movies"
	}

	guid := fmt.Sprintf("vodarr-%s-%d", item.Type, item.XtreamID)
	if item.SourceType != "" {
		// A provider series offered as a film: keep its GUID apart from the
		// VOD stream that may share its numeric ID.
		guid = fmt.Sprintf("vodarr-%s-from-%s-%d", item.Type, item.SourceType, item.XtreamID)
	}
	// type= names the provider type, which is what identifies the item.
	downloadURL := fmt.Sprintf("%s/api?t=get&id=%d&type=%s", serverURL, item.XtreamID, item.ProviderType())

	title := buildTitle(item)

	rssItem := Item{
		Title:       title,
		GUID:        GUID{Value: guid, IsPermaLink: "false"},
		Link:        downloadURL,
		PubDate:     stableDate(item.ReleaseDate),
		Description: item.Plot,
		Size:        size,
		Enclosure: Enclosure{
			URL:    downloadURL,
			Length: size,
			Type:   "application/x-bittorrent",
		},
		Attrs: []Attr{
			{Name: "category", Value: category},
			{Name: "category", Value: categoryName},
			{Name: "size", Value: strconv.FormatInt(size, 10)},
		},
	}

	if item.IMDBId != "" {
		rssItem.Attrs = append(rssItem.Attrs, Attr{Name: "imdb", Value: strings.TrimPrefix(item.IMDBId, "tt")})
		rssItem.Attrs = append(rssItem.Attrs, Attr{Name: "imdbid", Value: item.IMDBId})
	}
	if item.TVDBId != "" {
		rssItem.Attrs = append(rssItem.Attrs, Attr{Name: "tvdbid", Value: item.TVDBId})
	}
	if item.TMDBId != "" {
		rssItem.Attrs = append(rssItem.Attrs, Attr{Name: "tmdbid", Value: item.TMDBId})
	}
	if item.Year != "" {
		rssItem.Attrs = append(rssItem.Attrs, Attr{Name: "year", Value: item.Year})
	}
	if item.Rating > 0 {
		rssItem.Attrs = append(rssItem.Attrs, Attr{Name: "rating", Value: fmt.Sprintf("%.1f", item.Rating)})
	}
	if item.Duration > 0 {
		rssItem.Attrs = append(rssItem.Attrs, Attr{Name: "duration", Value: fmt.Sprintf("%.0f", item.Duration)})
	}
	if hevcRe.MatchString(item.Name) {
		rssItem.Attrs = append(rssItem.Attrs, Attr{Name: "video_codec", Value: "x265"})
	}
	if fourKRe.MatchString(item.Name) {
		rssItem.Attrs = append(rssItem.Attrs, Attr{Name: "resolution", Value: "2160p"})
	}
	rssItem.Attrs = append(rssItem.Attrs, Attr{Name: "language", Value: releaseLanguage(item)})

	return rssItem
}

// stableDate returns a consistent RFC1123Z timestamp from a release date string.
// Using a stable date (rather than time.Now) prevents Sonarr/Radarr from
// re-grabbing already-processed items on each RSS sync.
func stableDate(releaseDate string) string {
	if releaseDate != "" {
		for _, layout := range []string{"2006-01-02", "2006"} {
			if t, err := time.Parse(layout, releaseDate); err == nil {
				return t.UTC().Format(time.RFC1123Z)
			}
		}
	}
	return time.Unix(0, 0).UTC().Format(time.RFC1123Z)
}

func buildTitle(item *index.Item) string {
	ext := item.ContainerExt
	if ext == "" {
		ext = "mkv"
	}
	// Use canonical TMDB/TVDB title when available so Sonarr/Radarr can match by title.
	titleSource := item.Name
	if item.CanonicalName != "" {
		titleSource = item.CanonicalName
	}
	safe := strings.ReplaceAll(sanitizeNameForTitle(titleSource), " ", ".")
	safe = strings.ReplaceAll(safe, ":", "")
	safe = strings.ReplaceAll(safe, "/", "")

	year := item.Year
	if year == "" && item.ReleaseDate != "" && len(item.ReleaseDate) >= 4 {
		year = item.ReleaseDate[:4]
	}

	// Inject language token so Sonarr/Radarr custom format rules can match.
	// Skip if sanitizeNameForTitle already added the token (e.g. from GESPROKEN).
	token := ""
	if t := langTitleToken(releaseLanguage(item)); t != "" && !strings.Contains(safe, t) {
		token = t
	}

	if year != "" {
		if token != "" {
			return fmt.Sprintf("%s.%s.%s.%s.%s", safe, year, token, webDL(item), ext)
		}
		return fmt.Sprintf("%s.%s.%s.%s", safe, year, webDL(item), ext)
	}
	if token != "" {
		return fmt.Sprintf("%s.%s.%s.%s", safe, token, webDL(item), ext)
	}
	return fmt.Sprintf("%s.%s.%s", safe, webDL(item), ext)
}
