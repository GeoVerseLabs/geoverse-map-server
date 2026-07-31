# servebench

`servebench` 是 GeoVerse Serve 的零第三方依赖 HTTP 压测器。它复用连接并输出
单行 JSON，记录 RPS、成功率、状态码、传输错误、字节量和
min/mean/p50/p95/p99/max 延迟。

构建：

```powershell
& .\.tools\go\bin\go.exe build -o .\.cache\servebench.exe .\bench\servebench
```

热点切片：

```powershell
.\.cache\servebench.exe `
  -scenario hot-tile-c32 `
  -url /tiles/countries/2/2/1.pbf `
  -concurrency 32 `
  -duration 10s
```

冷切片使用 `-tile-grid layer:z:count` 生成确定性的、不重复的切片坐标。运行前应
先调用 `DELETE /admin/cache`：

```powershell
Invoke-RestMethod -Method Delete http://127.0.0.1:8080/admin/cache
.\.cache\servebench.exe `
  -scenario cold-countries-z6-c16 `
  -tile-grid countries:6:512 `
  -concurrency 16 `
  -requests 512 `
  -warmup 0
```

注意：本机回环测试用于比较端点和并发阶梯，不包含公网 RTT、TLS、反向代理、
容器限额或 PostGIS 网络成本；生产容量结论必须在目标部署拓扑中复测。
