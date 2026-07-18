# GeoVerse Map Server

轻量、易部署的地理空间数据分发服务，Go 实现，**单二进制、纯 Go（无 CGO）、零外部运行时依赖**。

- **矢量切片**：Mapbox Vector Tile（MVT/PBF），XYZ 与 WMTS 两种访问方式，附 TileJSON 3.0
- **OGC 常用格式与接口**：OGC API - Features（GeoJSON）、WMTS 1.0、GeoPackage、MVT
- **多数据源统一转换**：
  - 数据库：**PostGIS**（`ST_AsMVT` 动态切片下推，要素查询 bbox 下推）
  - 静态文件：**MBTiles**（矢量/栅格预切片）、**GeoJSON**、**GeoPackage**
- 内置 LRU+TTL 切片缓存、ETag/304、gzip 协商、CORS、健康检查、优雅退出

架构与详细设计见 [docs/DESIGN.md](docs/DESIGN.md)。

## 快速开始

```bash
make build                                # 产出 bin/geoverse（静态二进制）
./bin/geoverse -config config.example.yaml
```

验证：

```bash
curl http://localhost:8080/                       # 服务信息（landing page）
curl http://localhost:8080/catalog                # 全部图层与访问地址
curl http://localhost:8080/tiles/cities.json      # TileJSON
curl http://localhost:8080/tiles/cities/6/52/24.pbf -o tile.pbf   # 矢量切片
curl http://localhost:8080/collections/cities/items?bbox=115,39,118,41
curl http://localhost:8080/wmts/1.0.0/WMTSCapabilities.xml
```

## 配置

单个 YAML 文件描述服务与数据源，完整示例见 [config.example.yaml](config.example.yaml)：

```yaml
server:
  port: 8080

sources:
  - name: roads              # PostGIS 动态矢量切片
    type: postgis
    dsn: postgres://user:pass@localhost:5432/gis
    table: public.roads

  - name: basemap            # 预切片 MBTiles（矢量或栅格）
    type: mbtiles
    path: ./data/basemap.mbtiles

  - name: pois               # 静态 GeoJSON
    type: geojson
    path: ./data/pois.geojson

  - name: parcels            # OGC GeoPackage
    type: geopackage
    path: ./data/parcels.gpkg
```

SRID/主键/属性列在 PostGIS 源上可省略，服务会自动探测；GeoPackage 支持
EPSG:4326 与 EPSG:3857 图层（3857 自动转 4326）。

## HTTP API

| 端点 | 说明 |
|---|---|
| `GET /` | 服务元数据（OGC API landing page）|
| `GET /conformance` | OGC API 一致性声明 |
| `GET /health` | 健康检查（逐数据源）|
| `GET /catalog` | 全部图层清单与访问 URL |
| `GET /tiles/{layer}/{z}/{x}/{y}.pbf` | XYZ 切片（栅格源为 .png/.jpg/.webp）|
| `GET /tiles/{layer}.json` | TileJSON 3.0 |
| `GET /wmts/1.0.0/WMTSCapabilities.xml` | WMTS 能力文档 |
| `GET /wmts/1.0.0/{layer}/default/GoogleMapsCompatible/{z}/{row}/{col}.pbf` | WMTS RESTful GetTile |
| `GET /collections` | OGC API - Features 集合列表 |
| `GET /collections/{id}` | 集合描述 |
| `GET /collections/{id}/items?bbox=&limit=&offset=` | 要素查询（GeoJSON）|
| `GET /collections/{id}/items/{fid}` | 单要素 |

切片坐标体系为 WebMercatorQuad（EPSG:3857）。空白区域切片返回 `204 No Content`。

### 在 MapLibre GL 中使用

```js
map.addSource('cities', { type: 'vector', url: 'http://localhost:8080/tiles/cities.json' });
map.addLayer({ id: 'cities', type: 'circle', source: 'cities', 'source-layer': 'cities' });
```

QGIS：`图层 → 添加图层 → WMTS` 指向 capabilities 地址，或直接添加
Vector Tiles / OGC API - Features 连接。

## Docker 部署

```bash
make docker
docker run -p 8080:8080 \
  -v $(pwd)/config.yaml:/etc/geoverse/config.yaml \
  -v $(pwd)/data:/data \
  geoverse-map-server:dev
```

## 开发

```bash
make test   # 单元 + 集成测试
make vet
```

代码布局见 [docs/DESIGN.md](docs/DESIGN.md) 第 7 节。

## 许可

MIT
