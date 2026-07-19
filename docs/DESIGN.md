# GeoVerse Map Server 设计文档

## 1. 目标

构建一个**轻量、易部署**的地理数据分发服务，定位类似于精简版的 tegola / pg_tileserv / tileserver-gl：

- 单一静态二进制（纯 Go、无 CGO），`scp` 上去就能跑，也可 Docker 一键部署
- 支持**矢量切片**（Mapbox Vector Tile / MVT）
- 支持 **OGC 常用格式与接口**：OGC API - Features（GeoJSON）、WMTS、GeoPackage、MVT（OGC 社区标准）
- 支持**多数据源**并做统一转换：数据库（PostGIS）与静态文件（MBTiles / GeoJSON / GeoPackage）
- 内置缓存、CORS、健康检查，配置文件驱动，零外部运行时依赖

## 2. 总体架构

```
                    ┌────────────────────────────────────────────┐
                    │                HTTP Server                  │
                    │  (net/http, Go 1.22+ pattern routing)       │
                    │                                             │
   客户端            │  /tiles/{layer}/{z}/{x}/{y}.pbf   XYZ 切片  │
  MapLibre  ──────► │  /tiles/{layer}.json              TileJSON  │
  OpenLayers        │  /wmts/1.0.0/...                  WMTS      │
  QGIS              │  /collections/...          OGC API Features │
  Leaflet           │  /, /conformance, /health   服务元数据      │
                    └───────────────┬────────────────────────────┘
                                    │ middleware: 日志 / CORS / gzip / recover
                                    ▼
                    ┌────────────────────────────────────────────┐
                    │              Tile Cache (LRU+TTL)           │
                    └───────────────┬────────────────────────────┘
                                    ▼
                    ┌────────────────────────────────────────────┐
                    │            Source Registry（统一抽象）      │
                    │   TileSource / FeatureSource 两个接口       │
                    └──┬──────────┬──────────┬──────────┬────────┘
                       ▼          ▼          ▼          ▼
                   PostGIS     MBTiles    GeoJSON   GeoPackage
                  (ST_AsMVT)  (预切片)   (内存引擎)  (内存引擎)
                    数据库      静态文件    静态文件     静态文件
```

## 3. 数据源抽象

两个核心接口（`internal/source/source.go`）：

```go
// TileSource 提供 z/x/y 切片（矢量或栅格）
type TileSource interface {
    Tile(ctx context.Context, z, x, y uint32) ([]byte, error)
    TileInfo() TileInfo // 格式、zoom 范围、bounds、图层描述
}

// FeatureSource 提供 OGC API - Features 要素查询
type FeatureSource interface {
    Features(ctx context.Context, q FeatureQuery) (*FeatureCollection, error)
    Feature(ctx context.Context, id string) (*Feature, error)
    CollectionInfo() CollectionInfo
}
```

一个数据源可以同时实现两个接口（如 PostGIS、GeoJSON、GeoPackage），
也可以只实现其一（MBTiles 只做 TileSource）。

### 3.1 各数据源实现策略

| 数据源 | 类型 | 切片方式 | 要素查询 | 依赖 |
|---|---|---|---|---|
| PostGIS | 数据库 | 下推 `ST_AsMVT`/`ST_TileEnvelope` 动态生成 | SQL + `ST_AsGeoJSON`，bbox 下推 | jackc/pgx（纯 Go）|
| MBTiles | 静态文件 | 直接读预切片（vector pbf 或 png/jpg/webp 栅格），TMS y 翻转 | 不支持 | modernc.org/sqlite（纯 Go）|
| GeoJSON | 静态文件 | 启动时载入内存引擎，动态裁剪+简化+编码 MVT | 内存 bbox 过滤 | paulmach/orb |
| GeoPackage | 静态文件 | 启动时解析 GPB→WKB 载入同一内存引擎 | 同上 | modernc.org/sqlite + orb/encoding/wkb |

### 3.2 内存要素引擎（memengine）

GeoJSON 与 GeoPackage 共享同一个内存引擎：

- 启动时把要素加载进内存，逐要素预计算 bbox，构建轻量网格索引
- 切片请求：bbox 粗筛 → 按 zoom 做 Douglas-Peucker 简化 → 裁剪到 tile
  （带 buffer）→ `orb/encoding/mvt` 编码 → gzip
- 要素请求：bbox 过滤 + limit/offset 分页，输出 GeoJSON
- 定位是"中小规模静态数据"（十万级要素以内），大数据请用 PostGIS 或预切 MBTiles

## 4. HTTP API 设计

### 4.1 服务元数据（OGC API - Common 风格）

| 路径 | 说明 |
|---|---|
| `GET /` | Landing page：服务信息 + 全部链接 |
| `GET /conformance` | OGC API 一致性声明 |
| `GET /health` | 健康检查（含各数据源状态）|
| `GET /catalog` | 全部图层/集合清单（便于前端发现）|

### 4.2 切片（矢量 + 栅格）

| 路径 | 说明 |
|---|---|
| `GET /tiles/{layer}/{z}/{x}/{y}.{ext}` | XYZ 切片，ext = pbf / mvt / png / jpg / webp |
| `GET /tiles/{layer}.json` | TileJSON 3.0 元数据 |
| `GET /wmts/1.0.0/WMTSCapabilities.xml` | WMTS 能力文档（RESTful）|
| `GET /wmts/1.0.0/{layer}/default/GoogleMapsCompatible/{z}/{y}/{x}.{ext}` | WMTS RESTful GetTile |

坐标体系：Web Mercator（EPSG:3857），tile matrix set 为
`GoogleMapsCompatible`（即 OGC 的 WebMercatorQuad）。

### 4.3 OGC API - Features (Part 1: Core)

| 路径 | 说明 |
|---|---|
| `GET /collections` | 集合列表 |
| `GET /collections/{id}` | 集合描述（extent、links）|
| `GET /collections/{id}/items?bbox=&limit=&offset=` | 要素查询，GeoJSON FeatureCollection |
| `GET /collections/{id}/items/{fid}` | 单要素 |

## 5. 缓存

两级结构（`internal/cache`），key = `layer/z/x/y`：

- **一级（内存）**：LRU + TTL，按条目数上限淘汰
- **二级（磁盘，可选）**：普通文件存储（`dir/layer/z/x/y`），临时文件 +
  rename 原子写入；按 mtime 判定 TTL 过期；janitor 协程每 5 分钟清理
  过期项与孤儿临时文件，超过 `max_size_mb` 时按最旧优先淘汰到 90% 水位。
  磁盘命中会回填（promote）到内存一级。
- key 段经过白名单校验（并显式拒绝 `.`/`..`），杜绝路径穿越
- MBTiles 本身就是本地文件读取，标记 `Cacheable=false` 跳过缓存
- 响应带 `ETag`（内容哈希），支持 `If-None-Match` → 304

## 5a. 鉴权

简单 API Key 方案（`auth.enabled`）：middleware 层拦截，`/health` 豁免
（负载均衡探活）。key 可来自配置或 `GEOVERSE_API_KEYS` 环境变量；接受
`Authorization: Bearer`、`X-API-Key` 头或 `api_key` query 参数。比较时
先做 SHA-256 摘要再常数时间比较，避免时序侧信道。定位是"轻量内网/小
团队"场景；OIDC、限流等交由前置反向代理。

## 5b. MCP 端点

`mcp.enabled` 开启后在 `mcp.path`（默认 `/mcp`）暴露 Model Context
Protocol 服务（`internal/mcpserver`）：

- 传输：Streamable HTTP，无状态模式（单 POST → 单 JSON 响应，不提供
  SSE 流；GET 返回 405，符合规范中 server MAY not support 的约定）
- 协议：JSON-RPC 2.0，实现 `initialize` / `ping` / `tools/list` /
  `tools/call`，协议版本协商支持 2024-11-05 / 2025-03-26 / 2025-06-18
- 工具：`list_layers`、`describe_layer`、`query_features`、
  `get_feature`、`server_status`；结果同时以 text 与 structuredContent
  返回
- 零第三方依赖（手写 ~200 行 handler），复用 registry 的既有抽象；
  鉴权 middleware 覆盖 MCP 端点

## 6. 配置

单个 YAML 文件（`config.yaml`），`-config` 指定路径：

```yaml
server:
  host: 0.0.0.0
  port: 8080
  cors: true

cache:
  enabled: true
  max_entries: 10000
  ttl: 5m

sources:
  - name: roads            # PostGIS 动态矢量切片
    type: postgis
    dsn: postgres://user:pass@localhost:5432/gis
    table: public.roads
    geometry_column: geom
    id_column: gid
    srid: 4326
    fields: [name, class]
    min_zoom: 0
    max_zoom: 22

  - name: basemap          # 预切好的 MBTiles
    type: mbtiles
    path: ./data/basemap.mbtiles

  - name: pois             # 静态 GeoJSON
    type: geojson
    path: ./data/pois.geojson

  - name: parcels          # OGC GeoPackage
    type: geopackage
    path: ./data/parcels.gpkg
    layer: parcels
```

## 7. 代码布局

```
cmd/geoverse/            入口（flag 解析、优雅退出）
internal/config/         YAML 配置解析与校验
internal/tilemath/       Web Mercator 切片数学（z/x/y ↔ bbox）
internal/cache/          两级缓存（内存 LRU + 磁盘持久层）
internal/mcpserver/      MCP 端点（JSON-RPC / Streamable HTTP）
internal/source/         接口定义 + registry（按配置构建数据源）
internal/source/postgis/     PostGIS 实现
internal/source/mbtiles/     MBTiles 实现
internal/source/memengine/   内存要素引擎（MVT 编码、要素查询）
internal/source/geojsonsrc/  GeoJSON 加载器 → memengine
internal/source/geopackage/  GeoPackage 加载器（GPB/WKB 解析）→ memengine
internal/server/         HTTP 服务、路由、handler、middleware
docs/                    设计文档
examples/                 示例数据与示例配置
```

## 8. 非目标（保持轻量）

- 不做栅格动态渲染（WMS GetMap 渲染引擎），栅格仅透传 MBTiles 已有切片
- 不做坐标系重投影服务（统一 WebMercatorQuad 输出，源数据支持 4326/3857）
- 鉴权仅到简单 API Key 一层；OIDC/配额/多租户由前置反向代理承担

## 9. 部署

- `make build` → 单二进制 `bin/geoverse`（CGO_ENABLED=0，可静态运行于 scratch/alpine）
- `Dockerfile` 多阶段构建，最终镜像 ~20MB
- `docker run -v ./data:/data -v ./config.yaml:/etc/geoverse/config.yaml -p 8080:8080 geoverse`
