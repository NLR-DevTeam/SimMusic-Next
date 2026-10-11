$ErrorActionPreference = "Stop"
$workspace = Split-Path $PSScriptRoot -Parent
$web = Join-Path $workspace "internal/webui/web"
# Keep the user's original SimUI files untouched; adapt the embedded copy.
if (!(Test-Path -LiteralPath (Join-Path $workspace "simui.v5.js")) -or !(Test-Path -LiteralPath (Join-Path $workspace "simui.v5.css"))) {
    if ((Test-Path -LiteralPath (Join-Path $web "simui.v5.js")) -and (Test-Path -LiteralPath (Join-Path $web "simui.v5.css"))) { return }
    throw "SimUI source files are missing"
}
$javascript = Get-Content -LiteralPath (Join-Path $workspace "simui.v5.js") -Raw
$javascript = [regex]::Replace($javascript, '\b(inputElement|toastElement|bar|dot|dropdown)\.style\b', 'simCss($1)')
$javascript = [regex]::Replace($javascript, '(?m)^\s*(?:functionBtn\.tabIndex = 0;|dialog\.simsvLastActiveElement = document\.activeElement;|if \(dialog\.simsvLastActiveElement\) dialog\.simsvLastActiveElement\.focus\(\);|setTimeout\(\(\) => submitButton\.focus\(\), (?:10|200)\);)\r?\n', '')
$css = Get-Content -LiteralPath (Join-Path $workspace "simui.v5.css") -Raw
$css = [regex]::Replace($css, '(?m)^@import[^\r\n]*', '')
$css = [regex]::Replace($css, '(?m)^@font-face[^\r\n]*', '')
$css = [regex]::Replace($css, ',input\[type=(?:checkbox|radio)\](?::checked)?:focus-visible', '')
Set-Content -LiteralPath (Join-Path $web "simui.v5.js") -Value $javascript -Encoding utf8
Set-Content -LiteralPath (Join-Path $web "simui.v5.css") -Value $css -Encoding utf8
