package pyop

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// extFake 在 stubExecutor 之上实现 ExtManager，记录调用参数并返回预设结果。
type extFake struct {
	*stubExecutor
	installPrefix string
	installPkgs   []string
	installResult ExtInstallResult
	installErr    error
	installCalls  int

	listPrefix string
	listResult ExtListResult
	listErr    error
	listCalls  int

	removePrefix string
	removePkgs   []string
	removeResult ExtRemoveResult
	removeErr    error
	removeCalls  int
}

func (f *extFake) InstallPackages(prefix string, packages []string) (ExtInstallResult, error) {
	f.installCalls++
	f.installPrefix, f.installPkgs = prefix, packages
	return f.installResult, f.installErr
}

func (f *extFake) ListPackages(prefix string) (ExtListResult, error) {
	f.listCalls++
	f.listPrefix = prefix
	return f.listResult, f.listErr
}

func (f *extFake) RemovePackages(prefix string, packages []string) (ExtRemoveResult, error) {
	f.removeCalls++
	f.removePrefix, f.removePkgs = prefix, packages
	return f.removeResult, f.removeErr
}

func mustCallExt[T any](t *testing.T, method, params, root string, exec Executor) T {
	t.Helper()
	raw, perr := Call(method, json.RawMessage(params), root, exec)
	if perr != nil {
		t.Fatalf("%s 失败: %+v", method, perr)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s 结果解析失败: %v（%s）", method, err, raw)
	}
	return out
}

// TestExtInstallPrefixIsolated 校验 install/list/remove 的前缀固定 <state>/global/py_ext，
// 不接受调用方传路径，且与宿主 site-packages 无关。
func TestExtInstallPrefixIsolated(t *testing.T) {
	root := t.TempDir()
	fake := &extFake{}
	want := filepath.Join(root, DefaultGlobalSubdir, pyExtSubdir)
	result := mustCallExt[ExtInstallResult](t, "runtime-python.ext.install",
		`{"packages":["requests==2.31.0"]}`, root, fake)
	if result.Prefix != want || fake.installPrefix != want {
		t.Fatalf("隔离前缀不对: result=%s fake=%s want=%s", result.Prefix, fake.installPrefix, want)
	}
	if !result.NetworkUsed || len(result.Packages) != 1 || result.Packages[0] != "requests==2.31.0" {
		t.Fatalf("安装结果字段不对: %+v", result)
	}
	list := mustCallExt[ExtListResult](t, "runtime-python.ext.list", `{}`, root, fake)
	if list.Prefix != want || fake.listPrefix != want {
		t.Fatalf("list 前缀不对: result=%s fake=%s", list.Prefix, fake.listPrefix)
	}
	_ = mustCallExt[ExtRemoveResult](t, "runtime-python.ext.remove",
		`{"packages":["requests"]}`, root, fake)
	if fake.removePrefix != want {
		t.Fatalf("remove 前缀不对: %s", fake.removePrefix)
	}
	if !strings.HasSuffix(want, pyExtSubdir) {
		t.Fatalf("前缀应指向隔离 py_ext 目录: %s", want)
	}
}

// TestExtRejectsInjection 校验包名白名单（复用 venv.go 正则）挡选项/注入/路径，空列表与未知字段拒绝。
func TestExtRejectsInjection(t *testing.T) {
	root := t.TempDir()
	fake := &extFake{}
	bad := []string{
		"-g", "--user", "--break-system-packages", "--index-url=http://evil.example/simple",
		"pkg; rm -rf /", "pkg extra", "pkg$(whoami)", "pkg`id`", "../evil", "/etc/passwd", "",
	}
	for _, pkg := range bad {
		payload, _ := json.Marshal(map[string]any{"packages": []string{pkg}})
		_, perr := Call("runtime-python.ext.install", payload, root, fake)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("包名 %q 期望 E_INVALID，得到 %+v", pkg, perr)
		}
	}
	_, perr := Call("runtime-python.ext.install", json.RawMessage(`{}`), root, fake)
	if perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("空 packages 期望 E_INVALID，得到 %+v", perr)
	}
	if fake.installCalls != 0 {
		t.Fatalf("参数校验失败仍触达执行面: %d", fake.installCalls)
	}
	// 未知字段（prefix / venv_path）必须拒绝，防止调用方绕过固定前缀。
	for _, params := range []string{
		`{"packages":["requests"],"prefix":"/etc"}`,
		`{"packages":["requests"],"venv_path":"/etc"}`,
	} {
		_, perr = Call("runtime-python.ext.install", json.RawMessage(params), root, fake)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("未知字段参数 %s 期望 E_INVALID，得到 %+v", params, perr)
		}
	}
}

// TestExtPrefixRejectsEscape 校验四类坏 root + <state>/global 软链指向外部 → E_INVALID。
func TestExtPrefixRejectsEscape(t *testing.T) {
	fake := &extFake{}
	for _, root := range []string{"/tmp/../etc", "relative/state", " /tmp/state", "/tmp/state ", ""} {
		_, perr := Call("runtime-python.ext.list", json.RawMessage(`{}`), root, fake)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("stateRoot=%q 期望 E_INVALID，得到 %+v", root, perr)
		}
	}
	// <state>/global 软链指向状态目录之外：真实层校验必须拒绝。
	state := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(state, DefaultGlobalSubdir)); err != nil {
		t.Skipf("本环境不支持符号链接: %v", err)
	}
	_, perr := Call("runtime-python.ext.list", json.RawMessage(`{}`), state, fake)
	if perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("global 软链逃逸期望 E_INVALID，得到 %+v", perr)
	}
	// <state>/global/py_ext 软链指向外部也必须拒绝。
	state2 := t.TempDir()
	global2 := filepath.Join(state2, DefaultGlobalSubdir)
	if err := os.MkdirAll(global2, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(global2, pyExtSubdir)); err != nil {
		t.Skipf("本环境不支持符号链接: %v", err)
	}
	_, perr = Call("runtime-python.ext.install", json.RawMessage(`{"packages":["requests"]}`), state2, fake)
	if perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("py_ext 软链逃逸期望 E_INVALID，得到 %+v", perr)
	}
	if fake.installCalls != 0 {
		t.Fatalf("软链逃逸仍触达执行面: %d", fake.installCalls)
	}
}

// TestExtTruncateOutput 校验 64KiB 首尾截断与标记（复用 venv.go truncation）。
func TestExtTruncateOutput(t *testing.T) {
	raw := strings.Repeat("a", MaxPipOutputBytes+100)
	truncated, ok := truncatePipOutput(raw)
	if !ok || !strings.Contains(truncated, "输出截断") || len(truncated) >= len(raw) {
		t.Fatalf("截断逻辑异常: ok=%v len=%d", ok, len(truncated))
	}
	fake := &extFake{installResult: ExtInstallResult{Output: raw}}
	result := mustCallExt[ExtInstallResult](t, "runtime-python.ext.install",
		`{"packages":["requests"]}`, t.TempDir(), fake)
	if !result.Truncated || len(result.Output) >= len(raw) || !strings.Contains(result.Output, "截断") {
		t.Fatalf("安装输出应截断: truncated=%v len=%d", result.Truncated, len(result.Output))
	}
}

// TestExtRealManagerArgv 校验真实 install 固定 argv：python3 -m pip install --target <prefix>，
// 包名末尾透传，无 --user / --break-system-packages / shell 选项。
func TestExtRealManagerArgv(t *testing.T) {
	var captured []string
	mgr := realExtManager{runner: func(timeout time.Duration, name string, args ...string) (string, error) {
		if name != "python3" || timeout != VenvCommandTimeout {
			t.Fatalf("固定 argv 异常: name=%s timeout=%s", name, timeout)
		}
		captured = append([]string{}, args...)
		return "ok", nil
	}}
	prefix := filepath.Join(t.TempDir(), DefaultGlobalSubdir, pyExtSubdir)
	if _, err := mgr.InstallPackages(prefix, []string{"requests==2.31.0", "Flask"}); err != nil {
		t.Fatalf("install 失败: %v", err)
	}
	joined := strings.Join(captured, " ")
	if !strings.HasPrefix(joined, "-m pip install --target "+prefix+" --no-input --disable-pip-version-check ") {
		t.Fatalf("固定 argv 前缀不对: %s", joined)
	}
	for _, forbidden := range []string{"--user", "--break-system-packages", "sh -c", "-g"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("真实执行面出现禁止选项 %s: %s", forbidden, joined)
		}
	}
	if captured[len(captured)-2] != "requests==2.31.0" || captured[len(captured)-1] != "Flask" {
		t.Fatalf("包名未按白名单透传: %v", captured)
	}
	// remove 无 exec：runner 注入后不应被调用（固定 argv 要素 N/A，理由见 §2.2）。
	called := false
	mgr2 := realExtManager{runner: func(timeout time.Duration, name string, args ...string) (string, error) {
		called = true
		return "", nil
	}}
	if _, err := mgr2.RemovePackages(filepath.Join(t.TempDir(), pyExtSubdir), []string{"requests"}); err != nil {
		t.Fatalf("remove 失败: %v", err)
	}
	if called {
		t.Fatal("remove 不应起子进程（pip uninstall 无 --target）")
	}
}

// TestExtClassifyTimeout 校验 ErrExtTimeout → E_UPSTREAM。
func TestExtClassifyTimeout(t *testing.T) {
	fake := &extFake{installErr: ErrExtTimeout}
	_, perr := Call("runtime-python.ext.install", json.RawMessage(`{"packages":["requests"]}`), t.TempDir(), fake)
	if perr == nil || perr.Code != pluginapi.E_UPSTREAM {
		t.Fatalf("超时期望 E_UPSTREAM，得到 %+v", perr)
	}
	// 真实执行面超时：runner 直接返回 ErrExtTimeout，Call 层通过 classify 映射。
	mgr := realExtManager{runner: func(timeout time.Duration, name string, args ...string) (string, error) {
		return "", ErrExtTimeout
	}}
	if _, err := mgr.InstallPackages(t.TempDir(), []string{"requests"}); !errors.Is(err, ErrExtTimeout) {
		t.Fatalf("runner 超时错误未透传: %v", err)
	}
}

// TestExtListReadsDistInfo 校验 list 只读 *.dist-info/METADATA，解析 Name/Version，排序与空态。
func TestExtListReadsDistInfo(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), DefaultGlobalSubdir, pyExtSubdir)
	if err := os.MkdirAll(filepath.Join(prefix, "requests-2.31.0.dist-info"), 0o700); err != nil {
		t.Fatal(err)
	}
	meta := "Metadata-Version: 2.1\nName: requests\nVersion: 2.31.0\nSummary: HTTP library\n"
	if err := os.WriteFile(filepath.Join(prefix, "requests-2.31.0.dist-info", "METADATA"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	// 一个无 METADATA 的 egg-info 兜底目录：用目录名剥版本。
	if err := os.MkdirAll(filepath.Join(prefix, "Flask-3.0.0.egg-info"), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := realExtManager{}.ListPackages(prefix)
	if err != nil {
		t.Fatalf("list 失败: %v", err)
	}
	if len(result.Packages) != 2 {
		t.Fatalf("应列出两个扩展: %+v", result.Packages)
	}
	if result.Packages[0].Name != "Flask" || result.Packages[0].Version != "3.0.0" {
		t.Fatalf("egg-info 兜底解析不对: %+v", result.Packages[0])
	}
	if result.Packages[1].Name != "requests" || result.Packages[1].Version != "2.31.0" {
		t.Fatalf("METADATA 解析不对: %+v", result.Packages[1])
	}
	// 空前缀（目录不存在）→ 空数组 + ErrExtPrefixMissing，Call 层吞为空态而非报错。
	emptyRoot := t.TempDir()
	emptyPrefix := filepath.Join(emptyRoot, DefaultGlobalSubdir, pyExtSubdir)
	if _, err := (realExtManager{}).ListPackages(emptyPrefix); !errors.Is(err, ErrExtPrefixMissing) {
		t.Fatalf("空前缀应返回 ErrExtPrefixMissing，得到 %v", err)
	}
	fake := &extFake{}
	list := mustCallExt[ExtListResult](t, "runtime-python.ext.list", `{}`, emptyRoot, fake)
	if list.Count != 0 || list.Packages == nil || !strings.Contains(list.Hint, "管理扩展") {
		t.Fatalf("空态列表不对: %+v", list)
	}
}

// TestExtRemoveControlled 校验 remove 的 PEP503 归一 + Rel/EvalSymlinks containment + 只删 prefix 内。
func TestExtRemoveControlled(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), DefaultGlobalSubdir, pyExtSubdir)
	if err := os.MkdirAll(prefix, 0o700); err != nil {
		t.Fatal(err)
	}
	// requests↔requests-2.31.0.dist-info：PEP503 归一后 -→_ 也要匹配。
	for _, dir := range []string{"requests-2.31.0.dist-info", "requests", "zope.interface-6.0.dist-info"} {
		if err := os.MkdirAll(filepath.Join(prefix, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(prefix, "requests-2.31.0.dist-info", "METADATA"), []byte("Name: requests\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (realExtManager{}).RemovePackages(prefix, []string{"requests==2.31.0", "zope.interface"})
	if err != nil {
		t.Fatalf("remove 失败: %v", err)
	}
	if len(result.Removed) != 3 {
		t.Fatalf("应移除三个匹配项: %+v", result.Removed)
	}
	for _, dir := range []string{"requests-2.31.0.dist-info", "requests", "zope.interface-6.0.dist-info"} {
		if _, err := os.Stat(filepath.Join(prefix, dir)); !os.IsNotExist(err) {
			t.Fatalf("目录 %s 未被删除: %v", dir, err)
		}
	}
	// 符号链接指向外部时拒绝，且不跟随删除（外部目标必须完好）。
	outside := t.TempDir()
	marker := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(prefix, "evil-1.0.dist-info")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("本环境不支持符号链接: %v", err)
	}
	_, err = (realExtManager{}).RemovePackages(prefix, []string{"evil"})
	if err == nil {
		t.Fatal("符号链接指向外部应拒绝")
	}
	if payload, err := os.ReadFile(marker); err != nil || string(payload) != "keep" {
		t.Fatalf("跟随符号链接删了外部文件: %v %q", err, payload)
	}
}
