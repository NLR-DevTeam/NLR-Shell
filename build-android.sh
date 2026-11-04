#!/bin/sh
# Builds NLRShell.apk.
#   ./build-android.sh                  arm64 phones and tablets
#   ./build-android.sh arm64,arm        also 32-bit ARM devices
#   ./build-android.sh amd64            the x86_64 emulator
# Needs the Android SDK with build-tools, a platform and the NDK
# (ANDROID_HOME, default ~/Android/Sdk) and a JDK 17+; see tools/apk.
set -e
cd "$(dirname "$0")"
: "${ANDROID_HOME:=$HOME/Android/Sdk}"
export ANDROID_HOME
go run ./tools/apk -arch "${1:-arm64}" -o NLRShell.apk
