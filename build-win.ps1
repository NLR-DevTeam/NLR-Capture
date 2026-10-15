# Builds NLR Capture for Windows into dist\windows\, with the toolchain in
# "..\Go Tool Chain" when it is there, else the go on PATH.
#   .\build-win.ps1            release build
#   .\build-win.ps1 -Console   keep a console window (shows panics and logs)
#
# dist\windows\ holds everything the program needs; nothing is downloaded
# at run time. The program file is named nlrcapture.exe.bin because it is
# never started on the build machine: copy the folder to the test machine
# and rename it to nlrcapture.exe there.
param(
    [switch]$Console
)
$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
$tc = Join-Path (Split-Path $root -Parent) "Go Tool Chain"
if (Test-Path (Join-Path $tc "go\bin\go.exe")) {
    $env:GOPATH = Join-Path $tc "gopath"
    $env:GOCACHE = Join-Path $tc "gocache"
    $env:Path = (Join-Path $tc "go\bin") + ";" + $env:Path
}
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
Set-Location $root

# Wintun (the virtual adapter driver) ships with the program. The official
# release is fetched once into deps\ and must carry WireGuard's signature.
$deps = Join-Path $root "deps"
$wintunDll = Join-Path $deps "wintun\bin\amd64\wintun.dll"
if (-not (Test-Path $wintunDll)) {
    New-Item -ItemType Directory -Force $deps | Out-Null
    $zip = Join-Path $deps "wintun-0.14.1.zip"
    Invoke-WebRequest -UseBasicParsing "https://www.wintun.net/builds/wintun-0.14.1.zip" -OutFile $zip
    Expand-Archive -Force $zip $deps
    Remove-Item $zip
}
$sig = Get-AuthenticodeSignature $wintunDll
if ($sig.Status -ne "Valid" -or $sig.SignerCertificate.Subject -notmatch "WireGuard LLC") {
    Write-Error "wintun.dll signature check failed: $($sig.Status) $($sig.SignerCertificate.Subject)"
    exit 1
}

if (-not (Test-Path "rsrc_windows_amd64.syso")) {
    go run ./tools/mkres
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}
$ld = "-s -w -X gioui.org/app.ID=nlrcapture"
if (-not $Console) { $ld += " -H windowsgui" }
$dist = Join-Path $root "dist\windows"
if (Test-Path $dist) { Remove-Item -Recurse -Force $dist }
New-Item -ItemType Directory -Force $dist | Out-Null
go build -trimpath -ldflags $ld -o (Join-Path $dist "nlrcapture.exe.bin") .
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Copy-Item $wintunDll $dist
$lic = Join-Path $dist "licenses\wintun"
New-Item -ItemType Directory -Force $lic | Out-Null
Copy-Item (Join-Path $deps "wintun\LICENSE.txt") $lic

Get-ChildItem -Recurse -File $dist | ForEach-Object {
    "{0,-40} {1,10:N0} bytes" -f $_.FullName.Substring($dist.Length + 1), $_.Length
}
