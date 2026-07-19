# 示例

## 数据（examples/data/）

| 文件 | 内容 | 来源与许可 |
|---|---|---|
| `ne_110m_admin_0_countries.geojson` | 世界国家边界（177 面要素）| Natural Earth 1:110m，公有领域，属性已精简 |
| `ne_110m_populated_places_simple.geojson` | 世界主要城市（243 点要素）| Natural Earth 1:110m，公有领域，属性已精简 |
| `cities.geojson` | 中国 10 个主要城市 | 项目自制演示数据（MIT）|

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
