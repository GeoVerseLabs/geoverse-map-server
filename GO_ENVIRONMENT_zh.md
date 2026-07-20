# 本项目 Go 环境说明

> 记录日期：2026-07-19
> 适用目录：`D:\workspace\claude\geoverse-map-server`

## 1. 结论

本次没有在 Windows 系统中安装 Go，也没有修改系统或用户级 `PATH`、注册表、`GOROOT`、`GOPATH`。
采用的是**工程内便携 Go 工具链**：

```text
geoverse-map-server/
├── .tools/go/              # Go 1.26.5 工具链
├── .cache/go-build/        # 本项目构建缓存
├── .cache/gopath/          # 本项目 GOPATH 与模块缓存
├── bin/                    # 构建出的服务二进制
├── go.mod                  # Go 模块与语言版本声明
└── .gitignore              # 忽略以上本地生成目录
```

当前实际状态：

| 项目 | 值 |
|---|---|
| Go 版本 | `go1.26.5 windows/amd64` |
| Go 可执行文件 | `.tools\go\bin\go.exe` |
| 项目模块 | `github.com/GeoVerseLabs/geoverse-map-server` |
| `go.mod` 语言版本 | `go 1.24.0`（最低要求，CI 对齐） |
| 第三方 Go 模块 | 有（pgx、orb、sqlite 等） |
| 系统 PATH 是否包含该 Go | 否 |

## 2. 为什么使用项目内 Go

- 不需要管理员权限。
- 不写入 `C:\Program Files\Go`。
- 不修改系统/用户环境变量。
- 工具链只对当前工程有效。
- 删除 `.tools` 即可移除，不影响其他工程。

## 3. 推荐的使用方式

在 `geoverse-map-server` 目录执行：

```powershell
. .\setup-go-env.ps1
```

常用命令：

```powershell
# 格式检查
go vet ./...

# 单元测试
go test ./...

# 构建 Windows 二进制
New-Item -ItemType Directory -Force bin | Out-Null
go build -buildvcs=false -trimpath -ldflags='-s -w' -o 'bin\geoverse.exe' ./cmd/geoverse

# 本地运行
.\bin\geoverse.exe -config config.example.yaml
```

## 4. 缓存与 GOPATH 设置

Go 默认尝试在用户目录创建构建缓存，受限环境下改为工程内目录：

| Go 环境项 | 当前工程使用值 |
|---|---|
| `GOROOT` | `.tools\go` |
| `GOPATH` | `.cache\gopath` |
| `GOCACHE` | `.cache\go-build` |
| `GOMODCACHE` | `.cache\gopath\pkg\mod` |

这些值只通过执行测试/构建的 PowerShell 进程临时设置，没有持久写入用户环境变量。

## 5. Docker 环境

Dockerfile 使用 `golang:1.24-alpine`，与本地 `.tools/go` 相互独立。最终运行阶段只复制服务二进制，不会把 Go 编译器带入运行镜像。

## 6. 生成物和 Git 状态

`.gitignore` 当前包含：

```gitignore
.tools/
.cache/
bin/
```

因此以下内容都属于本机生成物，不应提交：

- `.tools/go`：便携工具链。
- `.cache/go-build`：编译缓存。
- `.cache/gopath`：GOPATH 和模块缓存。
- `bin/geoverse.exe`：构建产物。

应提交的是 Go 源码、`go.mod`、`go.sum`、Dockerfile、测试、样例数据和文档。

## 7. 如何清理或重新安装

### 只清理缓存和构建产物

```powershell
Remove-Item -Recurse -Force -LiteralPath '.cache'
Remove-Item -Recurse -Force -LiteralPath 'bin'
```

### 完全移除项目内 Go

```powershell
Remove-Item -Recurse -Force -LiteralPath '.tools'
```

这不会影响 Windows 系统或其他项目。

### 重新创建相同环境

```powershell
$Version = '1.26.5'
$Archive = ".tools-go$Version.zip"

Invoke-WebRequest `
  -Uri "https://go.dev/dl/go$Version.windows-amd64.zip" `
  -OutFile $Archive

New-Item -ItemType Directory -Force '.tools' | Out-Null
Expand-Archive -LiteralPath $Archive -DestinationPath '.tools' -Force
Remove-Item -LiteralPath $Archive

& '.\.tools\go\bin\go.exe' version
```
