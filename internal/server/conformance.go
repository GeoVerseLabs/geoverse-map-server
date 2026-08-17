package server

// Conformance class URIs this server may declare via GET /conformance.
// Every URI listed in declaredConformance must have a matching entry in
// conformanceEvidence (conformance_test.go); TestConformanceClassesHaveEvidence
// fails the build if a class is declared without a check that actually
// exercises the behaviour it promises, or if a check exists for a class we
// no longer declare (dead evidence is a sign the declaration regressed).
const (
	confCommonCore   = "http://www.opengis.net/spec/ogcapi-common-1/1.0/conf/core"
	confFeaturesCore = "http://www.opengis.net/spec/ogcapi-features-1/1.0/conf/core"
	confGeoJSON      = "http://www.opengis.net/spec/ogcapi-features-1/1.0/conf/geojson"
	confTilesCore    = "http://www.opengis.net/spec/ogcapi-tiles-1/1.0/conf/core"
)

// declaredConformance is served verbatim by GET /conformance. See
// docs/CONFORMANCE.md for the evidence behind each entry.
var declaredConformance = []string{
	confCommonCore,
	confFeaturesCore,
	confGeoJSON,
	confTilesCore,
}
