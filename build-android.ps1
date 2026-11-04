# Builds NLRShell.apk with the toolchain in "..\Go Tool Chain", if present.
#   .\build-android.ps1                     arm64 phones and tablets
#   .\build-android.ps1 -Arch arm64,arm     also 32-bit ARM devices
#   .\build-android.ps1 -Arch amd64         the x86_64 emulator
#   .\build-android.ps1 -Keystore my.jks    sign with a release key
# Needs the Android SDK with build-tools, a platform and the NDK (ANDROID_HOME,
# default %LOCALAPPDATA%\Android\Sdk) and a JDK 17+; see tools/apk.
param(
    [string]$Arch = "arm64",
    [string]$Keystore = "",
    [string]$StorePass = "android"
)
$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
$tc = Join-Path (Split-Path $root -Parent) "Go Tool Chain"
if (Test-Path (Join-Path $tc "go\bin\go.exe")) {
    $env:GOPATH = Join-Path $tc "gopath"
    $env:GOCACHE = Join-Path $tc "gocache"
    $env:Path = (Join-Path $tc "go\bin") + ";" + $env:Path
}
if (-not $env:ANDROID_HOME) {
    $env:ANDROID_HOME = Join-Path $env:LOCALAPPDATA "Android\Sdk"
}
Set-Location $root
$apkArgs = @("-arch", $Arch, "-o", "NLRShell.apk")
if ($Keystore) { $apkArgs += @("-keystore", $Keystore, "-storepass", $StorePass) }
go run ./tools/apk @apkArgs
exit $LASTEXITCODE
