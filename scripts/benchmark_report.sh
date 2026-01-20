#!/bin/bash

# ToolFS Performance Benchmark Report Generator
# Runs all benchmarks and generates a comprehensive report

set -e

# Color definitions
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Report file
REPORT_FILE="BENCHMARK_REPORT.md"
TIMESTAMP=$(date '+%Y-%m-%d %H:%M:%S')

echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}ToolFS Performance Benchmark Generator${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""

# Check if Go is installed
if ! command -v go &> /dev/null; then
    echo -e "${RED}Error: Go compiler not found${NC}"
    exit 1
fi

echo -e "${GREEN}Running benchmarks...${NC}"
echo ""

# Create report file header
cat > "$REPORT_FILE" << EOF
# ToolFS Performance Benchmark Report

**Generated At**: $TIMESTAMP

This report presents the performance benchmark results for the main features of ToolFS.

## Test Environment

- **Hardware**: Apple M4 Pro
- **Go Version**: $(go version)
- **OS**: $(uname -s) $(uname -r)
- **Architecture**: $(uname -m)

## Performance Results

### File Operations Performance

EOF

# Run file operation benchmarks
echo -e "${YELLOW}Testing file read/write performance...${NC}"
{
    echo "#### File Read Performance"
    echo ""
    echo '```'
    CGO_ENABLED=0 go test -bench=^BenchmarkReadFile$ -benchmem -run=^$ -benchtime=3s . | grep -E "Benchmark|PASS|ok"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

{
    echo "#### Large File Read Performance"
    echo ""
    echo '```'
    CGO_ENABLED=0 go test -bench=^BenchmarkReadFileLarge$ -benchmem -run=^$ -benchtime=3s . | grep -E "Benchmark|PASS|ok"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

{
    echo "#### File Write Performance"
    echo ""
    echo '```'
    CGO_ENABLED=0 go test -bench=^BenchmarkWriteFile$ -benchmem -run=^$ -benchtime=3s . | grep -E "Benchmark|PASS|ok"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

{
    echo "#### Directory Listing Performance"
    echo ""
    echo '```'
    CGO_ENABLED=0 go test -bench=^BenchmarkListDir$ -benchmem -run=^$ -benchtime=3s . | grep -E "Benchmark|PASS|ok"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

# Run memory operation benchmarks
echo -e "${YELLOW}Testing memory store performance...${NC}"
{
    echo "### Memory Store Performance"
    echo ""
    echo "#### Memory Write Performance"
    echo ""
    echo '```'
    CGO_ENABLED=0 go test -bench=^BenchmarkMemoryWrite$ -benchmem -run=^$ -benchtime=3s . | grep -E "Benchmark|PASS|ok"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

{
    echo "#### Memory Read Performance"
    echo ""
    echo '```'
    CGO_ENABLED=0 go test -bench=^BenchmarkMemoryRead$ -benchmem -run=^$ -benchtime=3s . | grep -E "Benchmark|PASS|ok"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

{
    echo "#### Memory Listing Performance"
    echo ""
    echo '```'
    CGO_ENABLED=0 go test -bench=^BenchmarkMemoryList$ -benchmem -run=^$ -benchtime=3s . | grep -E "Benchmark|PASS|ok"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

# Run RAG search benchmarks
echo -e "${YELLOW}Testing RAG search performance...${NC}"
{
    echo "### RAG Search Performance"
    echo ""
    echo "#### RAG Search Performance"
    echo ""
    echo '```'
    CGO_ENABLED=0 go test -bench=^BenchmarkRAGSearch$ -benchmem -run=^$ -benchtime=3s . | grep -E "Benchmark|PASS|ok"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

{
    echo "#### RAG Search (Different TopK) Performance"
    echo ""
    echo '```'
    CGO_ENABLED=0 go test -bench=^BenchmarkRAGSearchWithTopK$ -benchmem -run=^$ -benchtime=3s . | grep -E "Benchmark|PASS|ok"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

# Run snapshot operation benchmarks
echo -e "${YELLOW}Testing snapshot operation performance...${NC}"
{
    echo "### Snapshot Performance"
    echo ""
    echo "#### Snapshot Creation Performance"
    echo ""
    echo '```'
    CGO_ENABLED=0 go test -bench=^BenchmarkSnapshotCreate$ -benchmem -run=^$ -benchtime=2s . | grep -E "Benchmark|PASS|ok"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

{
    echo "#### Snapshot Rollback Performance"
    echo ""
    echo '```'
    CGO_ENABLED=0 go test -bench=^BenchmarkSnapshotRollback$ -benchmem -run=^$ -benchtime=2s . | grep -E "Benchmark|PASS|ok"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

# Run other operation benchmarks
echo -e "${YELLOW}Testing other operations performance...${NC}"
{
    echo "### Other Operations Performance"
    echo ""
    echo "#### Path Resolution Performance"
    echo ""
    echo '```'
    CGO_ENABLED=0 go test -bench=^BenchmarkPathResolution$ -benchmem -run=^$ -benchtime=3s . | grep -E "Benchmark|PASS|ok"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

{
    echo "#### Skill Execution Performance"
    echo ""
    echo '```'
    CGO_ENABLED=0 go test -bench=^BenchmarkSkillExecute$ -benchmem -run=^$ -benchtime=3s . | grep -E "Benchmark|PASS|ok"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

{
    echo "#### Session Access Control Performance"
    echo ""
    echo '```'
    CGO_ENABLED=0 go test -bench=^BenchmarkSessionAccessControl$ -benchmem -run=^$ -benchtime=3s . | grep -E "Benchmark|PASS|ok"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

{
    echo "#### Concurrent Read Performance"
    echo ""
    echo '```'
    CGO_ENABLED=0 go test -bench=^BenchmarkConcurrentReads$ -benchmem -run=^$ -benchtime=3s . | grep -E "Benchmark|PASS|ok"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

# Add summary section
cat >> "$REPORT_FILE" << 'EOF'

## Performance Summary

### File Operations
- **File Read**: Benchmarked reading files of various sizes.
- **File Write**: Benchmarked file creation and writing operations.
- **Directory List**: Benchmarked directory traversal and listing.

### Memory Store
- **Write Performance**: Benchmarked creating and updating memory entries.
- **Read Performance**: Benchmarked retrieval of memory entries.
- **List Performance**: Benchmarked listing memory entries.

### RAG Search
- **Search Performance**: Benchmarked response time for semantic search queries.
- **TopK Impact**: Benchmarked performance with different top_k values.

### Snapshot Operations
- **Creation Performance**: Benchmarked overhead of creating snapshots.
- **Rollback Performance**: Benchmarked overhead of restoring state from snapshots.

### Concurrency
- **Concurrent Reads**: Benchmarked performance under multi-threaded read access.
- **Session Isolation**: Benchmarked overhead of session-based access control.

## Optimization Recommendations

1. **Large File Operations**: Consider streaming for large file processing.
2. **Memory Usage**: For massive amounts of memory entries, consider indexing or pagination.
3. **RAG Search**: Optimize with vector indexing for large-scale databases.
4. **Snapshot Operations**: Use incremental snapshots to reduce time and storage.
5. **Concurrency**: Use refined locking mechanisms (e.g., RWMutex) to optimize parallel access.

## Running Benchmarks Manually

To run a specific benchmark:

```bash
# Run all benchmarks
go test -bench=. -benchmem ./...

# Run specific benchmark
go test -bench=BenchmarkReadFile -benchmem ./...

# Run with CPU profiling
go test -bench=. -cpuprofile=cpu.prof ./...

# Run with memory profiling
go test -bench=. -memprofile=mem.prof ./...
```

## Detailed Performance Comparison

For full details, run the benchmark suite with a longer duration:

```bash
go test -bench=. -benchmem -benchtime=5s ./... | tee benchmark_results.txt
```

EOF

echo ""
echo -e "${GREEN}Performance report generated: $REPORT_FILE${NC}"
echo ""
echo -e "${BLUE}To view the full results, run:${NC}"
echo -e "${YELLOW}go test -bench=. -benchmem -benchtime=5s ./...${NC}"
echo ""
