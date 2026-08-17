# Conformance

`GET /conformance` only lists a class when this repository has a passing,
automated check that exercises the behaviour the class promises —
`internal/server/conformance_test.go`'s `TestConformanceClassesHaveEvidence`
enforces the mapping in both directions: a declared class with no registered
check fails the build (over-claiming), and a registered check for a class no
longer declared also fails the build (dead evidence hiding a silent scope
shrink). This document is the human-readable index into that mapping.

| Conformance class | Evidence | Notes |
|---|---|---|
| [OGC API - Common Part 1: Core](http://www.opengis.net/spec/ogcapi-common-1/1.0/conf/core) | `verifyCommonCore` (internal Go fixture) | Landing page carries `self`/`service-desc`/`conformance`/`data` links; `GET /api` and `GET /conformance` both return 200. |
| [OGC API - Features Part 1: Core](http://www.opengis.net/spec/ogcapi-features-1/1.0/conf/core) | `verifyFeaturesCore` (internal Go fixture) + `TestCollectionsAndItems` | `/collections`, `/collections/{id}`, `/collections/{id}/items`, `/collections/{id}/items/{fid}`, bbox/limit/offset. |
| [OGC API - Features Part 1: GeoJSON](http://www.opengis.net/spec/ogcapi-features-1/1.0/conf/geojson) | `verifyGeoJSON` (internal Go fixture) | Items responses are served as `application/geo+json`. |
| [OGC API - Tiles Part 1: Core](http://www.opengis.net/spec/ogcapi-tiles-1/1.0/conf/core) | `verifyTilesCore` (internal Go fixture) + `TestTileMatrixSets`, `TestCollectionTileset`, `TestStandardTileMatchesXYZBytes` | `/tileMatrixSets`, `/collections/{id}/tiles`, standard tile route; byte-identical to the existing XYZ route on the same z/x/y. |

## What this table is, and isn't

- **Is**: an accurate statement of what this repository's own test suite
  verifies, tied to specific test names so the claim can't silently drift —
  if a test is renamed or deleted, `TestConformanceClassesHaveEvidence` will
  fail the next time `/conformance`'s declared list is touched without a
  matching evidence-map update.
- **Isn't**: certification against the official OGC Abstract/Executable Test
  Suites (ATS/ETS). This repository does not currently run the official
  `ets-ogcapi-tiles10` or `ets-ogcapi-features10` suites (TEAM Engine,
  Java-based) — wiring those in was scoped as an optional, non-blocking CI
  job in [[GEOVERSE_MAP_SERVER_S2_S3_OPERATIONAL_PLAN]] §4.1 and is deferred;
  the internal Go fixtures above are the only evidence backing the classes
  in the table. If official certification is ever pursued, add a row here
  pointing at the archived ETS run output rather than replacing this table.

## Explicitly out of scope

The following endpoints are this server's own extensions, not OGC API
resources, and carry no conformance claim: XYZ tile templates
(`/tiles/{layer}/{z}/{x}/{y}.{ext}`), WMTS (`/wmts/1.0.0/...`), PMTiles
archive downloads (`/archives/{name}.pmtiles`), spatial algorithms
(`/algorithms*`), MCP (`/mcp`), and `/admin/*` management. `docs/DESIGN.md`
and `README.md` document their behaviour instead.
