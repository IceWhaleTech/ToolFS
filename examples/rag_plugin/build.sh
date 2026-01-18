#!/bin/bash

# Build RAG plugin as WASM module

echo "Building RAG plugin for WASM..."

# Build for WASM
GOOS=js GOARCH=wasm go build -o rag.wasm .

echo "Build complete! Output: rag.wasm"
echo ""
echo "To test the plugin:"
echo "1. Load rag.wasm in your WASM runtime"
echo "2. Export the PluginInstance for ToolFS to use"

