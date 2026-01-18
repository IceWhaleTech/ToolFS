#!/bin/bash
# Script to test FUSE adapter using Docker

set -e

echo "=== ToolFS FUSE Docker Test ==="

# Check if Docker is available
if ! command -v docker &> /dev/null; then
    echo "Error: Docker is not installed or not in PATH"
    exit 1
fi

# Create test directories
echo "Creating test directories..."
mkdir -p test_mount test_data

# Build and run Docker container
echo "Building Docker image..."
docker build -f Dockerfile.fuse -t toolfs-fuse-test .

echo "Running FUSE test in Docker container..."
docker run --rm \
    --privileged \
    --cap-add SYS_ADMIN \
    --device /dev/fuse \
    -v "$(pwd)/test_mount:/mnt/toolfs:shared" \
    -v "$(pwd)/test_data:/data" \
    -e MOUNT_POINT=/mnt/toolfs \
    toolfs-fuse-test

echo ""
echo "=== Test completed ==="
echo "Check test_mount/ directory for mounted filesystem contents"


