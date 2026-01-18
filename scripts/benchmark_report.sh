#!/bin/bash

# ToolFS 性能基准测试报告生成脚本
# 运行所有基准测试并生成对比报告

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 报告文件
REPORT_FILE="BENCHMARK_REPORT.md"
TIMESTAMP=$(date '+%Y-%m-%d %H:%M:%S')

echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}ToolFS 性能基准测试报告生成器${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""

# 检查 Go 是否安装
if ! command -v go &> /dev/null; then
    echo -e "${RED}错误: 未找到 Go 编译器${NC}"
    exit 1
fi

echo -e "${GREEN}运行基准测试...${NC}"
echo ""

# 创建报告文件头部
cat > "$REPORT_FILE" << EOF
# ToolFS 性能基准测试报告

**生成时间**: $TIMESTAMP

本报告展示了 ToolFS 各主要功能的性能基准测试结果。

## 测试环境

- **Go 版本**: $(go version)
- **操作系统**: $(uname -s) $(uname -r)
- **架构**: $(uname -m)

## 性能测试结果

### 文件操作性能

EOF

# 运行文件操作基准测试
echo -e "${YELLOW}测试文件读写性能...${NC}"
{
    echo "#### 文件读取性能"
    echo ""
    echo '```'
    go test -bench=^BenchmarkReadFile$ -benchmem -benchtime=3s ./... | tee -a /dev/stderr | grep -E "(Benchmark|PASS|ok)"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

{
    echo "#### 大文件读取性能"
    echo ""
    echo '```'
    go test -bench=^BenchmarkReadFileLarge$ -benchmem -benchtime=3s ./... | tee -a /dev/stderr | grep -E "(Benchmark|PASS|ok)"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

{
    echo "#### 文件写入性能"
    echo ""
    echo '```'
    go test -bench=^BenchmarkWriteFile$ -benchmem -benchtime=3s ./... | tee -a /dev/stderr | grep -E "(Benchmark|PASS|ok)"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

{
    echo "#### 目录列表性能"
    echo ""
    echo '```'
    go test -bench=^BenchmarkListDir$ -benchmem -benchtime=3s ./... | tee -a /dev/stderr | grep -E "(Benchmark|PASS|ok)"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

# 运行内存操作基准测试
echo -e "${YELLOW}测试记忆存储性能...${NC}"
{
    echo "### 记忆存储操作性能"
    echo ""
    echo "#### 记忆写入性能"
    echo ""
    echo '```'
    go test -bench=^BenchmarkMemoryWrite$ -benchmem -benchtime=3s ./... | tee -a /dev/stderr | grep -E "(Benchmark|PASS|ok)"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

{
    echo "#### 记忆读取性能"
    echo ""
    echo '```'
    go test -bench=^BenchmarkMemoryRead$ -benchmem -benchtime=3s ./... | tee -a /dev/stderr | grep -E "(Benchmark|PASS|ok)"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

{
    echo "#### 记忆列表性能"
    echo ""
    echo '```'
    go test -bench=^BenchmarkMemoryList$ -benchmem -benchtime=3s ./... | tee -a /dev/stderr | grep -E "(Benchmark|PASS|ok)"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

# 运行 RAG 搜索基准测试
echo -e "${YELLOW}测试 RAG 搜索性能...${NC}"
{
    echo "### RAG 搜索性能"
    echo ""
    echo "#### RAG 搜索性能"
    echo ""
    echo '```'
    go test -bench=^BenchmarkRAGSearch$ -benchmem -benchtime=3s ./... | tee -a /dev/stderr | grep -E "(Benchmark|PASS|ok)"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

{
    echo "#### RAG 搜索 (不同 TopK) 性能"
    echo ""
    echo '```'
    go test -bench=^BenchmarkRAGSearchWithTopK$ -benchmem -benchtime=3s ./... | tee -a /dev/stderr | grep -E "(Benchmark|PASS|ok)"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

# 运行快照操作基准测试
echo -e "${YELLOW}测试快照操作性能...${NC}"
{
    echo "### 快照操作性能"
    echo ""
    echo "#### 快照创建性能"
    echo ""
    echo '```'
    go test -bench=^BenchmarkSnapshotCreate$ -benchmem -benchtime=2s ./... | tee -a /dev/stderr | grep -E "(Benchmark|PASS|ok)"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

{
    echo "#### 快照回滚性能"
    echo ""
    echo '```'
    go test -bench=^BenchmarkSnapshotRollback$ -benchmem -benchtime=2s ./... | tee -a /dev/stderr | grep -E "(Benchmark|PASS|ok)"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

# 运行其他操作基准测试
echo -e "${YELLOW}测试其他操作性能...${NC}"
{
    echo "### 其他操作性能"
    echo ""
    echo "#### 路径解析性能"
    echo ""
    echo '```'
    go test -bench=^BenchmarkPathResolution$ -benchmem -benchtime=3s ./... | tee -a /dev/stderr | grep -E "(Benchmark|PASS|ok)"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

{
    echo "#### 插件执行性能"
    echo ""
    echo '```'
    go test -bench=^BenchmarkPluginExecute$ -benchmem -benchtime=3s ./... | tee -a /dev/stderr | grep -E "(Benchmark|PASS|ok)"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

{
    echo "#### 会话访问控制性能"
    echo ""
    echo '```'
    go test -bench=^BenchmarkSessionAccessControl$ -benchmem -benchtime=3s ./... | tee -a /dev/stderr | grep -E "(Benchmark|PASS|ok)"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

{
    echo "#### 并发读取性能"
    echo ""
    echo '```'
    go test -bench=^BenchmarkConcurrentReads$ -benchmem -benchtime=3s ./... | tee -a /dev/stderr | grep -E "(Benchmark|PASS|ok)"
    echo '```'
    echo ""
} >> "$REPORT_FILE" 2>&1

# 添加总结部分
cat >> "$REPORT_FILE" << 'EOF'

## 性能总结

### 文件操作性能

- **文件读取**: 测试了不同大小文件的读取性能
- **文件写入**: 测试了文件写入操作的性能
- **目录列表**: 测试了目录遍历和列表操作的性能

### 记忆存储性能

- **写入性能**: 测试了记忆条目的创建和更新性能
- **读取性能**: 测试了记忆条目的检索性能
- **列表性能**: 测试了记忆条目列表的性能

### RAG 搜索性能

- **搜索性能**: 测试了语义搜索的响应时间
- **TopK 影响**: 测试了不同 top_k 参数对性能的影响

### 快照操作性能

- **创建性能**: 测试了快照创建的性能开销
- **回滚性能**: 测试了快照恢复的性能开销

### 并发性能

- **并发读取**: 测试了多协程并发读取的性能
- **会话隔离**: 测试了会话访问控制的开销

## 优化建议

1. **大文件操作**: 对于大文件操作，考虑使用流式处理
2. **内存使用**: 大量记忆条目时考虑分页或索引
3. **RAG 搜索**: 对于大型向量数据库，使用向量索引优化
4. **快照操作**: 使用增量快照减少创建和恢复时间
5. **并发优化**: 考虑使用读写锁优化并发访问

## 运行基准测试

要单独运行某个基准测试：

```bash
# 运行所有基准测试
go test -bench=. -benchmem ./...

# 运行特定基准测试
go test -bench=BenchmarkReadFile -benchmem ./...

# 运行基准测试并生成 CPU 性能分析
go test -bench=. -cpuprofile=cpu.prof ./...

# 运行基准测试并生成内存性能分析
go test -bench=. -memprofile=mem.prof ./...
```

## 性能对比

运行完整基准测试套件以获取详细的性能对比：

```bash
go test -bench=. -benchmem -benchtime=5s ./... | tee benchmark_results.txt
```

EOF

echo ""
echo -e "${GREEN}性能报告已生成: $REPORT_FILE${NC}"
echo ""
echo -e "${BLUE}要查看完整基准测试结果，请运行:${NC}"
echo -e "${YELLOW}go test -bench=. -benchmem -benchtime=5s ./...${NC}"
echo ""



