#!/bin/bash
#
# fix-macos-dylib-paths.sh
#
# This script makes macOS binaries portable by converting absolute Homebrew
# library paths to @rpath-based paths. This allows the same binary to work
# on both Intel Macs (/usr/local) and Apple Silicon Macs (/opt/homebrew).
#
# Usage: ./fix-macos-dylib-paths.sh <binary>
#

set -e

BINARY="$1"

if [ -z "$BINARY" ]; then
    echo "Usage: $0 <binary>"
    exit 1
fi

if [ ! -f "$BINARY" ]; then
    echo "Error: Binary not found: $BINARY"
    exit 1
fi

echo "Fixing dynamic library paths for: $BINARY"

# Add rpath entries for common Homebrew locations
# Apple Silicon uses /opt/homebrew, Intel uses /usr/local
echo "Adding rpath entries..."

# Check if rpath already exists before adding (to avoid errors on re-runs)
add_rpath_if_missing() {
    local rpath="$1"
    if ! otool -l "$BINARY" | grep -A2 "LC_RPATH" | grep -q "$rpath"; then
        echo "  Adding rpath: $rpath"
        install_name_tool -add_rpath "$rpath" "$BINARY"
    else
        echo "  Rpath already exists: $rpath"
    fi
}

add_rpath_if_missing "/opt/homebrew/lib"
add_rpath_if_missing "/usr/local/lib"

# Also add paths for common Homebrew keg-only formulas that vips depends on
add_rpath_if_missing "/opt/homebrew/opt/glib/lib"
add_rpath_if_missing "/usr/local/opt/glib/lib"
add_rpath_if_missing "/opt/homebrew/opt/vips/lib"
add_rpath_if_missing "/usr/local/opt/vips/lib"
add_rpath_if_missing "/opt/homebrew/opt/gettext/lib"
add_rpath_if_missing "/usr/local/opt/gettext/lib"

# Get all dynamic library dependencies
echo "Converting absolute paths to @rpath..."
otool -L "$BINARY" | tail -n +2 | while read -r line; do
    # Extract the library path (first field before the parentheses)
    lib_path=$(echo "$line" | awk '{print $1}')

    # Skip system libraries and already-converted paths
    if [[ "$lib_path" == /System/* ]] || \
       [[ "$lib_path" == /usr/lib/* ]] || \
       [[ "$lib_path" == @* ]]; then
        continue
    fi

    # Check if this is a Homebrew library path
    if [[ "$lib_path" == /opt/homebrew/* ]] || [[ "$lib_path" == /usr/local/* ]]; then
        # Extract just the library filename
        lib_name=$(basename "$lib_path")
        new_path="@rpath/$lib_name"

        echo "  $lib_path -> $new_path"
        install_name_tool -change "$lib_path" "$new_path" "$BINARY"
    fi
done

echo "Done! Verifying changes..."
echo ""
echo "Updated library paths:"
otool -L "$BINARY" | grep -v "^$BINARY" | head -20

echo ""
echo "Rpath entries:"
otool -l "$BINARY" | grep -A2 "LC_RPATH" | grep "path " || echo "  (none found)"
