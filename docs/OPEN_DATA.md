# 开放地理数据获取渠道

本项目 `examples/data/` 内置了小体量的示例数据（见文末清单）。生产使用时
请从下列渠道获取完整数据。各渠道许可协议不同，**商用前请务必阅读原始许可**。

## 1. 全球基础底图数据

| 渠道 | 内容 | 格式 | 许可 |
|---|---|---|---|
| [Natural Earth](https://www.naturalearthdata.com/) | 1:10m/50m/110m 全球行政区、水系、地形等制图数据 | Shapefile / GeoJSON / GeoPackage | 公有领域（PD）|
| [OpenStreetMap](https://www.openstreetmap.org/) | 全球众包矢量地图数据 | PBF / XML | ODbL（需署名 + 衍生开放）|
| [Geofabrik](https://download.geofabrik.de/) | OSM 按大洲/国家/地区切分的每日快照 | .osm.pbf / shp | ODbL |
| [BBBike Extract](https://extract.bbbike.org/) | OSM 自定义范围裁剪下载 | pbf / shp / garmin 等 | ODbL |
| [Overpass API](https://overpass-turbo.eu/) | OSM 按标签/范围在线查询，导出 GeoJSON | GeoJSON / XML | ODbL |
| [OpenMapTiles](https://openmaptiles.org/) | OSM 预切矢量瓦片方案与工具链 | MBTiles (MVT) | 数据 ODbL，方案 BSD/CC-BY |
| [Protomaps](https://protomaps.com/) | 全球单文件矢量底图，可按 bbox 裁剪下载 | PMTiles（可转 MBTiles）| ODbL |
| [MapTiler Data](https://data.maptiler.com/downloads/) | OpenMapTiles 预制区域包 | MBTiles | 免费层需署名 |

OSM → 本服务的常见路线：`Geofabrik pbf → tilemaker/planetiler → MBTiles →
mbtiles 数据源`；或 `osm2pgsql → PostGIS → postgis 数据源` 做动态切片。

## 2. 行政区划与边界

| 渠道 | 内容 | 许可 |
|---|---|---|
| [GADM](https://gadm.org/) | 全球各级行政区划边界（GeoPackage/Shapefile）| 学术免费，**商用需授权** |
| [geoBoundaries](https://www.geoboundaries.org/) | 全球行政边界开放数据库 | CC BY 4.0 |
| [Who's On First](https://whosonfirst.org/) | 全球地名/行政层级要素库 | CC0/CC BY 等（逐记录）|
| [阿里云 DataV.GeoAtlas](https://datav.aliyun.com/portal/school/atlas/area_selector) | 中国省市县 GeoJSON 边界在线获取 | 见页面条款（仅供学习研究）|

## 3. 中国相关渠道

| 渠道 | 内容 | 说明 |
|---|---|---|
| [全国地理信息资源目录服务系统](https://www.webmap.cn/) | 1:100 万全国基础地理数据库（公开版）| 免费注册下载，含水系/居民地/交通/边界等图层 |
| [天地图](https://www.tianditu.gov.cn/) | 国家地理信息公共服务平台，WMTS/API | 免费 key，注意调用配额与服务条款 |
| [国家地球系统科学数据中心](https://www.geodata.cn/) | 多学科地学数据集 | 注册申请下载 |
| [资源环境科学与数据中心 (RESDC)](https://www.resdc.cn/) | 土地利用、人口格网等专题数据 | 部分免费 |
| OSM 中国区提取 | Geofabrik `asia/china` | ODbL |

> 注意：在中国境内发布地图需遵守测绘法规（公开地图审图、边界表示等）。

## 4. 专题与栅格数据

| 渠道 | 内容 | 许可 |
|---|---|---|
| [HDX (Humanitarian Data Exchange)](https://data.humdata.org/) | 人道主义专题数据（人口、设施、边界）| 多为 CC BY |
| [WorldPop](https://www.worldpop.org/) | 全球人口格网 | CC BY 4.0 |
| [GHSL](https://human-settlement.emergency.copernicus.eu/) | 全球人居层（建成区、人口）| 免费开放 |
| [USGS EarthExplorer](https://earthexplorer.usgs.gov/) | Landsat、SRTM 高程等 | 公有领域为主 |
| [Copernicus Data Space](https://dataspace.copernicus.eu/) | Sentinel 系列影像 | 免费开放 |
| [AWS Open Data](https://registry.opendata.aws/) / [Google Earth Engine](https://earthengine.google.com/) | 云上开放地理数据集镜像 | 逐数据集 |

## 5. 政府开放数据门户（含地理图层）

- [Data.gov](https://data.gov/)（美国）、[data.europa.eu](https://data.europa.eu/)（欧盟）、
  [data.gov.uk](https://www.data.gov.uk/)（英国）
- 各地开放数据平台（如 [上海市公共数据开放平台](https://data.sh.gov.cn/)、
  [北京市公共数据开放平台](https://data.beijing.gov.cn/)）通常提供 GeoJSON/Shapefile 下载

## 6. 示例/测试用小数据

| 渠道 | 内容 |
|---|---|
| [Natural Earth GeoJSON 镜像](https://github.com/nvkelso/natural-earth-vector/tree/master/geojson) | 免转换直接可用的 GeoJSON |
| [NGA GeoPackage 样例](https://github.com/ngageoint/GeoPackage/tree/master/docs/examples) | 标准 GeoPackage 测试文件 |
| [geojson.xyz](https://geojson.xyz/) | Natural Earth 数据的 CDN 直链 |
| [OpenMapTiles 测试瓦片集](https://github.com/openmaptiles) | 小型 MBTiles 样例 |

## 内置示例数据清单

`examples/data/` 内的文件均可直接被 `config.example.yaml` 加载：

| 文件 | 来源 | 许可 | 处理 |
|---|---|---|---|
| `ne_110m_admin_0_countries.geojson` | Natural Earth 1:110m Admin 0 – Countries（经 [nvkelso/natural-earth-vector](https://github.com/nvkelso/natural-earth-vector) GeoJSON 镜像获取）| 公有领域 | 精简属性至 name / name_zh / iso_a3 / continent / pop_est / economy |
| `ne_110m_populated_places_simple.geojson` | Natural Earth 1:110m Populated Places (simple)，同上渠道 | 公有领域 | 精简属性至 name / adm0name / pop_max / featurecla |
| `cities.geojson` | 项目自制演示数据（中国 10 个主要城市坐标与公开人口数字）| 随项目 MIT | — |

Natural Earth 为公有领域数据，无署名义务；但按社区惯例建议注明
“Made with Natural Earth”。
