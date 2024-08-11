#!/bin/bash
set -e

# INSTRUCTIONS:
# Requirements: macOS, go, zig, tar, zip
# Run this on macOS:
#
# ./buildall.bash
#

if [[ $(go env GOOS) != "darwin" ]]; then
	echo "Must be run on macOS"
	exit
fi

mkdir -p dist
pushd dist

echo "Building macOS..."
go build -o timelinize_alpha ..
echo "Compressing macOS build..."
zip timelinize_alpha_mac.zip timelinize_alpha
rm timelinize_alpha

echo "Building Linux..."
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 CC="zig cc -target x86_64-linux" CXX="zig c++ -target x86_64-linux" go build -o timelinize_alpha ..
echo "Compressing Linux build..."
tar --xz -vcf timelinize_alpha_linux.tar.xz timelinize_alpha
rm timelinize_alpha

echo "Building Windows..."
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC="zig cc -target x86_64-windows" CXX="zig c++ -target x86_64-windows" go build -o timelinize_alpha.exe ..
echo "Compressing Windows build..."
zip timelinize_alpha_windows.zip timelinize_alpha.exe
rm timelinize_alpha.exe

popd

echo "🎉 Success! Builds are in the ./dist folder."
