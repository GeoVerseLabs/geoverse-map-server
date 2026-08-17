package server

// Shared OGC API Common link-relation builders. Landing, the OGC Tiles
// resources and the feature collections all point back at the same
// self/service-desc/conformance/data surface; centralising the construction
// keeps that surface from drifting between handlers as new resources land.

func selfLink(href, mediaType string) link {
	return link{Href: href, Rel: "self", Type: mediaType}
}

func serviceDescLink(base string) link {
	return link{
		Href:  base + "/api",
		Rel:   "service-desc",
		Type:  "application/vnd.oai.openapi+json;version=3.0",
		Title: "OpenAPI 3.0 service description",
	}
}

func serviceDocLink(base string) link {
	return link{
		Href:  "https://github.com/GeoVerseLabs/geoverse-map-server/blob/main/docs/DESIGN.md",
		Rel:   "service-doc",
		Type:  "text/markdown",
		Title: "Human-readable service documentation",
	}
}

func conformanceLink(base string) link {
	return link{Href: base + "/conformance", Rel: "conformance", Type: "application/json"}
}

func dataLink(base string) link {
	return link{Href: base + "/collections", Rel: "data", Type: "application/json", Title: "feature collections"}
}
