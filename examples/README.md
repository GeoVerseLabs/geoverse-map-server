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
