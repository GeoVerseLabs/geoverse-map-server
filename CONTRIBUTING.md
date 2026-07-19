# 贡献指南 / Contributing

感谢参与 GeoVerse Map Server！

## 开发环境

- Go 1.24+（纯 Go 构建，无需 CGO 与任何 C 库）
- 可选：Docker（构建镜像）、一个 PostGIS 实例（测试 postgis 数据源）

```bash
git clone https://github.com/GeoVerseLabs/geoverse-map-server.git
cd geoverse-map-server
make build          # 产出 bin/geoverse
make test           # 全部测试（无需外部服务）
make vet
./bin/geoverse -config config.example.yaml
```

## 代码结构

见 [docs/DESIGN.md](docs/DESIGN.md)。速览：

- `internal/source/` —— 数据源抽象与各后端实现；新增数据源从这里开始：
  实现 `source.Source`（+ `TileSource` / `FeatureSource` 按需），
  在 `internal/source/registry/registry.go` 与 `internal/config` 注册
- `internal/server/` —— HTTP 路由与 handler
- `internal/mcpserver/` —— MCP 端点

## 提交规范

1. 从 `main` 拉分支开发；提交信息用祈使句概括改动（英文或中文均可）
2. **必须**通过 `make test` 与 `make vet`，新逻辑请附测试
   （数据源实现请参考 mbtiles/geopackage 用临时文件构造 fixture 的做法）
3. 保持"轻量"底线：不引入 CGO 依赖；新增第三方库需要在 PR 里说明理由
4. 行为变化请同步更新 README / DESIGN.md / config.example.yaml

## 报告问题

Issue 请附：版本（`geoverse -version`）、配置文件（脱敏 DSN/密钥）、
复现步骤与日志。安全问题请勿公开 Issue，直接私信维护者。
