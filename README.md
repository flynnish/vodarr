<p align="center">
  <img src="docs/logo.svg" alt="VODarr — IPTV → arr bridge" width="220" />
</p>

# VODarr

Bridges an Xtream Codes IPTV provider with Sonarr and Radarr. Search your IPTV catalog through the standard \*arr interface, "download" content as `.strm` files, and play directly in Jellyfin — no video is ever downloaded to disk.

## How it works

1. **Sync** — VODarr fetches your full IPTV catalog from the Xtream provider, enriches each title with IMDB/TVDB IDs via TMDB, and holds everything in memory. The index is cached to disk so restarts are instant.
2. **Search** — Sonarr/Radarr query VODarr like any Newznab indexer (port 9091). Searches match by IMDB ID, TVDB ID, or fuzzy title.
3. **Grab** — Sonarr/Radarr send a "download" request to VODarr's fake qBittorrent client (port 9092). VODarr writes a tiny `.strm` file pointing to the stream URL and immediately reports the torrent as complete.
4. **Play** — Jellyfin imports the `.strm` file and streams directly from the IPTV provider on playback.

## Quick start

```bash
cp config.example.yml config.yml
# Edit config.yml with your Xtream and TMDB credentials
docker compose up -d
```

Open `http://<host>:9090` for the web UI.

## Sonarr / Radarr setup

### Option A — Auto-configure (recommended)

Add your arr instances to `config.yml` under the `arr:` section (see [Configuration](#configuration)), then open the VODarr web UI → **Settings → Arr Instances** and click **Setup** next to each instance.

VODarr will automatically:
- Add itself as a **Torznab indexer** (categories 5000 for TV, 2000 for movies)
- Add itself as a **qBittorrent download client**
- Register a **webhook** so arr notifies VODarr on import (used for cleanup)

The Settings page shows the live connection and webhook status for each configured instance.

### Option B — Manual setup

**Indexer** (Torznab):

| Setting | Value |
|---------|-------|
| URL | `http://<vodarr-host>:9091` |
| Movie categories | `2000` |
| TV categories | `5000` |

**Download client** (qBittorrent):

| Setting | Value |
|---------|-------|
| Host | `<vodarr-host>` |
| Port | `9092` |

**Webhook** (optional but recommended):

In Sonarr/Radarr → Settings → Connect → add a Webhook pointing to `http://<vodarr-host>:9090/api/webhook`. This lets VODarr remove the `.mkv` stub files from disk after arr imports the episode, keeping your output directory clean.

## Shared path requirement

VODarr writes `.strm` files to the configured `output.path`. Sonarr, Radarr, and Jellyfin all need read access to that same directory. When running via Docker, mount the same volume path in all containers.

For the post-import step, VODarr also needs your Sonarr/Radarr **library** folders mounted at the same paths arr uses. After each import, the webhook copies the `.strm` next to the imported file in the library and deletes the `.mkv` stub, so the `.strm` becomes the episode or movie for Jellyfin. It only ever deletes VODarr's own stubs, never real videos.

Do **not** list `strm` under **Settings → Media Management → Import Extra Files** in arr: arr deletes a file's extras whenever that file is deleted or found missing, so it would remove the `.strm` along with the stub. Auto-configure removes it for you. Turning on **Unmonitor Deleted Episodes/Movies** in the same settings page stops arr from searching again once it notices the stub is gone.

## ⚠️ Security — credentials in `.strm` files

Every `.strm` file contains your Xtream username and password in the stream URL:

```
http://provider.example.com/movie/username/password/12345.mkv
```

This is a limitation of the Xtream Codes protocol — credentials are embedded in stream URLs by design and cannot be removed.

**What this means in practice:**

- Anyone with read access to your `.strm` output directory can see your Xtream credentials.
- Media players (Jellyfin, Plex, Emby) and library scanners will have access to these files in the normal course of operation.
- If your media server logs playback errors, the full URL (including credentials) may appear in logs.

**Recommended mitigations:**

- Restrict the output directory to the minimum set of users/processes that need it (`chmod 750`).
- Do not expose the output directory publicly or via an unauthenticated network share.
- If your IPTV provider supports sub-accounts or connection limits, create a dedicated account for VODarr with a unique password. That way, if credentials are ever exposed, you can rotate just that account without affecting other devices.
- Enable VODarr web UI authentication (`server.web_username` / `server.web_password`) if the web port is accessible on your network.

## Configuration

```yaml
xtream:
  url: "http://provider.example.com"  # Base URL, no trailing slash
  username: "user"
  password: "pass"

tmdb:
  api_key: "your-tmdb-api-key"        # Free at themoviedb.org/settings/api
  # tvdb_api_key: "your-tvdb-api-key" # Optional; enables TVDB fallback for unmatched series

output:
  path: "/data/strm"                  # Root for all .strm files
  movies_dir: "movies"                # → /data/strm/movies/
  series_dir: "tv"                    # → /data/strm/tv/

sync:
  interval: "6h"                      # Go duration: 1h, 6h, 24h, etc.
  on_startup: true                    # Sync on start, unless the cached index is younger than one interval
  parallelism: 10                     # Concurrent workers for TMDB enrichment (1–20)
  grace_cycles: 3                     # Syncs to retain items missing from provider before removing (0 = immediate)
  # title_cleanup_patterns:           # Regex patterns stripped from stream names before TMDB search
  #   - '\s*\(DUBBED\)'
  #   - '\s*\[EXTENDED\]'

server:
  newznab_port: 9091
  qbit_port: 9092
  web_port: 9090
  # Optional auth
  web_username: ""
  web_password: ""
  api_key: ""                         # Newznab API key; empty = no auth
  qbit_username: ""                   # qBittorrent auth (if required by arr)
  qbit_password: ""
  external_url: ""                    # Public base URL (e.g. http://vodarr:9090); used in webhook URLs sent to arr

logging:
  level: "info"                       # debug | info | warn | error

# arr instances for auto-configure (Settings → Arr Instances → Setup)
# arr:
#   instances:
#     - name: "Sonarr"
#       type: sonarr                  # "sonarr" or "radarr"
#       url: "http://<host>:8989"
#       api_key: "<api-key>"
#     - name: "Radarr"
#       type: radarr
#       url: "http://<host>:7878"
#       api_key: "<api-key>"
```

## Dashboard & monitoring

The web UI dashboard shows live sync statistics including total items, unenriched count, grace-retained items, last expired items, and the duration of the most recent sync. A **Sync History** table lists the last 20 sync runs with per-run counts, refreshed automatically every 5 seconds.

## Automatic searching

Sonarr and Radarr only grab on their own through RSS sync, which shows an indexer's *recent* releases. An IPTV catalog is not a stream of new releases, so RSS never surfaces most of it, and a "Refresh" in arr never searches. Instead, after every sync VODarr asks each configured arr instance (the `arr.instances` in config) for its **wanted/missing** list, keeps only the episodes and movies VODarr actually has, and starts a search for exactly those. Items already searched are not asked for again for 24 hours, so a release arr rejects is not searched over and over.

Disable with `arr.auto_search: false`. Ticking **Start search for missing** when adding a series or movie in arr still gives you an immediate search, without waiting for the next sync.

## Excluding provider groups

Providers often carry the same show or movie in several language groups (e.g. `EN | Series` and `ES | Series`). To stop Sonarr/Radarr from ever being handed a stream from a group you don't want, open **Settings → Provider Groups**, load the group list, and untick the groups to leave out. The filter box plus **Exclude all shown** makes it quick to drop every group matching, say, `ES`.

Saving takes effect at once: items from excluded groups disappear from searches immediately, with no restart. They are left out of every later sync without going through the grace period, and VODarr does not delete any files for them. The choice is stored in `config.yml` as `sync.excluded_categories`.

## Manual matching

If a title is matched to the wrong show or movie (or not matched at all), open **Content**, find it, and click **Fix match**. Search TMDB and pick the right entry, or type a TMDB ID directly. For series you can also set the **TVDB ID**, which is what Sonarr searches by, for shows TMDB has no TVDB link for.

A manual match applies immediately and is kept across every sync and restart. It is stored in `matches.json` next to `config.yml`. **Reset to automatic** removes it and re-runs normal matching.

## Grace period for provider outages

When a title disappears from the provider catalog (e.g. during a temporary outage), VODarr retains it in the index for a configurable number of syncs before removing its `.strm` files. This prevents Sonarr/Radarr from losing track of content during short-lived outages.

Configure via `sync.grace_cycles` (default: 3). Set to `0` to remove items immediately on the next sync that doesn't return them.

## Refreshing stream URLs

If you change your Xtream password, all existing `.strm` files will point to the old credentials. After updating the config, trigger a full rewrite without running a new sync:

```
POST /api/strm/refresh
```

This rewrites every `.strm` file on disk with the current credentials from the config.

## Architecture & design decisions

See [`docs/intent.md`](docs/intent.md) for a detailed breakdown of the architecture, data flow, and the reasoning behind key design choices.
