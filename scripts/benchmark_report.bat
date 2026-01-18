@echo off
REM ToolFS 性能基准测试报告生成脚本 (Windows)
REM 运行所有基准测试并生成对比报告

setlocal enabledelayedexpansion

echo ========================================
echo ToolFS 性能基准测试报告生成器
echo ========================================
echo.

REM 检查 Go 是否安装
where go >nul 2>&1
if %errorlevel% neq 0 (
    echo 错误: 未找到 Go 编译器
    exit /b 1
)

set REPORT_FILE=BENCHMARK_REPORT.md
for /f "tokens=1-4 delims=/ " %%a in ('date /t') do set DATE_STR=%%a %%b %%c
for /f "tokens=1-2 delims=: " %%a in ('time /t') do set TIME_STR=%%a:%%b
set TIMESTAMP=%DATE_STR% %TIME_STR%

echo 运行基准测试...
echo.

REM 创建报告文件头部
(
    echo # ToolFS 性能基准测试报告
    echo.
    echo **生成时间**: %TIMESTAMP%
    echo.
    echo 本报告展示了 ToolFS 各主要功能的性能基准测试结果。
    echo.
    echo ## 测试环境
    echo.
    echo - **Go 版本**: 
    go version
    echo - **操作系统**: %OS%
    echo - **架构**: %PROCESSOR_ARCHITECTURE%
    echo.
    echo ## 性能测试结果
    echo.
    echo ### 文件操作性能
    echo.
) > %REPORT_FILE%

REM 运行文件操作基准测试
echo 测试文件读写性能...
go test -bench=^BenchmarkReadFile$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1
go test -bench=^BenchmarkReadFileLarge$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1
go test -bench=^BenchmarkWriteFile$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1
go test -bench=^BenchmarkListDir$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1

REM 运行内存操作基准测试
echo 测试记忆存储性能...
(
    echo.
    echo ### 记忆存储操作性能
    echo.
) >> %REPORT_FILE%
go test -bench=^BenchmarkMemoryWrite$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1
go test -bench=^BenchmarkMemoryRead$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1
go test -bench=^BenchmarkMemoryList$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1

REM 运行 RAG 搜索基准测试
echo 测试 RAG 搜索性能...
(
    echo.
    echo ### RAG 搜索性能
    echo.
) >> %REPORT_FILE%
go test -bench=^BenchmarkRAGSearch$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1
go test -bench=^BenchmarkRAGSearchWithTopK$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1

REM 运行快照操作基准测试
echo 测试快照操作性能...
(
    echo.
    echo ### 快照操作性能
    echo.
) >> %REPORT_FILE%
go test -bench=^BenchmarkSnapshotCreate$ -benchmem -benchtime=2s ./... >> %REPORT_FILE% 2>&1
go test -bench=^BenchmarkSnapshotRollback$ -benchmem -benchtime=2s ./... >> %REPORT_FILE% 2>&1

REM 运行其他操作基准测试
echo 测试其他操作性能...
(
    echo.
    echo ### 其他操作性能
    echo.
) >> %REPORT_FILE%
go test -bench=^BenchmarkPathResolution$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1
go test -bench=^BenchmarkPluginExecute$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1
go test -bench=^BenchmarkSessionAccessControl$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1
go test -bench=^BenchmarkConcurrentReads$ -benchmem -benchtime=3s ./... >> %REPORT_FILE% 2>&1

REM 添加总结部分
(
    echo.
    echo ## 性能总结
    echo.
    echo ### 文件操作性能
    echo.
    echo - **文件读取**: 测试了不同大小文件的读取性能
    echo - **文件写入**: 测试了文件写入操作的性能
    echo - **目录列表**: 测试了目录遍历和列表操作的性能
    echo.
    echo ### 记忆存储性能
    echo.
    echo - **写入性能**: 测试了记忆条目的创建和更新性能
    echo - **读取性能**: 测试了记忆条目的检索性能
    echo - **列表性能**: 测试了记忆条目列表的性能
    echo.
    echo ### RAG 搜索性能
    echo.
    echo - **搜索性能**: 测试了语义搜索的响应时间
    echo - **TopK 影响**: 测试了不同 top_k 参数对性能的影响
    echo.
    echo ### 快照操作性能
    echo.
    echo - **创建性能**: 测试了快照创建的性能开销
    echo - **回滚性能**: 测试了快照恢复的性能开销
    echo.
    echo ### 并发性能
    echo.
    echo - **并发读取**: 测试了多协程并发读取的性能
    echo - **会话隔离**: 测试了会话访问控制的开销
    echo.
    echo ## 优化建议
    echo.
    echo 1. **大文件操作**: 对于大文件操作，考虑使用流式处理
    echo 2. **内存使用**: 大量记忆条目时考虑分页或索引
    echo 3. **RAG 搜索**: 对于大型向量数据库，使用向量索引优化
    echo 4. **快照操作**: 使用增量快照减少创建和恢复时间
    echo 5. **并发优化**: 考虑使用读写锁优化并发访问
    echo.
) >> %REPORT_FILE%

echo.
echo 性能报告已生成: %REPORT_FILE%
echo.
echo 要查看完整基准测试结果，请运行:
echo go test -bench=. -benchmem -benchtime=5s ./...
echo.



