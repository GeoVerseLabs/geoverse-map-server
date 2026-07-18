package server

import (
	"encoding/xml"
	"fmt"
	"math"
	"net/http"
	"strings"
	"text/template"
)

const wmtsTMS = "GoogleMapsCompatible"

// handleWMTSTile serves the WMTS RESTful GetTile:
// /wmts/1.0.0/{layer}/{style}/{tms}/{z}/{y}/{x}.{ext}
// Note WMTS orders TileRow (y) before TileCol (x).
func (s *Server) handleWMTSTile(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("tms") != wmtsTMS {
		writeError(w, http.StatusBadRequest, "unknown TileMatrixSet; only "+wmtsTMS+" is served")
		return
	}
	xRaw, ext, ok := splitExt(r.PathValue("xext"))
	if !ok {
		writeError(w, http.StatusBadRequest, "tile path must end in .pbf, .mvt, .png, .jpg or .webp")
		return
	}
	tile, err := parseTile(r.PathValue("z"), xRaw, r.PathValue("y"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.serveTile(w, r, r.PathValue("layer"), tile, ext)
}

type wmtsLayer struct {
	ID, Title, Format, Ext string
	Bounds                 [4]float64
}

type wmtsMatrix struct {
	ID               int
	ScaleDenominator string
	MatrixSize       uint64
}

var wmtsTemplate = template.Must(template.New("wmts").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<Capabilities xmlns="http://www.opengis.net/wmts/1.0"
    xmlns:ows="http://www.opengis.net/ows/1.1"
    xmlns:xlink="http://www.w3.org/1999/xlink"
    version="1.0.0">
  <ows:ServiceIdentification>
    <ows:Title>GeoVerse Map Server</ows:Title>
    <ows:ServiceType>OGC WMTS</ows:ServiceType>
    <ows:ServiceTypeVersion>1.0.0</ows:ServiceTypeVersion>
  </ows:ServiceIdentification>
  <Contents>
{{- range .Layers}}
    <Layer>
      <ows:Title>{{.Title}}</ows:Title>
      <ows:Identifier>{{.ID}}</ows:Identifier>
      <ows:WGS84BoundingBox>
        <ows:LowerCorner>{{index .Bounds 0}} {{index .Bounds 1}}</ows:LowerCorner>
        <ows:UpperCorner>{{index .Bounds 2}} {{index .Bounds 3}}</ows:UpperCorner>
      </ows:WGS84BoundingBox>
      <Style isDefault="true"><ows:Identifier>default</ows:Identifier></Style>
      <Format>{{.Format}}</Format>
      <TileMatrixSetLink><TileMatrixSet>{{$.TMS}}</TileMatrixSet></TileMatrixSetLink>
      <ResourceURL format="{{.Format}}" resourceType="tile"
        template="{{$.Base}}/wmts/1.0.0/{{.ID}}/default/{{$.TMS}}/{TileMatrix}/{TileRow}/{TileCol}.{{.Ext}}"/>
    </Layer>
{{- end}}
    <TileMatrixSet>
      <ows:Identifier>{{.TMS}}</ows:Identifier>
      <ows:SupportedCRS>urn:ogc:def:crs:EPSG::3857</ows:SupportedCRS>
      <WellKnownScaleSet>urn:ogc:def:wkss:OGC:1.0:GoogleMapsCompatible</WellKnownScaleSet>
{{- range .Matrices}}
      <TileMatrix>
        <ows:Identifier>{{.ID}}</ows:Identifier>
        <ScaleDenominator>{{.ScaleDenominator}}</ScaleDenominator>
        <TopLeftCorner>-20037508.342789244 20037508.342789244</TopLeftCorner>
        <TileWidth>256</TileWidth>
        <TileHeight>256</TileHeight>
        <MatrixWidth>{{.MatrixSize}}</MatrixWidth>
        <MatrixHeight>{{.MatrixSize}}</MatrixHeight>
      </TileMatrix>
{{- end}}
    </TileMatrixSet>
  </Contents>
</Capabilities>
`))

// handleWMTSCapabilities serves GET /wmts/1.0.0/WMTSCapabilities.xml.
func (s *Server) handleWMTSCapabilities(w http.ResponseWriter, r *http.Request) {
	var layers []wmtsLayer
	maxZ := 0
	for _, ts := range s.reg.TileSources() {
		info := ts.TileInfo()
		title := info.Title
		if title == "" {
			title = info.Name
		}
		layers = append(layers, wmtsLayer{
			ID:     xmlEscape(info.Name),
			Title:  xmlEscape(title),
			Format: info.Format.ContentType(),
			Ext:    string(info.Format),
			Bounds: info.Bounds,
		})
		if info.MaxZoom > maxZ {
			maxZ = info.MaxZoom
		}
	}
	var matrices []wmtsMatrix
	for z := 0; z <= maxZ; z++ {
		matrices = append(matrices, wmtsMatrix{
			ID: z,
			// 559082264.0287178 is the level-0 scale denominator of
			// the GoogleMapsCompatible well-known scale set.
			ScaleDenominator: fmt.Sprintf("%.10f", 559082264.0287178/math.Pow(2, float64(z))),
			MatrixSize:       uint64(1) << z,
		})
	}
	data := struct {
		Base     string
		TMS      string
		Layers   []wmtsLayer
		Matrices []wmtsMatrix
	}{s.baseURL(r), wmtsTMS, layers, matrices}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	if err := wmtsTemplate.Execute(w, data); err != nil {
		s.log.Error("wmts capabilities", "error", err)
	}
}

func xmlEscape(s string) string {
	var sb strings.Builder
	_ = xml.EscapeText(&sb, []byte(s))
	return sb.String()
}
