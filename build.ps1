# Builds NLRShell.exe with the toolchain in "..\Go Tool Chain".
#   .\build.ps1            release build
#   .\build.ps1 -Test      run the test suite first
#   .\build.ps1 -Console   keep a console window (shows panics and logs)
param(
    [switch]$Test,
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
Set-Location $root

if (-not (Test-Path "rsrc_windows_amd64.syso")) {
    go run ./tools/mkres
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}
# Build against the patched Gio: upstream ignores app.Decorated(false) on X11
# and Wayland and sizes the pointer cursor itself, so the Linux fixes live in
# tools/giopatch/gioui-fixes.patch. A plain go build uses upstream Gio.
$modfile = (go run ./tools/giopatch | Select-Object -Last 1).Trim()
if ($LASTEXITCODE -ne 0 -or -not $modfile) { exit 1 }
if ($Test) {
    go vet -modfile=$modfile ./...
    go test -modfile=$modfile ./...
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}
$ld = "-s -w -X gioui.org/app.ID=nlrshell"
if (-not $Console) { $ld += " -H windowsgui" }
go build -modfile=$modfile -trimpath -ldflags $ld -o NLRShell.exe .
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
$f = Get-Item NLRShell.exe
"Built {0} ({1:N1} MB)" -f $f.FullName, ($f.Length / 1MB)
