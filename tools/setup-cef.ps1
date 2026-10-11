param([string]$Version = "106.1.1+g5891c70+chromium-106.0.5249.119")
$ErrorActionPreference = "Stop"
$workspace = Split-Path $PSScriptRoot -Parent
$cefDirectory = Join-Path $workspace "cef"
$distribution = "cef_binary_${Version}_windows64_minimal"
$archiveName = "$distribution.tar.bz2"
$archive = Join-Path $cefDirectory $archiveName
$target = Join-Path $cefDirectory $distribution
$url = "https://cef-builds.spotifycdn.com/$([Uri]::EscapeDataString($archiveName))"
New-Item -ItemType Directory -Force $cefDirectory | Out-Null
$sourceSDK = Join-Path $cefDirectory "chromiumembedded-cef-5891c70"
if ((Test-Path -LiteralPath (Join-Path $sourceSDK "include/cef_api_hash.h")) -and (Test-Path -LiteralPath (Join-Path $workspace "build/runtime/libcef.dll")) -and (Get-Item -LiteralPath (Join-Path $workspace "build/runtime/libcef.dll")).VersionInfo.FileVersion -match '^106\.') { return $sourceSDK }
if (!(Test-Path (Join-Path $target "include/cef_api_hash.h"))) {
    if (!(Test-Path $archive)) {
        & curl.exe -L --fail --retry 3 $url -o $archive
        if ($LASTEXITCODE -ne 0) { throw "CEF download failed" }
    }
    $expected = (Invoke-RestMethod "$url.sha1").ToString().Trim().Split(" ")[0]
    if ((Get-FileHash -LiteralPath $archive -Algorithm SHA1).Hash.ToLower() -ne $expected.ToLower()) { throw "CEF checksum mismatch" }
    & tar -xjf $archive -C $cefDirectory
    if ($LASTEXITCODE -ne 0) { throw "CEF extraction failed" }
}
if (!(Test-Path (Join-Path $target "Release/libcef.dll"))) { throw "Incomplete CEF distribution" }
return $target
