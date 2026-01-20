@echo off
REM Build RAG skill as WASM module for Windows

echo Building RAG skill for WASM...

REM Build for WASM
set GOOS=js
set GOARCH=wasm
go build -o rag.wasm .

echo Build complete! Output: rag.wasm
echo.
echo To test the skill:
echo 1. Load rag.wasm in your WASM runtime
echo 2. Export the SkillInstance for ToolFS to use

