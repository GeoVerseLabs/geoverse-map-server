# 部署指南

GeoVerse Map Server 是**单个静态二进制**（纯 Go、`CGO_ENABLED=0`），无外部运行时依赖：
不需要 JVM、不需要 Python、不需要系统 GDAL/GEOS。部署的全部工作就是「放一个二进制 +
一份 YAML + 一份数据」。

三种形态任选：[Docker Compose](#一docker-compose推荐) · [systemd](#二systemd裸机--vm) ·
[Kubernetes](#三kubernetes)。前面加反向代理见[第四节](#四反向代理-nginx)。

---

## 0. 部署前自检

```bash
geoverse -doctor -config /etc/geoverse/config.yaml
geoverse -validate -config /etc/geoverse/config.yaml
```

`-doctor` 输出资产路径、大小、数据源能力和兼容模式警告；生产部署应确保没有
`assets.root_not_enforced` 或无限大小警告。可用 `-format json` 接入 CI。
`-validate` 不止解析 YAML——它会**真正打开每一个数据源并 ping 一遍**，然后退出。
退出码 0 即「这份配置可以上线」。少了这一步，配置写错的代价是等到容器起来、
探针失败、再去翻日志。CI 与 systemd 的 `ExecStartPre` 都应该跑它。

---

## 一、Docker Compose（推荐）

```bash
cd deploy
cp .env.example .env && $EDITOR .env          # 至少填 GEOVERSE_API_KEYS
cp ../config.example.yaml config.yaml && $EDITOR config.yaml
mkdir -p data && cp /path/to/*.mbtiles data/

docker compose up -d
docker compose ps                              # STATUS 应为 healthy
curl -s localhost:8080/readyz | jq
```

`deploy/docker-compose.yml` 已经做了这些事，不必自己再加：

| 项 | 取值 | 为什么 |
|---|---|---|
| `read_only: true` | 根文件系统只读 | 静态二进制只读配置与数据，唯一要写的是切片缓存卷 |
| `cap_drop: ALL` + `no-new-privileges` | 无 capability | 绑 8080 不需要任何特权 |
| 端口绑 `127.0.0.1` | 默认不对外 | 期望前面有 TLS 反代；要直接暴露得显式改 `BIND_ADDR` |
| `config.yaml`/`data` 挂 `:ro` | 只读挂载 | 服务从不写它们 |
| `tile-cache` 命名卷 | 唯一可写路径 | `down` 后不留 root 属主的文件在仓库里 |
| `GEOVERSE_API_KEYS` 走环境变量 | 密钥不进镜像 | 也不进 `config.yaml`，便于轮换 |

**密钥生成**：`openssl rand -hex 32`。多个密钥用逗号分隔，可用于灰度轮换
（先加新的、客户端切完再删旧的）。

**数据库 profile**（动态服务验证，默认关闭）：

```bash
docker compose --profile postgis up -d postgis
docker compose --profile mysql up -d mysql
```

`mysql` profile 使用宿主 `127.0.0.1:3308`，并通过 `mysql-init/` 创建 SRID 4326
空间点表；凭据来自 `.env`。它用于开发与验收，不会进入最小静态部署。若本机已有
GeoVerse Live 的 `gv-postgis`，可直接使用 `examples/config.multisource.yaml`
连接该实例，无需再启动本 compose 的 PostGIS。

### 构建镜像

```bash
make docker                       # 或 docker build -t geoverse-map-server:dev .
```

> **构建上下文**：仓库内 `.tools/go`（内置 Go 工具链 252 MB）与 `.cache/`
> （模块与构建缓存 896 MB）都由 `.dockerignore` 排除。没有它，每次
> `docker build` 都要先上传 1.2 GB 上下文。改 `.dockerignore` 时留意
> `examples/data/` 是**刻意保留**的——`config.example.yaml` 引用它，
> 排除掉会让「拉起来就能看」的默认体验失效。

---

## 二、systemd（裸机 / VM）

```bash
make build
sudo install -m 0755 bin/geoverse /usr/local/bin/geoverse
sudo useradd --system --no-create-home --shell /usr/sbin/nologin geoverse
sudo mkdir -p /etc/geoverse /var/lib/geoverse
sudo cp config.example.yaml /etc/geoverse/config.yaml
sudo chown -R geoverse:geoverse /var/lib/geoverse

# 密钥单独放，0600 root 属主
printf 'GEOVERSE_API_KEYS=%s\n' "$(openssl rand -hex 32)" \
  | sudo tee /etc/geoverse/geoverse.env >/dev/null
sudo chmod 600 /etc/geoverse/geoverse.env

sudo cp deploy/systemd/geoverse-map-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now geoverse-map-server
systemctl status geoverse-map-server
```

单元文件里两个点值得注意：

- **密钥用 `EnvironmentFile=` 而非 `Environment=`**。后者会出现在
  `systemctl show` 的输出里，任何本地用户都读得到。
- **`ProtectSystem=strict` + `ReadWritePaths=/var/lib/geoverse`**：整个文件系统
  对进程只读，只有磁盘缓存目录可写。用了磁盘缓存就要把
  `cache.disk.dir` 指到 `/var/lib/geoverse`，否则服务起不来。

停止走 `SIGTERM`，进程会 drain 在途请求（最长 10s）后退出。

---

## 三、Kubernetes

镜像里 `USER` 用的是**数字 UID 10001**，因此可以直接配 `runAsNonRoot: true`
（K8s 无法把用户名解析成 UID，只写名字会被准入拒绝）。

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: geoverse-map-server
spec:
  replicas: 2
  selector:
    matchLabels: { app: geoverse-map-server }
  template:
    metadata:
      labels: { app: geoverse-map-server }
    spec:
      securityContext:
        runAsNonRoot: true
        runAsUser: 10001
        fsGroup: 10001
      containers:
        - name: map-server
          image: ghcr.io/geoverselabs/geoverse-map-server:v0.1.0
          ports: [{ containerPort: 8080 }]
          env:
            - name: GEOVERSE_API_KEYS
              valueFrom:
                secretKeyRef: { name: geoverse-api-keys, key: keys }
          # 就绪探针用 /readyz（会看数据源），存活探针**不要**用它：
          # PostGIS 抖一下不该让 kubelet 杀掉本来健康的进程。存活只需确认
          # 进程还在响应 HTTP。
          readinessProbe:
            httpGet: { path: /readyz, port: 8080 }
            periodSeconds: 10
          livenessProbe:
            httpGet: { path: /health, port: 8080 }
            periodSeconds: 30
            failureThreshold: 5
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities: { drop: ["ALL"] }
          resources:
            requests: { cpu: 100m, memory: 64Mi }
            limits:   { memory: 512Mi }
          volumeMounts:
            - { name: config, mountPath: /etc/geoverse, readOnly: true }
            - { name: data,   mountPath: /data, readOnly: true }
            - { name: cache,  mountPath: /var/cache/geoverse }
            - { name: tmp,    mountPath: /tmp }
      volumes:
        - { name: config, configMap: { name: geoverse-config } }
        - { name: data, persistentVolumeClaim: { claimName: geoverse-data } }
        - { name: cache, emptyDir: {} }
        - { name: tmp, emptyDir: {} }
```

> **多副本与缓存**：磁盘缓存是 pod 本地的 `emptyDir`，副本之间不共享。这没问题
> ——切片是派生数据，各自预热即可。但也意味着 `DELETE /admin/cache`
> **只清它打到的那一个副本**。要全清就滚动重启，或逐副本调。

---

## 四、反向代理（nginx）

`deploy/nginx/geoverse-map-server.conf` 是一份可直接改域名使用的配置，要点：

- **必须转发 `X-Forwarded-Proto` / `X-Forwarded-Host`**。服务用它们拼 TileJSON
  与 OGC `links` 里的绝对地址；不转发的话，发给客户端的每个 URL 都会写成
  `http://127.0.0.1:8080`。（也可以在 config 里写死 `server.base_url`。）
- **`/metrics` 与 `/admin/*` 限制来源网段**。即使开了 API Key 也别公网暴露。
- **`/health`、`/readyz` 不限流不缓存**。被限流器饿死的健康检查会把一次流量高峰
  变成一次假故障。
- **204/404 也要缓存**。数据集边缘的空切片非常多，不缓存的话每个海洋切片都回源。

---

## 五、可观测

| 端点 | 用途 | 鉴权 |
|---|---|---|
| `GET /health` | 深检查，每次都重新 ping 全部数据源 | **豁免** |
| `GET /readyz` | 就绪探针，结果缓存 5s | **豁免** |
| `GET /metrics` | Prometheus 文本格式 | 需要 API Key |
| `GET /admin/stats` | 缓存命中率、运行时、请求统计（JSON） | 需要 API Key |
| `DELETE /admin/cache` | 清空两级切片缓存 | 需要 API Key |

两个探针豁免鉴权是刻意的：负载均衡器没法携带密钥，而它们只报数据源存活、不返回数据。

`/readyz` 缓存 5s 的原因：编排器按秒级探活，不缓存的话滚动重启会把就绪探测
变成对 PostGIS 的一波压力——而探针本该保护它。

Prometheus 抓取示例：

```yaml
scrape_configs:
  - job_name: geoverse-map-server
    metrics_path: /metrics
    authorization: { credentials: "<api-key>" }
    static_configs: [{ targets: ["map-server:8080"] }]
```

关键指标：`geoverse_cache_hits_total{tier=}` / `geoverse_cache_misses_total{tier=}`
（命中率掉了通常是数据换版或缓存被清）、`geoverse_source_up{source=}`、
`geoverse_http_request_duration_seconds_bucket`。

**清缓存**是这个服务里唯一的写操作，幂等且安全——切片是派生数据，最坏结果只是
下一批请求重新渲染：

```bash
curl -X DELETE -H "X-API-Key: $KEY" http://localhost:8080/admin/cache
```

数据换版后应当清一次，否则旧切片会一直服务到 TTL 到期。

---

## 六、升级与回滚

切片是派生数据、配置是声明式的，所以升级就是换二进制：

```bash
# Docker
docker compose pull && docker compose up -d      # 滚动替换
# systemd
sudo install -m 0755 bin/geoverse /usr/local/bin/geoverse
sudo systemctl restart geoverse-map-server        # ExecStartPre 会先 validate
```

回滚同理，换回上一个镜像 tag / 二进制即可。**没有数据库迁移，没有状态需要迁移**。

**数据原子换版**：不要就地覆盖 `.mbtiles`（读到一半的文件会给出损坏切片）。
用 symlink 切换：

```bash
cp new.mbtiles /data/basemap-2026-07-30.mbtiles
ln -sfn /data/basemap-2026-07-30.mbtiles /data/basemap.mbtiles
systemctl restart geoverse-map-server     # 重开文件句柄
curl -X DELETE -H "X-API-Key: $KEY" localhost:8080/admin/cache   # 清旧切片
```

---

## 七、供应链：镜像 digest / SBOM / 许可证 / 漏洞扫描

### 基础镜像 digest

`Dockerfile` 两处 `FROM` 都固定到具体 digest（`golang:1.25-alpine@sha256:...`、
`alpine:3.20@sha256:...`），不是浮动 tag——同一份 Dockerfile 今天和一年后构建
出的基础层字节完全一致，不会因为上游重新推送同一 tag 而悄悄变化。

**代价**：固定 digest 之后，基础镜像的安全补丁不会自动到达，必须有人定期
复核并手动升级。约定复核周期：**至少每季度一次**，或收到 CVE 通知时；升级
步骤：

```bash
# 取当前 tag 对应的最新 digest（示例：golang:1.25-alpine）
docker pull golang:1.25-alpine
docker inspect --format='{{index .RepoDigests 0}}' golang:1.25-alpine
# 把 Dockerfile 里对应 FROM 行的 sha256 换成新值，注释同步改复核日期
```

alpine 基础镜像同理。两处改动应在同一次提交里完成，避免 build/runtime 两阶段
用不同批次的补丁。

### SBOM / 许可证 / 漏洞扫描（CI，观测模式）

`.github/workflows/ci.yml` 新增三个作业，**均不阻断构建**——这是这些工具第一次
跑在本仓库上，先建立基线再决定失败阈值，避免把工具引入当天的存量问题直接变成
发布停摆：

| 作业 | 产出 | 工具 |
|---|---|---|
| `supply-chain` → SBOM | `sbom.spdx.json`（SPDX JSON）| [syft](https://github.com/anchore/syft) |
| `supply-chain` → 漏洞扫描 | `trivy-report.json`（CRITICAL/HIGH/MEDIUM）| [trivy](https://github.com/aquasecurity/trivy) |
| `licenses` | `licenses.csv`（Go 依赖许可证清单）| [go-licenses](https://github.com/google/go-licenses) |

三份产物都作为 CI artifact 上传（保留 90 天），在 Actions 运行页面的
"Artifacts" 区下载。首批报告出来后，用户应过一遍：

- 漏洞报告里 CRITICAL/HIGH 是否都有可用修复版本（有则升级依赖，无则记录接受
  的风险）；
- 许可证清单里有没有与本项目分发方式冲突的许可证（例如强 copyleft）；
- 确认基线之后，再决定是否给 CI 加真正的失败阈值（例如"仅 CRITICAL 且有可用
  修复版本时阻断"），以及许可证黑名单。这一步是新的用户决策点，本轮实施
  只到"能看见问题"为止，不代为设定阻断标准。

## 八、Docker Hub 发布

镜像发布链路（构建 → 打标签 → 推送）已在 CI 里搭好，但**本仓库尚未执行过一次
真实的 `docker push`**——这一步需要仓库所有者在 GitHub 配置好 Docker Hub 凭据
后才会真正跑：

1. 在 Docker Hub 创建仓库 `geoverselabs/geoverse-map-server`（或替换成实际
   使用的命名空间——这里假设复用本仓库的 GitHub 组织名 `GeoVerseLabs`，
   Docker Hub 命名空间必须全小写）。
2. GitHub 仓库 Settings → Secrets and variables → Actions，新增两个 secret：
   `DOCKERHUB_USERNAME`、`DOCKERHUB_TOKEN`（Docker Hub 账号设置里生成
   Access Token，不要用账号密码）。
3. `.github/workflows/docker-publish.yml`（见仓库根目录）在推送 `v*` 格式的
   git tag（如 `v1.2.3`）时触发，用 `docker/metadata-action` 派生三个标签并
   推送：`1.2.3`、`1.2`（不含 patch，滚动指向该 minor 最新 patch）、`latest`
   ——**Docker Hub 标签本身不带 `v` 前缀**，这是 metadata-action 对 semver
   类型的默认行为，git tag 侧的 `v` 前缀只是本仓库沿用的常见 tag 命名习惯。
   未配置上述两个 secret 时，工作流会在登录步骤失败，不会推送残缺镜像。
4. 首次发布前建议本地手动跑一遍（需要本机登录 Docker Hub）：

   ```bash
   docker build --build-arg VERSION=1.2.3 -t geoverselabs/geoverse-map-server:1.2.3 .
   docker run --rm geoverselabs/geoverse-map-server:1.2.3 -version
   docker push geoverselabs/geoverse-map-server:1.2.3   # 确认无误后再打 git tag v1.2.3 触发 CI
   ```

镜像标签与 `main.version`（`-ldflags -X main.version=...`，由 Dockerfile 的
`ARG VERSION` 注入）保持一致，`geoverse -version` 输出即可核对线上镜像对应
的源码版本。

---

## 九、生产清单

- [ ] `geoverse -doctor` 无资产边界或大小上限警告
- [ ] `geoverse -validate` 退出码 0
- [ ] 开启 `auth.enabled` 且密钥来自环境变量，不写在 `config.yaml` 里
- [ ] 前置 TLS；`/metrics` 与 `/admin/*` 不对公网开放
- [ ] 反代转发 `X-Forwarded-Proto`/`X-Forwarded-Host`，或配 `server.base_url`
- [ ] 就绪探针指 `/readyz`、存活探针指 `/health`（别互换）
- [ ] 数据卷只读挂载；磁盘缓存有独立可写卷且设了 `max_size_mb`
- [ ] 抓 `/metrics`，至少对 `geoverse_source_up` 告警
- [ ] 日志轮转（compose 已配 `max-size`/`max-file`）
- [ ] 基础镜像 digest 复核未超过一个季度（见七节）
- [ ] 最近一次 SBOM/许可证/漏洞扫描 artifact 已过一遍（见七节）
