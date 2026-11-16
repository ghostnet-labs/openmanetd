# Offline maps for ATAK phones

Every node serves map tiles to the phones on it (GHO-95). A phone that went
through [phone onboarding](phone-onboarding.md) gets two map layers in ATAK,
**Manet Satellite** and **Manet Topo**. Both point at the node the phone is
connected to:

```
http://<address the phone used>:8080/tiles/<layer>/{z}/{x}/{y}
```

The route is public (ATAK sends no login) and plain HTTP (ATAK does not
trust the node's self-signed certificate).

## Where a tile comes from

1. **The node's disk cache.** Served at once, with or without internet.
2. **Peer nodes** in `tiles.peers`, in order. A mast that downloaded the
   area ahead of time answers here. Requests to a peer carry
   `X-Openmanet-Tile-Peer: 1`; a peer answers those from its cache or the
   internet but never asks its own peers, so nodes that list each other
   cannot loop.
3. **The internet**, from the layer's `upstream` URL, when the mesh has a
   gateway (for example the Starlink mast).

A tile found in step 2 or 3 is stored and served. A source that fails (no
route, timeout, 5xx, 429) is skipped for `tiles.offlineBackoff` (30 s), so an
offline node answers a whole screen of misses at once instead of timing out
tile by tile. A source that answers "no such tile" (404) is not a failure.
Anything that is not an image (a captive portal or error page sent with 200)
is never cached.

## Layers

| Layer | Source | Format | Zooms |
|---|---|---|---|
| `satellite` | USGS National Map imagery (`USGSImageryOnly`) | JPEG | 0 to 16 |
| `topo` | USGS National Map topo (`USGSTopo`) | JPEG | 0 to 16 |

USGS basemaps are public domain, so nodes may cache and redistribute them.
Their tile caches stop at zoom 16 (about 2.4 m per pixel); ATAK enlarges
zoom 16 when you zoom in further. Esri and Google tiles have terms of use
that forbid this kind of caching, so they are not defaults.

A layer with no `upstream` is served from the cache and peers only, for an
area loaded onto the node by hand.

## Limits

- `tiles.maxBytes` (512 MiB) caps the cache. When it is exceeded, the least
  recently used tiles are deleted until it is back under 90%. A cache hit
  refreshes a tile's age at most once an hour, so serving maps does not
  wear the flash.
- `tiles.minFreeBytes` (64 MiB) is always left free on the cache's
  filesystem. Below it, tiles are still served to the phone but not stored,
  and the cache shrinks to give the space back.
- The cache lives on persistent storage (`/srv/openmanet/tiles`), never in
  `/tmp`, which is RAM on OpenWrt.
- At most 4 on-demand fetches run at once; concurrent requests for one tile
  share one fetch.

## Downloading an area ahead of time

Do this while the node has internet, before going somewhere without it.

**From the web app:** Settings › Phones › Offline maps. Enter the center
(or press **Use node position** when the node has a GPS fix), a radius and
the most detail you need, then **Download area**. The panel estimates the
tiles and size first, shows progress, and can stop the download. Tiles
already stored are skipped, so running it again only fills gaps.

**From config, for a mast:** list the area under `tiles.seed`. It is
downloaded each time openmanetd starts, skipping tiles already stored.

```yaml
tiles:
  seed:
    - name: Eagle Mountain
      lat: 40.314
      lon: -112.006
      radiusKm: 12
      maxZoom: 16
```

Other nodes then list the mast as a peer:

```yaml
tiles:
  peers:
    - http://10.41.0.1:8080
```

Sizes, per layer, to zoom 16: a 10 km radius is about 2,600 tiles
(roughly 50 MB); 25 km is about 15,600 tiles (roughly 300 MB), so both
layers at 25 km need `tiles.maxBytes` raised above the 512 MiB default. One download
is refused above 250,000 tiles or when it would not fit in 90% of
`tiles.maxBytes`. A download stops by itself after 50 failures in a row (no
source answering) or when the disk reaches its free-space floor, and says
why in the panel.

## API

| Method | Path | Login | |
|---|---|---|---|
| GET | `/tiles/<layer>/<z>/<x>/<y>[.ext]` | no | One tile. 404 when no source has it. `Cache-Control: max-age=86400`. |
| GET | `/api/tiles/status` | yes | Cache size and limit, free space, layers, internet and peer health, counters, the current or last download. `{"enabled": false}` when the cache is off. |
| POST | `/api/tiles/seed` | yes | `{"lat":40.314,"lon":-112.006,"radiusKm":10,"maxZoom":16,"layers":["satellite"],"name":"Town"}`; 202 when started, 400 for a bad or oversized area, 409 while another download runs. |
| DELETE | `/api/tiles/seed` | yes | Stop the running download. |

## Guard against blank maps

The onboarding settings package only lists map sources that this node's
tile cache serves, with the same tile type, and clips their zooms to the
served range. Two tests keep it that way:

- `internal/config` `TestDefaultMapSourcesAreServed`: every default map
  source has a matching default tile layer.
- `internal/frontend` `TestOnboardPackage_everyMapSourceLoads`: builds the
  real settings package, takes each map source URL out of it, and fetches a
  tile through the same server with no login.

## Not covered yet

- Tiles are never refreshed once stored. Delete the cache directory to
  start over.
- iPhones (iTAK) may not join mesh multicast; a CoT TCP endpoint for them
  is a separate piece of work noted on GHO-95.
- Seeding needs internet on at least one node; there is no import of a
  ready-made MBTiles file yet.
