# GeoVerse Map Server

轻量、易部署的地理空间数据分发服务，Go 实现，**单二进制、纯 Go（无 CGO）、零外部运行时依赖**。

- **矢量切片**：Mapbox Vector Tile（MVT/PBF），XYZ 与 WMTS 两种访问方式，附 TileJSON 3.0
- **OGC 常用格式与接口**：OGC API - Features（GeoJSON）、WMTS 1.0、GeoPackage、MVT
- **多数据源统一转换**：
  - 数据库：**PostGIS**（`ST_AsMVT` 动态切片下推，要素查询 bbox 下推）
  - 静态文件：**MBTiles**（矢量/栅格预切片）、**GeoJSON**、**GeoPackage**
- 两级切片缓存（内存 LRU + 可选磁盘持久缓存）、ETag/304、gzip 协商、CORS、健康检查、优雅退出
- 可选 **API Key 鉴权**（Bearer / X-API-Key / query 参数三种携带方式）
- 可选 **MCP 端点**（Model Context Protocol，Streamable HTTP）：LLM 智能体可直接发现图层、查询要素
- **空间算法插件框架**：最短路径（室内/室外，A*）、等时圈（marching squares 轮廓）、路径匹配（Newson-Krumm HMM）、DBSCAN 聚类（网格加速）；`POST /algorithms/{name}` 与 MCP `algo_*` 工具双入口，详见 [docs/ALGORITHMS.md](docs/ALGORITHMS.md)

架构与详细设计见 [docs/DESIGN.md](docs/DESIGN.md)；开放地理数据的获取渠道
汇总见 [docs/OPEN_DATA.md](docs/OPEN_DATA.md)。

## 快速开始

```bash
make build                                # 产出 bin/geoverse（静态二进制）
./bin/geoverse -config config.example.yaml
```

示例配置自带三个开箱即用的图层（数据在 `examples/data/`，来源与许可见
[docs/OPEN_DATA.md](docs/OPEN_DATA.md)）：

- `countries` —— 世界国家边界，Natural Earth 1:110m（公有领域）
- `places` —— 世界主要城市点，Natural Earth 1:110m（公有领域）
- `cities` —— 中国主要城市演示数据

启动后用浏览器打开 `examples/viewer.html` 可直接看到三层叠加的矢量切片渲染。

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
| `GET /algorithms` | 空间算法清单（自描述 JSON Schema）|
| `POST /algorithms/{name}` | 执行算法（shortest_path / isochrone / map_match / dbscan）|

切片坐标体系为 WebMercatorQuad（EPSG:3857）。空白区域切片返回 `204 No Content`。

## 缓存

- **一级**：进程内 LRU + TTL（`cache.max_entries` / `cache.ttl`）
- **二级**（可选）：磁盘缓存（`cache.disk.*`），原子写入（临时文件 + rename），
  按文件 mtime 过期，后台守护协程定期清理过期项并在超过 `max_size_mb`
  时按最旧优先淘汰。重启后依然命中，适合把动态生成的 PostGIS/内存引擎
  切片"越用越热"地固化下来。

## 鉴权

```yaml
auth:
  enabled: true
  api_keys: ["your-secret-key"]     # 或环境变量 GEOVERSE_API_KEYS=k1,k2
```

开启后除 `/health` 外全部端点要求 API Key，三种携带方式任选：

```bash
curl -H "Authorization: Bearer your-secret-key" http://localhost:8080/catalog
curl -H "X-API-Key: your-secret-key" http://localhost:8080/catalog
curl "http://localhost:8080/tiles/cities/6/52/24.pbf?api_key=your-secret-key"  # QGIS 等仅支持 URL 的客户端
```

密钥比较使用 SHA-256 摘要 + 常数时间比较。更复杂的需求（OIDC、配额）建议交给前置网关。

## MCP（供 LLM 智能体调用）

开启 `mcp.enabled` 后，服务在 `mcp.path`（默认 `/mcp`）提供一个
Model Context Protocol 端点（Streamable HTTP 传输、无状态模式），外部
Agent（Claude、各类 MCP 客户端）可直接把本服务当作工具箱使用：

| 工具 | 说明 |
|---|---|
| `list_layers` | 列出全部图层：格式、范围、zoom、访问 URL |
| `describe_layer` | 单图层元数据（TileJSON 风格，含矢量字段清单）|
| `query_features` | 按 bbox/分页查询要素，返回 GeoJSON |
| `get_feature` | 按 id 取单要素 |
| `server_status` | 服务与各数据源健康状态 |

在 Claude Code 中接入：

```bash
claude mcp add --transport http geoverse http://localhost:8080/mcp \
  --header "X-API-Key: your-secret-key"   # 开了鉴权时
```

鉴权开启时 MCP 端点同样受 API Key 保护。开启算法端点后，每个算法还会
自动成为 `algo_{name}` 工具（如 `algo_shortest_path`、`algo_isochrone`），
智能体可以直接做路径规划、等时圈、轨迹匹配与聚类分析。

## 空间算法

配置 `networks` 后（从任意 LineString 要素源构建可路由图，支持多层室内），
即可调用算法端点：

```bash
# 最短路径（跨楼层：室外入口 → 二层走廊）
curl -X POST localhost:8080/algorithms/shortest_path \
  -d '{"network":"campus","from":[116.300,39.990],"to":[116.3055,39.9925],"to_level":2}'

# 等时圈（步行 2 分钟 / 5 分钟）
curl -X POST localhost:8080/algorithms/isochrone \
  -d '{"network":"campus","origin":[116.302,39.992],"cutoffs":[120,300]}'

# GPS 轨迹匹配（HMM）
curl -X POST localhost:8080/algorithms/map_match \
  -d '{"network":"campus","trace":[[116.3001,39.9901],[116.3010,39.9899],[116.3021,39.9901]]}'

# DBSCAN 聚类
curl -X POST localhost:8080/algorithms/dbscan \
  -d '{"collection":"places","eps_m":200000,"min_points":3}'
```

算法设计、采用的改进（A*、gridded isochrone、Newson-Krumm HMM、网格加速
DBSCAN）与扩展规划（CH/ALT、OD 矩阵、TSP、HDBSCAN、KDE 等）见
[docs/ALGORITHMS.md](docs/ALGORITHMS.md)。

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
make test   # 单元 + 集成测试（无需外部服务）
make vet
```

代码布局见 [docs/DESIGN.md](docs/DESIGN.md) 第 7 节，贡献流程见
[CONTRIBUTING.md](CONTRIBUTING.md)。CI（gofmt / vet / test -race / build /
docker build）见 `.github/workflows/ci.yml`。

## 许可

代码以 [MIT](LICENSE) 许可发布。`examples/data/` 中的 Natural Earth 数据
为公有领域（Made with Natural Earth），自制演示数据随项目 MIT。
