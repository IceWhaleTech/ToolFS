#!/bin/bash
# Script to test FUSE adapter using WSL (Windows Subsystem for Linux)

set -e

echo "=== ToolFS FUSE WSL Test ==="

# Check if we're in WSL
if [ -z "$WSL_DISTRO_NAME" ] && [ -z "$WSL_INTEROP" ]; then
    echo "Warning: This script is designed for WSL. Continuing anyway..."
fi

# Check if FUSE is available
if ! command -v fusermount &> /dev/null; then
    echo "Installing FUSE..."
    sudo apt-get update
    sudo apt-get install -y fuse
fi

# Create mount point
MOUNT_POINT="$HOME/toolfs_mount"
echo "Creating mount point at $MOUNT_POINT..."
mkdir -p "$MOUNT_POINT"

# Build test program
echo "Building FUSE test program..."
go build -tags "linux" -o fuse_test ./cmd/fuse_test/main.go

# Run test with mount point
echo "Running FUSE test..."
MOUNT_POINT="$MOUNT_POINT" ./fuse_test

# Cleanup
echo ""
echo "Cleaning up..."
fusermount -u "$MOUNT_POINT" 2>/dev/null || true
rmdir "$MOUNT_POINT" 2>/dev/null || true
rm -f fuse_test

echo "=== Test completed ==="


