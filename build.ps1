# Builds the Go core, native CEF host and complete portable CEF runtime.
param([switch]$SkipTests)
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
$goCommand = Get-Command go -ErrorAction SilentlyContinue
if (!$goCommand) { throw "Go 1.27.1 or newer is required on PATH" }
$goExe = $goCommand.Source
$gccCommand = Get-Command gcc -ErrorAction SilentlyContinue
$gccExe = if ($gccCommand) { $gccCommand.Source } else { Join-Path $env:LOCALAPPDATA "Microsoft/WinGet/Packages/BrechtSanders.WinLibs.POSIX.UCRT_Microsoft.Winget.Source_8wekyb3d8bbwe/mingw64/bin/gcc.exe" }
if (!(Test-Path -LiteralPath $gccExe)) { throw "Install a Windows x64 MinGW GCC toolchain to compile the CEF C-API host" }
$env:PATH = "$(Split-Path $goExe);$(Split-Path $gccExe);$env:PATH"
& "$PSScriptRoot/tools/prepare-ui.ps1"
New-Item -ItemType Directory -Force build/runtime | Out-Null
if (Test-Path -LiteralPath "$PSScriptRoot/cef/host.c") {
$cefRoot = & "$PSScriptRoot/tools/setup-cef.ps1"
$windresExe = Join-Path (Split-Path $gccExe) "windres.exe"
& $windresExe cef/host.rc -O coff -o build/runtime/cef-icons.o
if ($LASTEXITCODE -ne 0) { throw "CEF icon resource build failed" }
& $gccExe -std=gnu11 -O2 -municode -mwindows -static "-specs=cef/gcc.specs" -I $cefRoot cef/host.c build/runtime/cef-icons.o -o build/runtime/SimMusicCef.exe -luser32 -lgdi32 -lshell32 -lcomctl32 -ldwmapi -lole32 -lpropsys -luuid
if ($LASTEXITCODE -ne 0) { throw "CEF host build failed" }
Remove-Item -LiteralPath build/runtime/cef-icons.o
if (Test-Path -LiteralPath "$cefRoot/Release") { Copy-Item -Path "$cefRoot/Release/*" -Destination build/runtime -Recurse -Force -Exclude "*.lib", "*.pdb" }
if (Test-Path -LiteralPath "$cefRoot/Resources") { Copy-Item -Path "$cefRoot/Resources/*" -Destination build/runtime -Recurse -Force }
Copy-Item -LiteralPath "$cefRoot/LICENSE.txt" -Destination build/runtime/CEF-LICENSE.txt -Force
if (Test-Path -LiteralPath "$cefRoot/CREDITS.html") { Copy-Item -LiteralPath "$cefRoot/CREDITS.html" -Destination build/runtime -Force }
Copy-Item -LiteralPath internal/webui/web/assets/tray.ico -Destination build/runtime -Force
} elseif (!(Test-Path -LiteralPath "$PSScriptRoot/build/runtime/SimMusicCef.exe") -or !(Test-Path -LiteralPath "$PSScriptRoot/build/runtime/libcef.dll") -or (Get-Item -LiteralPath "$PSScriptRoot/build/runtime/libcef.dll").VersionInfo.FileVersion -notmatch '^106\.') {
    throw "CEF host source is missing and no Chromium 106 runtime is available"
}
if (!$SkipTests) {
    & $goExe test ./...
    if ($LASTEXITCODE -ne 0) { throw "Go checks failed" }
}
& $goExe build -trimpath -ldflags "-H windowsgui -s -w" -o build/SimMusicNext.exe .
if ($LASTEXITCODE -ne 0) { throw "Go build failed" }
Write-Host "Built build/SimMusicNext.exe with Chromium 106. Distribute SimMusicNext.exe and the runtime directory; keep build/data for existing user data."


