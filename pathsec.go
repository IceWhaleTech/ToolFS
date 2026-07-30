package toolfs

// 路径安全基元（设计稿 §4.2 / §11.0 的 S1、S6）。
//
// 分工务必清楚：
//   - normalizeVirtualPath 只做【字符】规范化——反斜杠转斜杠、合并重复斜杠、
//     去 "./" 前缀与尾斜杠。它【不】清理 ".."，且被多处既有代码依赖，故保持原样。
//   - sanitizeVirtualPath 在其之上施加【语义】校验，拒绝一切可能改变路径归属的成分。
//
// S1 的根因正是二者被混用：判定走 normalizeVirtualPath（保留 ".."），
// 执行走 filepath.Join（会 clean 掉 ".."），同一个输入产生两个不同的路径值。
// 在入口处直接拒绝 ".." 之后，这个偏差在结构上不再可能出现。

import (
	"errors"
	"strings"
)

var (
	// ErrInvalidPath 表示路径含有非法字符或为空。
	ErrInvalidPath = errors.New("toolfs: invalid path")
	// ErrPathTraversal 表示路径含有 ".." 段，可能逃逸出挂载点。
	ErrPathTraversal = errors.New("toolfs: path traversal rejected")
	// ErrPathEscapesRoot 表示解析后的真实路径落在了挂载点之外。
	ErrPathEscapesRoot = errors.New("toolfs: path escapes mount root")
	// ErrReadOnlyMount 表示对只读挂载点的写入尝试。
	ErrReadOnlyMount = errors.New("toolfs: read-only mount")
)

// sanitizeVirtualPath 规范化并校验一个虚拟路径。
//
// 拒绝：空路径、含 NUL 字节、任何 ".." 路径段。
// 返回规范化后的路径，调用方应当【只】使用返回值继续处理，
// 以保证判定与执行落在同一个路径值上。
func sanitizeVirtualPath(path string) (string, error) {
	if path == "" {
		return "", ErrInvalidPath
	}
	if strings.IndexByte(path, 0) >= 0 {
		return "", ErrInvalidPath
	}

	normalized := normalizeVirtualPath(path)
	if normalized == "" {
		return "", ErrInvalidPath
	}

	// 按段检查，不能用 strings.Contains(path, "..")——那会误伤 "..foo" 这类合法名字。
	for _, seg := range strings.Split(normalized, "/") {
		if seg == ".." {
			return "", ErrPathTraversal
		}
	}

	return normalized, nil
}

// pathHasPrefix 按【路径段】判断 path 是否位于 prefix 之下（或恰好等于它）。
//
// 这替代了裸 strings.HasPrefix：后者会让 "/toolfs/database" 命中 "/toolfs/data"（S6）。
func pathHasPrefix(path, prefix string) bool {
	if prefix == "" {
		return false
	}
	if path == prefix {
		return true
	}

	// 归一尾斜杠，避免 "/a/" 与 "/a" 被当成不同前缀。
	trimmed := strings.TrimSuffix(prefix, "/")
	if trimmed == "" {
		// prefix 是根 "/"，任何绝对路径都在其下。
		return strings.HasPrefix(path, "/")
	}
	if path == trimmed {
		return true
	}
	if !strings.HasPrefix(path, trimmed) {
		return false
	}

	// 边界必须是分隔符，否则 "/toolfs/database" 会命中 "/toolfs/data"（S6）。
	// 除 "/" 外，"?" 也算边界：虚拟后端（rag/memory）把查询串直接编码在路径里，
	// 形如 "/toolfs/rag?text=x"，readRAG 正是按 "?" 切分的。
	//
	// 放宽到 "?" 不会带来逃逸风险：字符串匹配只决定【选中哪个挂载点】，
	// 真正的边界由 RootHandle（os.Root / openat2 RESOLVE_BENEATH）在内核层守住——
	// 即使某个真实文件名里含 "?"，读写也仍被约束在该挂载点之内。
	switch path[len(trimmed)] {
	case '/', '?':
		return true
	default:
		return false
	}
}

// relativeUnder 在 path 位于 prefix 之下时返回相对部分（不含前导斜杠）。
// path == prefix 时返回空串。第二个返回值表示是否命中。
func relativeUnder(path, prefix string) (string, bool) {
	if !pathHasPrefix(path, prefix) {
		return "", false
	}
	trimmed := strings.TrimSuffix(prefix, "/")
	if path == trimmed || path == prefix {
		return "", true
	}
	rel := strings.TrimPrefix(path, trimmed)
	// 只剥掉 "/"；"?" 属于相对部分本身（虚拟后端要拿它解析查询串）。
	return strings.TrimPrefix(rel, "/"), true
}
