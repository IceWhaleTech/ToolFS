package toolfs

// S1–S6 安全回归测试。
//
// 这些用例对应设计稿 §11.0 记录的六个已实测漏洞。它们必须先红后绿：
// 在 P0 修复前应当全部失败，修复后全部通过，且此后永久防止回归。
//
// 根因（S1/S2/S3 共享）：normalizeVirtualPath 只做字符规范化、不清理 "..",
// 而 resolvePath 用 filepath.Join（会 clean）——判定与执行用了两个不同的路径值。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// newSecurityFixture 搭建一个「挂载内 + 挂载外」的目录布局：
//
//	<tmp>/mount/data.txt    挂载点内的正常文件
//	<tmp>/outside/secret.txt 挂载点外的敏感文件，任何路径都不应读到
func newSecurityFixture(t *testing.T) (fs *ToolFS, mountDir, outsideDir string) {
	t.Helper()

	base := t.TempDir()
	mountDir = filepath.Join(base, "mount")
	outsideDir = filepath.Join(base, "outside")

	for _, d := range []string{mountDir, outsideDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("准备目录 %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(mountDir, "data.txt"), []byte("PUBLIC"), 0o644); err != nil {
		t.Fatalf("写挂载内文件: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("SECRET"), 0o644); err != nil {
		t.Fatalf("写挂载外文件: %v", err)
	}

	fs = NewToolFS("/toolfs")
	if err := fs.MountLocal("/data", mountDir, false); err != nil {
		t.Fatalf("MountLocal: %v", err)
	}
	return fs, mountDir, outsideDir
}

// S1: 路径穿越绕过访问控制。
// "/toolfs/data/../../outside/secret.txt" 在判定阶段以 "/toolfs/data" 为前缀而被放行，
// 执行阶段 filepath.Join 清理掉 ".." 后落到挂载点之外。
func TestS1_PathTraversalIsRejected(t *testing.T) {
	fs, _, _ := newSecurityFixture(t)

	traversals := []string{
		"/toolfs/data/../../outside/secret.txt",
		"/toolfs/data/../outside/secret.txt",
		"/toolfs/data/subdir/../../../outside/secret.txt",
		"/toolfs/data/./../../outside/secret.txt",
	}

	for _, p := range traversals {
		t.Run(p, func(t *testing.T) {
			// 判定层必须拒绝
			session := NewSession("s1", []string{"/toolfs/data"})
			if session.IsPathAllowed(p) {
				t.Errorf("IsPathAllowed(%q) = true，应为 false（路径穿越被判定为合法）", p)
			}

			// 执行层必须拒绝，且绝不能读到挂载点外的内容
			data, err := fs.ReadFile(p)
			if err == nil {
				t.Errorf("ReadFile(%q) 意外成功，读到 %q", p, string(data))
			}
			if strings.Contains(string(data), "SECRET") {
				t.Errorf("ReadFile(%q) 读到了挂载点外的敏感内容", p)
			}
		})
	}
}

// S2: 符号链接逃逸。挂载点内的软链指向挂载点外，读取时被直接跟随。
func TestS2_SymlinkEscapeIsRejected(t *testing.T) {
	fs, mountDir, outsideDir := newSecurityFixture(t)

	link := filepath.Join(mountDir, "escape.txt")
	if err := os.Symlink(filepath.Join(outsideDir, "secret.txt"), link); err != nil {
		t.Skipf("当前环境不支持创建符号链接: %v", err)
	}

	data, err := fs.ReadFile("/toolfs/data/escape.txt")
	if err == nil && strings.Contains(string(data), "SECRET") {
		t.Errorf("经软链读到了挂载点外的内容 %q，符号链接逃逸未被阻止", string(data))
	}
}

// S3: 只读挂载被绕过写入。从可写挂载经 ".." 落到只读挂载的本地目录。
func TestS3_ReadOnlyMountCannotBeBypassed(t *testing.T) {
	base := t.TempDir()
	rwDir := filepath.Join(base, "rw")
	roDir := filepath.Join(base, "ro")
	for _, d := range []string{rwDir, roDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("准备目录: %v", err)
		}
	}
	target := filepath.Join(roDir, "protected.txt")
	if err := os.WriteFile(target, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatalf("写只读区文件: %v", err)
	}

	fs := NewToolFS("/toolfs")
	if err := fs.MountLocal("/rw", rwDir, false); err != nil {
		t.Fatalf("挂载可写区: %v", err)
	}
	if err := fs.MountLocal("/ro", roDir, true); err != nil {
		t.Fatalf("挂载只读区: %v", err)
	}

	// 直接写只读挂载必须被拒
	if err := fs.WriteFile("/toolfs/ro/protected.txt", []byte("HACKED")); err == nil {
		t.Error("直接写只读挂载意外成功")
	}

	// 经可写挂载穿越到只读挂载的本地目录，同样必须被拒
	_ = fs.WriteFile("/toolfs/rw/../ro/protected.txt", []byte("HACKED"))

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("回读只读区文件: %v", err)
	}
	if string(got) != "ORIGINAL" {
		t.Errorf("只读挂载的文件被改写为 %q，只读保护被绕过", string(got))
	}
}

// S4: 并发竞态。mounts 的写入与 resolvePath 的读取并发。
// 需配合 -race 运行才有意义。
func TestS4_ConcurrentMountAndResolveIsRaceFree(t *testing.T) {
	base := t.TempDir()
	fs := NewToolFS("/toolfs")

	dirs := make([]string, 8)
	for i := range dirs {
		d := filepath.Join(base, "m", string(rune('a'+i)))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("准备目录: %v", err)
		}
		dirs[i] = d
	}

	var wg sync.WaitGroup
	for i, d := range dirs {
		wg.Add(1)
		go func(i int, d string) {
			defer wg.Done()
			_ = fs.MountLocal("/m"+string(rune('a'+i)), d, false)
		}(i, d)
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = fs.ListDir("/toolfs/m" + string(rune('a'+i)))
			_, _ = fs.Stat("/toolfs/m" + string(rune('a'+i)))
		}(i)
	}
	wg.Wait()
}

// S5: sandbox 为捕获技能输出而改写进程全局 os.Stdout，
// 与审计日志经 fmt.Println 读取 os.Stdout 并发冲突。
//
// 原始 race 的两端：
//
//	写 —— InMemorySandbox.Execute 内的 os.Stdout = w（sandbox.go）
//	读 —— StdoutAuditLogger.Log 的 fmt.Println（读全局 os.Stdout）
//
// 本用例同时驱动两端，必须配合 -race 运行才有意义。
func TestS5_SandboxStdoutCaptureRacesNoLongerOccur(t *testing.T) {
	before := os.Stdout

	sandbox := NewInMemorySandbox()
	spm := NewSandboxedSkillManager(sandbox)

	fs := NewToolFS("/toolfs")
	session, err := fs.NewSession("s5-sandbox", []string{})
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	// 审计写到测试自己的缓冲区，避免污染测试输出
	var auditBuf syncBuffer
	session.SetAuditLogger(NewAuditLoggerTo(&auditBuf))
	skillCtx := NewSkillContext(fs, session)

	spm.InjectSkill(&StdoutStderrSkill{}, skillCtx, nil)
	config := DefaultSandboxConfig()
	config.CaptureStdout = true
	config.CaptureStderr = true
	spm.SetSandboxConfig("stdio-skill", config)

	input, err := json.Marshal(&SkillRequest{Operation: "test"})
	if err != nil {
		t.Fatalf("编码请求: %v", err)
	}

	var wg sync.WaitGroup

	// 一端：反复执行会捕获 stdout 的技能（写全局 os.Stdout）
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = spm.ExecuteSkillSandboxed("stdio-skill", input, skillCtx)
		}()
	}

	// 另一端：并发写审计日志（原实现会读全局 os.Stdout）
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session.logAudit("ReadFile", "/toolfs/x", true, nil, 1, 0)
		}()
	}

	wg.Wait()

	if os.Stdout != before {
		t.Error("os.Stdout 在全部执行结束后未被还原，进程全局状态被留在了劫持态")
	}
}

// syncBuffer 是并发安全的 io.Writer，供审计器在并发测试中使用。
type syncBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

// S6: 挂载点前缀边界错误。"/toolfs/data" 不应命中 "/toolfs/database"。
func TestS6_MountPrefixIsSegmentAware(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	dbDir := filepath.Join(base, "database")
	for _, d := range []string{dataDir, dbDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("准备目录: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dbDir, "db.txt"), []byte("DATABASE"), 0o644); err != nil {
		t.Fatalf("写文件: %v", err)
	}

	fs := NewToolFS("/toolfs")
	if err := fs.MountLocal("/data", dataDir, false); err != nil {
		t.Fatalf("挂载 /data: %v", err)
	}
	if err := fs.MountLocal("/database", dbDir, false); err != nil {
		t.Fatalf("挂载 /database: %v", err)
	}

	// 会话仅授权 /toolfs/data，不得因字符串前缀而命中 /toolfs/database
	session := NewSession("s6", []string{"/toolfs/data"})
	if session.IsPathAllowed("/toolfs/database/db.txt") {
		t.Error("IsPathAllowed 把 /toolfs/database 当成了 /toolfs/data 的子路径（字符串前缀匹配）")
	}

	// 解析层也必须按路径段区分两个挂载点
	data, err := fs.ReadFile("/toolfs/database/db.txt")
	if err != nil {
		t.Fatalf("读 /toolfs/database/db.txt 应当成功: %v", err)
	}
	if string(data) != "DATABASE" {
		t.Errorf("读到 %q，期望 DATABASE —— 挂载点解析串到了 /data", string(data))
	}
}

// 拒绝路径必须记为 success:false，而不是记成成功。
func TestDeniedAccessIsAuditedAsFailure(t *testing.T) {
	fs, _, _ := newSecurityFixture(t)

	rec := &recordingAuditLogger{}
	session := NewSession("audit", []string{"/toolfs/data"})
	session.SetAuditLogger(rec)

	_, _ = fs.ReadFileWithSession("/toolfs/data/../../outside/secret.txt", session)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.entries) == 0 {
		t.Fatal("被拒绝的访问没有产生任何审计记录")
	}
	for _, e := range rec.entries {
		if e.Success {
			t.Errorf("被拒绝的访问被记为 success:true（path=%s op=%s）", e.Path, e.Operation)
		}
	}
}

type recordingAuditLogger struct {
	mu      sync.Mutex
	entries []AuditLogEntry
}

func (l *recordingAuditLogger) Log(entry AuditLogEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
	return nil
}
