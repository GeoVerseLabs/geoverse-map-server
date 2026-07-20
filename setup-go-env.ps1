# GeoVerse Map Server — Go 环境设置脚本
# 用法：在项目根目录执行 . .\setup-go-env.ps1（注意前面的 "." 用于 source 到当前会话）

$Project = (Resolve-Path '.').Path
$Go = Join-Path $Project '.tools\go\bin\go.exe'
$GoFmt = Join-Path $Project '.tools\go\bin\gofmt.exe'

$env:GOCACHE = (New-Item -ItemType Directory -Force '.cache\go-build').FullName
$env:GOPATH  = (New-Item -ItemType Directory -Force '.cache\gopath').FullName

Write-Host "Go toolchain: $( & $Go version )"
Write-Host "GOCACHE     : $env:GOCACHE"
Write-Host "GOPATH      : $env:GOPATH"
