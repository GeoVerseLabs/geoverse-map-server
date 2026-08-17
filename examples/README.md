# 示例

## 数据（examples/data/）

| 文件 | 内容 | 来源与许可 |
|---|---|---|
| `ne_110m_admin_0_countries.geojson` | 世界国家边界（177 面要素）| Natural Earth 1:110m，公有领域，属性已精简 |
| `ne_110m_populated_places_simple.geojson` | 世界主要城市（243 点要素）| Natural Earth 1:110m，公有领域，属性已精简 |
| `cities.geojson` | 中国 10 个主要城市 | 项目自制演示数据（MIT）|
| `campus_paths.geojson` | 校园步行路网与室内连接体 | 项目自制演示数据（MIT）|

完整来源说明与更多开放数据获取渠道见 [docs/OPEN_DATA.md](../docs/OPEN_DATA.md)。

## 运行

```bash
make build
./bin/geoverse -config config.example.yaml
```

浏览器打开 `examples/viewer.html`（或任何静态服务器托管它），即可看到
国家面 + 世界城市点 + 中国城市三层矢量切片叠加渲染。

也可以在 QGIS 中验证：

- **Vector Tiles**：URL 填 `http://localhost:8080/tiles/countries/{z}/{x}/{y}.pbf`
- **WMTS**：连接 `http://localhost:8080/wmts/1.0.0/WMTSCapabilities.xml`
- **OGC API - Features**：连接 `http://localhost:8080/`

## OGC API - Tiles 客户端示例（`examples/clients/`）

`examples/viewer.html` 走的是 XYZ TileJSON（`/tiles/{name}.json`）；
`examples/clients/` 下两个示例改走标准 OGC API - Tiles 资源
（`GET /collections/{id}/tiles` 与 `GET /collections/{id}/tiles/{tileMatrixSetId}/{tileMatrix}/{tileRow}/{tileCol}`），
两条路由背后是同一份瓦片数据，字节级一致（见
`internal/server/tiles_ogc_test.go` 的 `TestStandardTileMatchesXYZBytes`）：

- `openlayers-ogc-tiles.html`：`ol/source/OGCVectorTile` 原生支持 OGC API -
  Tiles，直接把 tileset URL 交给它，不需要手写瓦片 URL 模板。
- `maplibre-ogc-tiles.html`：MapLibre 没有原生 OGC API - Tiles 客户端，示例
  先 `fetch` tileset 资源拿到 `rel=item` 的瓦片模板链接，把 OGC 的
  `{tileMatrix}/{tileRow}/{tileCol}` 换成 MapLibre style spec 认的
  `{z}/{y}/{x}` 再喂给 vector source。

两者都假设服务跑在 `http://localhost:8080`（`config.example.yaml` 默认端口），
直接用浏览器打开 HTML 文件即可，未做浏览器端渲染验收，仅保证背后的 JSON
契约（tileset 结构、瓦片模板、字节一致性）有测试覆盖。

## Docker 多数据源验收

[`config.multisource.yaml`](config.multisource.yaml) 同时发布：

- 已有 `gv-postgis` 的 `geo.feature`（PostGIS 动态 MVT + Features）；
- `deploy` compose 的 `mysql` profile（MySQL 8 空间点表）；
- 仓库内 `cities`、`countries`、`campus_paths` 三个 GeoJSON 静态资源。

```bash
cd deploy
cp .env.example .env
# 在 .env 填写 MYSQL_PASSWORD=geoverse_demo 和一个本机 MYSQL_ROOT_PASSWORD
docker compose --profile mysql up -d mysql
cd ..
./bin/geoverse -validate -config examples/config.multisource.yaml
./bin/geoverse -config examples/config.multisource.yaml
```

控制台 `http://127.0.0.1:18080/admin/` 的服务目录应显示 5 个图层，运行统计应按
`postgis=1 / mysql=1 / geojson=3` 汇总数据源类型。
