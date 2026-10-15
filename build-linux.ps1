# Cross-compiles NLR Capture for Linux (x86-64) on Windows into dist\linux\.
#   .\build-linux.ps1
#
# Gio needs cgo on Linux. The C side is built with zig against a Debian
# sysroot holding the X11, Wayland, EGL and xkbcommon headers and
# libraries, all under "..\Go Tool Chain\linux-cross":
#   zig-x86_64-windows-0.14.1\zig.exe   C compiler and linker
#   sysroot\                            headers and libraries
#   pkg-config.exe                      built from pkgcfg\ when missing
# On a Linux machine, use build-linux.sh instead.
#
# The program is built against Gio with the Linux fixes in tools/giopatch
# (window decorations, cursor shape, window state); see tools/giopatch.
$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
$tc = Join-Path (Split-Path $root -Parent) "Go Tool Chain"
if (Test-Path (Join-Path $tc "go\bin\go.exe")) {
    $env:GOPATH = Join-Path $tc "gopath"
    $env:GOCACHE = Join-Path $tc "gocache"
    $env:Path = (Join-Path $tc "go\bin") + ";" + $env:Path
}
Set-Location $root

# The variables set below stay in the calling session otherwise, and a
# Windows build run after this one would target Linux.
$touched = "GOOS", "GOARCH", "CGO_ENABLED", "CC", "CXX", "PKG_CONFIG", "SYSROOT"
$saved = @{}
foreach ($n in $touched) { $saved[$n] = [Environment]::GetEnvironmentVariable($n) }
try {
    $cross = Join-Path $tc "linux-cross"
    $zig = Join-Path $cross "zig-x86_64-windows-0.14.1\zig.exe"
    $sysroot = Join-Path $cross "sysroot"
    $pkgConfig = Join-Path $cross "pkg-config.exe"
    foreach ($p in @($zig, $sysroot)) {
        if (-not (Test-Path $p)) { Write-Error "missing $p (see the comment at the top of build-linux.ps1)"; exit 1 }
    }
    if (-not (Test-Path $pkgConfig)) {
        Push-Location (Join-Path $cross "pkgcfg")
        $env:GOOS = ""; $env:GOARCH = ""; $env:CGO_ENABLED = "0"
        go build -o $pkgConfig .
        $code = $LASTEXITCODE
        Pop-Location
        if ($code -ne 0) { exit $code }
    }

    # The paths go into compiler command lines and pkg-config output, which
    # are split on spaces: use the short 8.3 names.
    $fso = New-Object -ComObject Scripting.FileSystemObject
    $short = { param($p) ($(if (Test-Path $p -PathType Container) { $fso.GetFolder($p) } else { $fso.GetFile($p) }).ShortPath) -replace '\\', '/' }
    $zigS = & $short $zig
    $env:SYSROOT = & $short $sysroot
    $env:PKG_CONFIG = & $short $pkgConfig
    # glibc 2.31: Debian 11, Ubuntu 20.04 and anything newer.
    $target = "x86_64-linux-gnu.2.31"
    $env:CC = "$zigS cc -target $target"
    $env:CXX = "$zigS c++ -target $target"

    # The patched Gio is materialized by a host build of the tool.
    $env:GOOS = ""; $env:GOARCH = ""; $env:CGO_ENABLED = "0"
    $modfile = go run ./tools/giopatch | Select-Object -Last 1
    if ($LASTEXITCODE -ne 0 -or -not $modfile) { Write-Error "patching Gio failed"; exit 1 }
    $modfile = $modfile.Trim()

    $env:GOOS = "linux"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "1"
    $dist = Join-Path $root "dist\linux"
    if (Test-Path $dist) { Remove-Item -Recurse -Force $dist }
    New-Item -ItemType Directory -Force $dist | Out-Null
    $out = Join-Path $dist "nlrcapture"
    go build "-modfile=$modfile" -trimpath -ldflags "-s -w -X gioui.org/app.ID=nlrcapture" -o $out .
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    # zig's linker keeps the symbols -s asks Go to drop.
    & $zig objcopy --strip-all $out "$out.stripped"
    if ($LASTEXITCODE -eq 0) { Move-Item -Force "$out.stripped" $out }
    Copy-Item (Join-Path $root "build\icon.png") (Join-Path $dist "nlrcapture.png")

    Get-ChildItem -Recurse -File $dist | ForEach-Object {
        "{0,-40} {1,10:N0} bytes" -f $_.FullName.Substring($dist.Length + 1), $_.Length
    }
} finally {
    foreach ($n in $touched) { [Environment]::SetEnvironmentVariable($n, $saved[$n]) }
}
