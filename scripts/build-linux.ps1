# scripts/build-linux.ps1
#
# Windows PowerShell 用：cross-compile 当前项目到 Linux amd64 / arm64
#
# 用法：
#   pwsh scripts/build-linux.ps1                 # 默认 amd64
#   pwsh scripts/build-linux.ps1 -Arch arm64     # ARM 服务器
#   pwsh scripts/build-linux.ps1 -Output myapp   # 自定义输出名
#
# 背景：
#   - go build 默认产出当前平台 binary（Windows .exe）
#   - 直接把 .exe 改名成 chatwitheino-linux 上传 → "cannot execute binary file"
#   - GOOS / GOARCH 必须在 go build 前设到环境变量（PowerShell 进程级）
#   - ldflags="-s -w" 砍调试符号，binary 从 ~20MB → ~14MB（实际看依赖）
#
# 上传配套：
#   scp -O chatwitheino-linux root@server:/home/www/wwwroot/agent.yuyuanyuan.cn/
#   # -O 强制 binary mode（PowerShell 自带 scp 不一定默认走 binary）

param(
    [ValidateSet("amd64", "arm64")]
    [string]$Arch = "amd64",
    [string]$Output = "chatwitheino-linux"
)

$ErrorActionPreference = "Stop"

Write-Host ">>> 准备 cross-compile: GOOS=linux GOARCH=$Arch" -ForegroundColor Cyan

# 设置 Go 交叉编译环境变量（当前 PowerShell 进程）
$env:GOOS = "linux"
$env:GOARCH = $Arch
$env:CGO_ENABLED = "0"  # 纯 Go cross-compile 不需要 CGO；用了 cgo 就要配 CC 工具链

Write-Host ">>> 执行 go build..." -ForegroundColor Cyan
$buildStart = Get-Date
go build -ldflags="-s -w" -o $Output .
if ($LASTEXITCODE -ne 0) {
    Write-Host "✗ go build 失败，退出码 $LASTEXITCODE" -ForegroundColor Red
    exit $LASTEXITCODE
}
$buildEnd = Get-Date
$duration = $buildEnd - $buildStart

# 校验产物
$bin = Get-Item $Output -ErrorAction SilentlyContinue
if ($null -eq $bin) {
    Write-Host "✗ 找不到产物 $Output" -ForegroundColor Red
    exit 1
}
$sizeMB = [math]::Round($bin.Length / 1MB, 2)

Write-Host ""
Write-Host "✓ build 完成" -ForegroundColor Green
Write-Host "  文件: $($bin.FullName)"
Write-Host "  大小: $sizeMB MB"
Write-Host "  耗时: $($duration.ToString('mm\:ss'))"
Write-Host ""
Write-Host "  上传到服务器：" -ForegroundColor Yellow
Write-Host "    scp -O $Output root@<server>:/home/www/wwwroot/agent.yuyuanyuan.cn/" -ForegroundColor White
Write-Host ""
Write-Host "  部署机检查格式：" -ForegroundColor Yellow
Write-Host "    file $Output" -ForegroundColor White
Write-Host "    # 期望输出: ELF 64-bit LSB executable, x86-64, version 1 (SYSV), statically linked" -ForegroundColor DarkGray
