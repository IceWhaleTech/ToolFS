@echo off
REM ToolFS Performance Benchmark Report Generator (Windows)
REM Runs all benchmarks and generates a comprehensive report

setlocal enabledelayedexpansion

echo ========================================
echo ToolFS Performance Benchmark Generator
echo ========================================
echo.

REM Check if Go is installed
where go >nul 2>&1
if %errorlevel% neq 0 (
    echo Error: Go compiler not found
    exit /b 1
)

set REPORT_FILE=BENCHMARK_REPORT.md
for /f "tokens=1-4 delims=/ " %%a in ('date /t') do set DATE_STR=%%a %%b %%c
for /f "tokens=1-2 delims=: " %%a in ('time /t') do set TIME_STR=%%a:%%b
set TIMESTAMP=%DATE_STR% %TIME_STR%

echo Running benchmarks...
echo.

REM Create report file header
(
    echo # ToolFS Performance Benchmark Report
    echo.
    echo **Generated At**: %TIMESTAMP%
    echo.
    echo This report presents the performance benchmark results for the main features of ToolFS.
    echo.
    echo ## Test Environment
    echo.
    echo - **Hardware**: Apple M4 Pro
    echo - **Go Version**: 
    go version
    echo - **OS**: %OS%
    echo - **Architecture**: %PROCESSOR_ARCHITECTURE%
    echo.
    echo ## Performance Results
    echo.
    echo ### File Operations Performance
    echo.
) > %REPORT_FILE%

REM Run file operation benchmarks
echo Testing file read/write performance...
CGO_ENABLED=0 go test -bench=^BenchmarkReadFile$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1
CGO_ENABLED=0 go test -bench=^BenchmarkReadFileLarge$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1
CGO_ENABLED=0 go test -bench=^BenchmarkWriteFile$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1
CGO_ENABLED=0 go test -bench=^BenchmarkListDir$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1

REM Run memory operation benchmarks
echo Testing memory store performance...
(
    echo.
    echo ### Memory Store Performance
    echo.
) >> %REPORT_FILE%
CGO_ENABLED=0 go test -bench=^BenchmarkMemoryWrite$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1
CGO_ENABLED=0 go test -bench=^BenchmarkMemoryRead$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1
CGO_ENABLED=0 go test -bench=^BenchmarkMemoryList$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1

REM Run RAG search benchmarks
echo Testing RAG search performance...
(
    echo.
    echo ### RAG Search Performance
    echo.
) >> %REPORT_FILE%
CGO_ENABLED=0 go test -bench=^BenchmarkRAGSearch$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1
CGO_ENABLED=0 go test -bench=^BenchmarkRAGSearchWithTopK$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1

REM Run snapshot operation benchmarks
echo Testing snapshot operation performance...
(
    echo.
    echo ### Snapshot Performance
    echo.
) >> %REPORT_FILE%
CGO_ENABLED=0 go test -bench=^BenchmarkSnapshotCreate$ -benchmem -benchtime=2s ./... >> %REPORT_FILE% 2>&1
CGO_ENABLED=0 go test -bench=^BenchmarkSnapshotRollback$ -benchmem -benchtime=2s ./... >> %REPORT_FILE% 2>&1

REM Run other operation benchmarks
echo Testing other operations performance...
(
    echo.
    echo ### Other Operations Performance
    echo.
) >> %REPORT_FILE%
CGO_ENABLED=0 go test -bench=^BenchmarkPathResolution$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1
CGO_ENABLED=0 go test -bench=^BenchmarkSkillExecute$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1
CGO_ENABLED=0 go test -bench=^BenchmarkSessionAccessControl$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1
CGO_ENABLED=0 go test -bench=^BenchmarkConcurrentReads$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1

REM Add summary section
(
    echo.
    echo ## Performance Summary
    echo.
    echo ### File Operations
    echo.
    echo - **File Read**: Benchmarked reading files of various sizes.
    echo - **File Write**: Benchmarked file creation and writing operations.
    echo - **Directory List**: Benchmarked directory traversal and listing.
    echo.
    echo ### Memory Store
    echo.
    echo - **Write Performance**: Benchmarked creating and updating memory entries.
    echo - **Read Performance**: Benchmarked retrieval of memory entries.
    echo - **List Performance**: Benchmarked listing memory entries.
    echo.
    echo ### RAG Search
    echo.
    echo - **Search Performance**: Benchmarked response time for semantic search queries.
    echo - **TopK Impact**: Benchmarked performance with different top_k parameters.
    echo.
    echo ### Snapshot Operations
    echo.
    echo - **Creation Performance**: Benchmarked overhead of creating snapshots.
    echo - **Rollback Performance**: Benchmarked overhead of restoring state from snapshots.
    echo.
    echo ### Concurrency
    echo.
    echo - **Concurrent Reads**: Benchmarked performance under multi-threaded read access.
    echo - **Session Isolation**: Benchmarked overhead of session-based access control.
    echo.
    echo ## Optimization Recommendations
    echo.
    echo 1. **Large File Operations**: Consider streaming for large file processing.
    echo 2. **Memory Usage**: For massive amounts of memory entries, consider indexing or pagination.
    echo 3. **RAG Search**: Optimize with vector indexing for large-scale databases.
    echo 4. **Snapshot Operations**: Use incremental snapshots to reduce time and storage.
    echo 5. **Concurrency**: Use refined locking mechanisms (e.g., RWMutex) to optimize parallel access.
    echo.
) >> %REPORT_FILE%

echo.
echo Performance report generated: %REPORT_FILE%
echo.
echo To view the full results, run:
echo go test -bench=. -benchmem -benchtime=5s ./...
echo.
