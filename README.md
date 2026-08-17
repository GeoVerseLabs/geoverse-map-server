# GeoVerse Map Server

轻量、易部署的地理空间数据分发服务，Go 实现，**单二进制、纯 Go（无 CGO）、零外部运行时依赖**。

- **矢量切片**：Mapbox Vector Tile（MVT/PBF），XYZ 与 WMTS 两种访问方式，附 TileJSON 3.0
- **OGC 常用格式与接口**：OGC API - Features（GeoJSON）、WMTS 1.0、GeoPackage、MVT
- **多数据源统一转换**：
  - 数据库：**PostGIS**（`ST_AsMVT` 动态切片下推）与 **MySQL 8 / MariaDB**（bbox 下推、Go 侧 MVT 编码）
  - 静态文件：**PMTiles v3**、**MBTiles**（矢量/栅格预切片）、**GeoJSON**、**GeoPackage**
- **内置数据源 WebUI**：分型表单、真实连接测试、YAML 原子写回与运行时热加载；数据库凭据不回显
- 两级切片缓存（内存 LRU + 可选磁盘持久缓存）、ETag/304、gzip 协商、CORS、健康检查、优雅退出
- 可选 **API Key 鉴权**（Bearer / X-API-Key / query 参数三种携带方式）
- 可选 **MCP 端点**（Model Context Protocol，Streamable HTTP）：LLM 智能体可直接发现图层、查询要素
- **空间算法插件框架**：最短路径（室内/室外，A*）、等时圈（marching squares 轮廓）、路径匹配（Newson-Krumm HMM）、DBSCAN 聚类（网格加速）；`POST /algorithms/{name}` 与 MCP `algo_*` 工具双入口，详见 [docs/ALGORITHMS.md](docs/ALGORITHMS.md)

架构与详细设计见 [docs/DESIGN.md](docs/DESIGN.md)；开放地理数据的获取渠道
汇总见 [docs/OPEN_DATA.md](docs/OPEN_DATA.md)。

## 快速开始

```bash
make build                                # 产出 bin/geoverse（静态二进制）
./bin/geoverse -doctor -config config.example.yaml
./bin/geoverse -config config.example.yaml
```

示例配置自带三个开箱即用的图层（数据在 `examples/data/`，来源与许可见
[docs/OPEN_DATA.md](docs/OPEN_DATA.md)）：

- `countries` —— 世界国家边界，Natural Earth 1:110m（公有领域）
- `places` —— 世界主要城市点，Natural Earth 1:110m（公有领域）
- `cities` —— 中国主要城市演示数据

启动后访问 `http://localhost:8080/admin/` 可打开数据源控制台；打开
`examples/viewer.html` 可直接看到三层叠加的矢量切片渲染。

要同时验证数据库与静态资源，可复用 GeoVerse Live 的 `gv-postgis`，并用
compose 启动仓库自带的 MySQL 8 空间表示例：

```bash
cd deploy
cp .env.example .env       # 填写 MYSQL_PASSWORD / MYSQL_ROOT_PASSWORD
docker compose --profile mysql up -d mysql
cd ..
./bin/geoverse -validate -config examples/config.multisource.yaml
./bin/geoverse -config examples/config.multisource.yaml
# WebUI: http://127.0.0.1:18080/admin/
# 目录中应同时出现 PostGIS、MySQL 8 与三个 GeoJSON 图层
```

历史 `mariadb-gis` 兼容性验证仍保留在
[`examples/config.mariadb.yaml`](examples/config.mariadb.yaml)。

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

assets:
  root: ./data               # 允许读取本地数据文件的目录；不重写 source.path
  enforce_root: true         # 拒绝目录外路径及符号链接逃逸
  max_file_size_mb: 8192
  max_memory_file_size_mb: 256  # GeoJSON / GeoPackage 的内存加载上限

data_sources:
  allowed_schemas: [gis]     # PostGIS/MySQL 只读 schema 白名单，与账号权限独立
  allowed_tables: [gis.roads, gis.warehouse]
  require_readonly_role: true  # 仅影响 -doctor：探测账号是否持有超出 SELECT 的权限

sources:
  - name: roads              # PostGIS 动态矢量切片
    type: postgis
    dsn: postgres://user:pass@localhost:5432/gis
    table: public.roads

  - name: warehouses         # MySQL 8 / MariaDB 空间表
    type: mysql
    dsn: mysql://reader:pass@localhost:3306/gis
    table: gis.warehouse
    geometry_column: location

  - name: basemap            # 预切片 MBTiles（矢量或栅格）
    type: mbtiles
    path: ./data/basemap.mbtiles

  - name: regional           # PMTiles v3 单文件归档
    type: pmtiles
    path: ./data/regional.pmtiles

  - name: pois               # 静态 GeoJSON
    type: geojson
    path: ./data/pois.geojson

  - name: parcels            # OGC GeoPackage
    type: geopackage
    path: ./data/parcels.gpkg
```

SRID/主键/属性列在 PostGIS、MySQL 源上可省略，服务会自动探测；GeoPackage 支持
EPSG:4326 与 EPSG:3857 图层（3857 自动转 4326）。

省略 `assets` 或关闭 `enforce_root` 会保留旧配置的路径行为，便于平滑升级；生产环境建议
显式启用根目录限制和两个大小上限。文件路径仍按进程工作目录解析，`assets.root` 只作为
允许边界。PMTiles 原始归档的每次 Range 请求都会重新检查文件身份，防止运行中符号链接
被替换后越界读取。

省略 `data_sources` 或两个列表都留空同样保持兼容行为（DSN 账号能读到的表都可注册），
`-doctor` 会提醒。allowlist 只拦截"注册到了错误的表"这类配置失误，不是权限机制——
真正的安全边界仍是 DSN 账号自身的 GRANT；`require_readonly_role: true` 时 `-doctor`
额外探测账号在目标表上是否持有超出 SELECT 的权限，探测本身失败（部分托管数据库限制
权限目录查询）只作提示，不算 warning。生产环境的最小权限建库脚本见
[DEPLOY.md 九节](DEPLOY.md)。

### 部署诊断

`-doctor` 对完整配置执行只读诊断，`-inspect` 只检查指定数据源（或 `all`）。两者支持
稳定的文本与 JSON 输出；警告退出 0，配置或数据源错误退出 1，CLI 用法错误退出 2：

```bash
geoverse -doctor -config config.yaml
geoverse -doctor -format json -config config.yaml
geoverse -inspect cities -format json -config config.yaml
geoverse -inspect all -config config.yaml
```

诊断会报告本地资产的规范化路径、大小、数据源能力与瓦片/要素元数据；数据库错误中的
DSN 密码会脱敏。它不启动 HTTP 监听，也不修改配置或数据。

### 数据源 WebUI

`GET /admin/` 是随 Go 二进制嵌入的轻量控制台，无需单独部署前端。侧栏在同一
控制台内提供“数据源 / 服务目录 / 运行统计”三视图；数据源管理沿用 Live Platform
的“按类型填写 → 测试连接 → 保存”交互，目前支持 PostGIS、MySQL 8 / MariaDB、
GeoJSON、MBTiles、PMTiles 与 GeoPackage：

- PostGIS 使用主机、端口、数据库、账号、密码和表名分栏配置；“填入本地
  PostgreSQL 示例”对应 `gv-postgis` 的 `127.0.0.1:5432/geoverse` 与
  `geo.feature`，保存前仍会实际连接。
- MySQL 使用同一套分栏表单与 `mysql://user:pass@host:port/database` 配置格式。
  “填入本地 MySQL / MariaDB 示例”对应 compose profile 创建的
  `127.0.0.1:3308/geoverse_demo`、`warehouse.location` 与只读账号。
- MySQL/MariaDB 的 bbox 过滤在数据库空间索引侧完成，再由 Go 编码为 gzip MVT；
  单个瓦片候选要素上限为 50,000，超出会明确报错，超密数据应提高 zoom 或预切片。
  MariaDB 不提供 `ST_Transform`，因此 MariaDB 源仅接受 SRID 0（按 WGS84 解释）或
  4326；MySQL 8 可把其他已注册 SRID 转为 4326。
- 保存时只替换 YAML 的 `sources` 节点，不会把环境变量提供的 API Key 写回磁盘；
  新后端先打开并探活成功，之后才写配置并热替换运行时 Registry。
- 列表接口只返回脱敏后的 DSN 提示。编辑已有 PostGIS/MySQL 时密码留空会保留原连接。
- “服务目录”把 `/catalog` 的 TileJSON、XYZ、Features 与 PMTiles Archive 地址按图层
  呈现并支持复制；“运行统计”渲染 `/admin/stats` 的请求、缓存、数据源类型和 Go
  运行时指标，不再跳转到原始 JSON。
- 配置文件来自只读容器卷时页面自动进入只读模式；生产环境应开启 API Key，或在
  nginx/网关处限制 `/admin/*`，不要把写接口直接暴露到公网。开启鉴权时仅
  WebUI 的 HTML/CSS/JS 外壳可匿名加载（不含任何配置），数据与写接口仍要求密钥，
  因而可以在页面左下角输入 API Key。

## HTTP API

| 端点 | 说明 |
|---|---|
| `GET /` | 服务元数据（OGC API landing page）|
| `GET /api` | OpenAPI 3.0 服务描述（`Accept: application/json` 或 `?f=json` 取 JSON，默认 YAML）|
| `GET /conformance` | OGC API 一致性声明（每条都有自动化测试背书，见 [docs/CONFORMANCE.md](docs/CONFORMANCE.md)）|
| `GET /health` | 健康检查（逐数据源，每次重新 ping）|
| `GET /readyz` | 就绪探针（结果缓存 5s，供编排器高频探活）|
| `GET /metrics` | Prometheus 文本格式指标 |
| `GET /admin/stats` | 缓存命中率、运行时与请求统计（JSON）|
| `DELETE /admin/cache` | 清空两级切片缓存（幂等）|
| `GET /admin/` | 内置数据源配置 WebUI |
| `GET/POST /admin/sources` | 查看脱敏配置 / 探活后保存并热加载数据源 |
| `POST /admin/sources/probe` | 测试候选数据源连接 |
| `DELETE /admin/sources/{name}` | 移除数据源并写回配置（至少保留一个）|
| `GET /catalog` | 全部图层清单与访问 URL |
| `GET /tiles/{layer}/{z}/{x}/{y}.pbf` | XYZ 切片（栅格源为 .png/.jpg/.webp）|
| `GET /tiles/{layer}.json` | TileJSON 3.0 |
| `GET /archives/{layer}.pmtiles` | PMTiles 原始归档（支持 HEAD、Range/206 与条件请求）|
| `GET /wmts/1.0.0/WMTSCapabilities.xml` | WMTS 能力文档 |
| `GET /wmts/1.0.0/{layer}/default/GoogleMapsCompatible/{z}/{row}/{col}.pbf` | WMTS RESTful GetTile |
| `GET /collections` | OGC API - Features 集合列表 |
| `GET /collections/{id}` | 集合描述 |
| `GET /collections/{id}/items?bbox=&limit=&offset=` | 要素查询（GeoJSON）|
| `GET /collections/{id}/items/{fid}` | 单要素 |
| `GET /tileMatrixSets` | 支持的 TileMatrixSet 列表（目前仅 WebMercatorQuad）|
| `GET /tileMatrixSets/{id}` | TileMatrixSet 定义（0-24 级）|
| `GET /collections/{id}/tiles` | OGC API - Tiles tileset 元数据（边界、zoom 范围、瓦片 URL 模板）|
| `GET /collections/{id}/tiles/{tileMatrixSetId}/{tileMatrix}/{tileRow}/{tileCol}` | OGC API - Tiles 标准取瓦片路由；与上面的 XYZ 路由共用同一份瓦片数据，同一 z/x/y 字节完全一致 |
| `GET /algorithms` | 空间算法清单（自描述 JSON Schema）|
| `POST /algorithms/{name}` | 执行算法（shortest_path / isochrone / map_match / dbscan）|

切片坐标体系为 WebMercatorQuad（EPSG:3857）。空白区域切片返回 `204 No Content`。
标准 OGC API - Tiles 路由是 XYZ 路由之外**新增**的入口，不是替代——现有
`/tiles/{layer}/{z}/{x}/{y}.{ext}` 与 `/wmts/...` 客户端不受影响。
`examples/clients/` 下有 OpenLayers 与 MapLibre 两个消费该路由的示例。

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

开启后只有 `/health`、`/readyz` 和不含数据的 WebUI 静态外壳豁免；
其余数据、管理与写端点全部要求 API Key，三种携带方式任选：

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

PMTiles 可走 Serve 的统一 XYZ/TileJSON，也可让浏览器按 Range 直接读取归档：

```js
import { Protocol } from 'pmtiles';
import maplibregl from 'maplibre-gl';

const protocol = new Protocol();
maplibregl.addProtocol('pmtiles', protocol.tile);
map.addSource('regional', {
  type: 'vector',
  url: 'pmtiles://http://localhost:8080/archives/regional.pmtiles',
});
```

当前读取器支持 PMTiles v3 常用的 `none` / `gzip` 目录与 tile 压缩，以及
MVT、PNG、JPEG、WebP 四种 tile 类型；其他压缩或 tile 类型会在启动/探活时
明确报错，不会静默按错误媒体类型分发。

QGIS：`图层 → 添加图层 → WMTS` 指向 capabilities 地址，或直接添加
Vector Tiles / OGC API - Features 连接。

## 部署

上线前先自检——`-validate` 会真正打开每个数据源并 ping 一遍再退出，
退出码 0 即「这份配置可以上线」：

```bash
geoverse -validate -config config.yaml
```

最快的方式是 Docker Compose（已配好只读根文件系统、非 root、cap drop、
健康检查与缓存卷）：

```bash
cd deploy && cp .env.example .env && docker compose up -d
```

PostGIS 与 MySQL 都是显式可选 profile；最小静态部署不会启动数据库：

```bash
docker compose --profile postgis up -d postgis
docker compose --profile mysql up -d mysql
```

单容器：

```bash
make docker
docker run -p 8080:8080 \
  -v $(pwd)/config.yaml:/etc/geoverse/config.yaml \
  -v $(pwd)/data:/data \
  geoverse-map-server:dev
```

systemd、Kubernetes、nginx 反代、可观测与升级回滚见 **[DEPLOY.md](DEPLOY.md)**；
现成配置在 [`deploy/`](deploy/)。基础镜像 digest 固定、SBOM/许可证/漏洞扫描
与 Docker Hub 发布流程见 DEPLOY.md 七、八节——**Docker Hub 发布链路已搭好但
尚未执行过真实发布**，目前仍需自行 `make docker` 构建本地镜像。

> 两个探针（`/health`、`/readyz`）**豁免 API Key**——负载均衡器无法携带密钥，
> 而它们只报数据源存活、不返回数据。`/metrics` 与 `/admin/*` 则要求鉴权，
> 且不建议公网暴露。

## 开发

```bash
make test   # 单元 + 集成测试（无需外部服务）
make vet
```

代码布局见 [docs/DESIGN.md](docs/DESIGN.md) 第 7 节，贡献流程见
[CONTRIBUTING.md](CONTRIBUTING.md)。CI（gofmt / vet / test -race / build /
docker build）见 `.github/workflows/ci.yml`。

> **Go 版本**：`go.mod` 声明 1.25，因为依赖 `modernc.org/sqlite` 与 `jackc/pgx/v5`
> 自身要求 1.25。改低会让 `go build`/`go vet` 直接失败（不是警告，是拒绝构建），
> 所以 go.mod、Dockerfile 的 builder 镜像、CI 的 `setup-go` 三处版本必须一起动。

本机开发若无系统 Go，仓库内置了一份工具链，见 [GO_ENVIRONMENT_zh.md](GO_ENVIRONMENT_zh.md)。
注意 Windows 上 `git config core.autocrlf=true` 会让工作区文件是 CRLF，
`gofmt -l .` 因此列出所有文件——这是本地假阳性，CI（Linux/LF）看到的是干净的。

## 许可

代码以 [MIT](LICENSE) 许可发布。`examples/data/` 中的 Natural Earth 数据
为公有领域（Made with Natural Earth），自制演示数据随项目 MIT。
