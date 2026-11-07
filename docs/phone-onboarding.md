# Phone onboarding

A phone that joins a node's access point can install ATAK and load its
settings from the node, with no internet. This page covers the node side
that exists today and what is still to come.

## Endpoints

Public, no login (a phone that has just joined has no session):

| Method | Path | What it does |
| --- | --- | --- |
| GET | `/onboard/status` | JSON: whether an APK is stored (name, size, SHA-256, upload time), the plain-HTTP `apkUrl` and `packageUrl` for the address the phone used, and the team, role and map source names to offer. |
| GET, HEAD | `/onboard/atak.apk` | The stored APK as `application/vnd.android.package-archive`. Supports `Range`, so a dropped download resumes. The ETag is the SHA-256. 404 until an APK is uploaded. |
| GET | `/onboard/atak-setup.zip` | An ATAK data package built per request. Optional query: `callsign` (up to 40 letters, digits, space, `-`, `_`, `.`), `team` (one of ATAK's 14 team colors), `role` (one of ATAK's 8 roles). 400 on a bad value. |

Behind login (`/api/` is protected by the frontend auth middleware):

| Method | Path | What it does |
| --- | --- | --- |
| POST | `/api/onboard/atak` | Upload the APK as multipart field `file`. The body streams to disk; it is never spooled to `/tmp` (RAM on OpenWrt). Rejects files over `onboard.maxApkBytes` (413) and files that are not APKs (400: no `AndroidManifest.xml` or no `classes*.dex`). A rejected upload leaves the previous APK in place. |
| DELETE | `/api/onboard/atak` | Remove the APK. |

## The settings package

`/onboard/atak-setup.zip` is a standard ATAK data package:

```
MANIFEST/manifest.xml        uid openmanet-onboard-settings, so a re-import replaces it
prefs/openmanet.pref         locationCallsign, locationTeam, atakRoleType (only those given)
mapsources/<layer>.xml       one MOBAC customMapSource per onboard.mapSources entry
```

Each map source points at `http://<address the phone used>:<HTTP port>/tiles/<layer>/{$z}/{$x}/{$y}`.
Using the request's own address means the same package works on every
node, and on phones that cannot resolve node names (Android keeps using
mobile data DNS on a Wi-Fi network with no internet). The `/tiles/`
endpoint itself is GHO-95.

The URLs are plain HTTP on purpose. ATAK downloads the package itself and
does not trust the node's self-signed HTTPS certificate.

The APK URL is plain HTTP too, so the setup page that links it must be
served over HTTP (port 8080): Chrome blocks an HTTP download started from
an HTTPS page. Recent Chrome may still show an "insecure download" prompt
for an APK over HTTP; the user taps Keep. Bench-check this on the issued
phone model.

A phone imports it by opening (ATAK 5.1 or newer):

```
tak://com.atakmap.app/import?url=<percent-encoded packageUrl with query>
```

ATAK asks the user to confirm once, then imports the preferences and map
sources.

## Configuration

See `onboard.*` in `example_config.yml`. Onboarding is on by default and
offers nothing until an APK is uploaded. `onboard.dir` must be on
persistent storage.

## Still to come

1. The phone-facing setup page in the web UI (download, callsign and team,
   one-tap configure) and the admin upload control.
2. The setup page popping up when a phone joins the access point.
3. Plug-in setup: the node installs and configures ATAK over USB or
   wireless debugging, with permissions granted and battery optimization
   off.
4. Copying the APK to every node on the mesh.

## Licensing note

ATAK-CIV is free on Google Play and export-classified EAR99, but no
published licence covers redistributing current 5.x builds. The firmware
therefore ships no APK; an operator uploads one to the node.
