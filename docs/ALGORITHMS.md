# 空间算法插件框架

## 1. 框架设计

算法是运行在既有数据源之上的**可插拔能力**（`internal/algo`）：

```go
type Algorithm interface {
    Describe() Descriptor   // 名称 + 说明 + 参数 JSON Schema
    Run(ctx, env, params json.RawMessage) (interface{}, error)
}

type Env struct {
    Features func(name string) (source.FeatureSource, bool) // 要素集合
    Networks *network.Manager                               // 可路由网络图
}
```

- **自描述**：`Descriptor.InputSchema` 就是 JSON Schema，`GET /algorithms`
  的清单与 MCP 工具定义共用同一份文档，新增算法零成本接入两个入口
- **注册即暴露**：算法注册到 `Registry` 后自动获得
  `POST /algorithms/{name}` 端点与 MCP `algo_{name}` 工具
- **错误分层**：`algo.UserError`（参数/数据问题）→ HTTP 400 / MCP 工具错误；
  其他错误 → 500，不泄露内部细节

```
POST /algorithms/{name}        执行（JSON 参数 → GeoJSON/JSON 结果）
GET  /algorithms               全部算法描述 + 已配置网络
GET  /algorithms/{name}        单个算法描述
MCP  algo_{name}               同一能力暴露给 LLM 智能体
```

### 1.1 可路由网络（internal/algo/network）

路由类算法共享同一个图模型，用 `networks` 配置从任意要素源
（GeoJSON/GeoPackage/PostGIS）的 LineString 构建，首次使用时懒构建并缓存：

- 折线炸开为**顶点到顶点的直线段边**，坐标量化 (~0.1m) 自动合并共享节点，
  使等时圈的边内插值与路径匹配的投影在段上是精确的
- **多层（室内）支持**：要素带 `level` 属性即位于该层，节点键 =
  (坐标, 层)；带 `level_from`/`level_to` 的要素是**跨层连接体**
  （楼梯/电梯/出入口），可用 `duration_s` 指定固定通行耗时——
  室内外共用同一套图与同一套搜索代码
- 成本模型：时间（长度/速度，`speed_field` 每边覆盖）或距离；
  `oneway_field` 支持单行
- 空间索引：节点网格索引（最近点吸附）与线段网格索引（候选边查询）

```yaml
networks:
  - name: campus
    source: campus_paths      # 任意含 LineString 的要素源
    default_speed_kmh: 5
    speed_field: speed_kmh    # 可选
    oneway_field: oneway      # 可选
```

## 2. 已实现算法与采用的改进

### 2.1 shortest_path — 最短路径（室内/室外）

| | |
|---|---|
| 基线 | Dijkstra |
| **采用的改进** | **A\***，启发函数为大圆距离（时间成本时除以全图最大速度），可采纳（admissible）故保证最优，路网上通常少展开一个数量级的节点 |
| 室内 | 同一张图：跨层走连接体边，`from_level`/`to_level` 指定楼层，结果带 `levels_visited` |

```bash
curl -X POST localhost:8080/algorithms/shortest_path -d '{
  "network": "campus", "from": [116.300, 39.990],
  "to": [116.3055, 39.9925], "to_level": 2, "cost": "time"
}'
```

### 2.2 isochrone — 等时圈

| | |
|---|---|
| 基线 | 截断 Dijkstra + 可达节点凸包 |
| **采用的改进** | 凸包在稀疏路网上严重高估。改为：① 在预算边界的**边上精确内插**截止点；② 把所有已走过的线段**栅格化**到网格；③ **marching squares** 提取等值线并嵌套洞（Valhalla 等生产级引擎采用的 gridded contours 思路），不可达飞地保留为洞 |
| 多档 | 一次 Dijkstra 覆盖最大预算，多档一起出（`cutoffs: [300,600,900]`），大圈在前便于叠加渲染 |

```bash
curl -X POST localhost:8080/algorithms/isochrone -d '{
  "network": "campus", "origin": [116.302, 39.992], "cutoffs": [120, 300]
}'
```

### 2.3 map_match — 路径匹配

| | |
|---|---|
| 基线 | 每点独立吸附最近边（噪声下会在平行道路间跳变） |
| **采用的改进** | **Newson & Krumm (2009) 的 HMM 建模 + Viterbi 解码**（OSRM/Valhalla 匹配器同源）：候选边为隐状态；发射概率 = 高斯（吸附距离，σ 默认 15m）；转移概率 = 指数（\|大圆距离 − 路网距离\|，β 默认 30m）；全局最优序列一次解出 |
| 工程细节 | 每个前驱候选一次**多源种子 Dijkstra**（边中间起搜）覆盖所有后继候选；无候选点跳过、断链降级续接；输出匹配后路线几何 + 逐点吸附明细 |

```bash
curl -X POST localhost:8080/algorithms/map_match -d '{
  "network": "campus",
  "trace": [[116.3001,39.9901],[116.3010,39.9899],[116.3021,39.9901]]
}'
```

### 2.4 dbscan — 密度聚类

| | |
|---|---|
| 基线 | 经典 DBSCAN 两两算距，O(n²) |
| **采用的改进** | **eps 尺寸网格索引**：邻域查询只扫 3×3 相邻格，期望近线性；数据质心处**局部等距投影**使 `eps_m` 是真实米制而不受纬度畸变影响 |
| 输出 | 逐点 `cluster` 标签（噪声 = -1）+ 每簇摘要（数量/质心/bbox），`include_points:false` 可只要摘要 |

```bash
curl -X POST localhost:8080/algorithms/dbscan -d '{
  "collection": "places", "eps_m": 200000, "min_points": 3
}'
```

## 3. 扩展规划（未实现，按优先级）

| 算法 | 思路 / 拟采用的改进 | 依赖 |
|---|---|---|
| 路网预处理加速 | Contraction Hierarchies 或 ALT（A* + 地标 + 三角不等式），支撑大路网毫秒级查询 | 现有图模型 |
| OD 矩阵 / 多对多 | 一对多 Dijkstra 批处理 + CH 加速 | shortest_path |
| 服务区叠加分析 | 多设施等时圈并集/差集（网格布尔运算即可，复用栅格层） | isochrone |
| TSP / 路径优化 | 小规模精确（Held-Karp ≤ 15 点）+ 大规模启发式（2-opt / Or-opt） | OD 矩阵 |
| HDBSCAN | 变密度聚类，免调 eps；或 DBSCAN++ 采样加速超大点集 | dbscan 基建 |
| ST-DBSCAN | 时空聚类（轨迹停留点识别），eps 增加时间维 | dbscan 基建 |
| KDE 热力图 | 核密度栅格输出（复用 binaryGrid → 浮点网格 + 等值线） | marching squares |
| Voronoi / 最近设施 | Fortune 扫描线或基于路网的 network Voronoi | 图模型 |
| 高程剖面 | 路线叠加 DEM 采样（需栅格源支持，见 OPEN_DATA 的 SRTM/Copernicus） | 栅格读取 |
| 轨迹简化/停留点 | Douglas-Peucker（已有）+ 速度阈值停留检测，作为 map_match 前处理 | — |

新增算法只需：实现 `algo.Algorithm` → 在 `server.New` 注册一行 →
HTTP 与 MCP 自动获得端点；参见 `internal/algo/cluster/dbscan.go`
这个最小样例。

## 4. 参考文献

- Newson, P. & Krumm, J. (2009). *Hidden Markov Map Matching Through
  Noise and Sparseness*. ACM SIGSPATIAL GIS.
- Ester, M. et al. (1996). *A Density-Based Algorithm for Discovering
  Clusters in Large Spatial Databases with Noise* (DBSCAN). KDD.
- Hart, P., Nilsson, N. & Raphael, B. (1968). *A Formal Basis for the
  Heuristic Determination of Minimum Cost Paths* (A*). IEEE TSSC.
- Valhalla isochrone 文档（gridded contours 思路）；
  Geisberger, R. et al. (2008). *Contraction Hierarchies*（规划中）.
