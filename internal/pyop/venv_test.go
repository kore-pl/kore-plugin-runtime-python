package pyop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// venvFake 在 stubExecutor 之上实现 VenvManager，记录调用参数并返回预设结果。
type venvFake struct {
	*stubExecutor
	createSpec   VenvSpec
	createResult VenvCreateResult
	createErr    error
	createCalls  int

	installSpec   PipInstallSpec
	installResult PipInstallResult
	installErr    error
	installCalls  int

	venvs       VenvListResult
	venvsErr    error
	packages    PipListResult
	packagesErr error
}

func (f *venvFake) CreateVenv(root string, spec VenvSpec) (VenvCreateResult, error) {
	f.createCalls++
	f.createSpec = spec
	return f.createResult, f.createErr
}

func (f *venvFake) InstallPackages(spec PipInstallSpec) (PipInstallResult, error) {
	f.installCalls++
	f.installSpec = spec
	return f.installResult, f.installErr
}

func (f *venvFake) ListVenvs(root string) (VenvListResult, error) { return f.venvs, f.venvsErr }

func (f *venvFake) ListPackages(venvPath string) (PipListResult, error) {
	return f.packages, f.packagesErr
}

// mustCallVenv 调 Call 并要求成功返回指定类型。
func mustCallVenv[T any](t *testing.T, method, params, root string, exec Executor) T {
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

// TestVenvCreateDefaults 校验缺省路径 <state>/venvs/venv 与 python_version 形状。
func TestVenvCreateDefaults(t *testing.T) {
	root := t.TempDir()
	fake := &venvFake{createResult: VenvCreateResult{PythonVersion: "3.12.4", Created: true}}
	result := mustCallVenv[VenvCreateResult](t, "runtime-python.venv.create", `{}`, root, fake)
	if result.Path != filepath.Join(root, DefaultVenvSubdir, "venv") || !result.Created {
		t.Fatalf("缺省创建结果不对: %+v", result)
	}
	if fake.createSpec.PythonVersion != "" {
		t.Fatalf("未指定 python_version 时不应传版本: %+v", fake.createSpec)
	}
	result = mustCallVenv[VenvCreateResult](t, "runtime-python.venv.create",
		`{"python_version":"3.12.4","packages":["requests==2.31.0"]}`, root, fake)
	if !result.NetworkUsed || len(result.Packages) != 1 {
		t.Fatalf("带 packages 时应标注 network_used 并回显包: %+v", result)
	}
}

// TestVenvPathOutOfRoot 校验显式路径越界 → E_INVALID + hint（Detail 含允许根）。
func TestVenvPathOutOfRoot(t *testing.T) {
	root := t.TempDir()
	fake := &venvFake{}
	for _, bad := range []string{"/etc/kore-venv-test", "/tmp/../etc/kore-venv", "relative/venv"} {
		_, perr := Call("runtime-python.venv.create", json.RawMessage(`{"path":"`+bad+`"}`), root, fake)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("path=%q 期望 E_INVALID，得到 %+v", bad, perr)
		}
	}
	// 白名单外（非 ..、非相对路径）必须带可操作 hint。
	_, perr := Call("runtime-python.venv.create", json.RawMessage(`{"path":"/etc/kore-venv-test"}`), root, fake)
	if perr == nil || !strings.Contains(string(perr.Detail), "hint") {
		t.Fatalf("越界错误缺 hint: %+v detail=%s", perr, perr.Detail)
	}
	if fake.createCalls != 0 {
		t.Fatalf("越界校验失败仍触达执行面: %d", fake.createCalls)
	}
}

// TestPipInstallRejectsInjection 校验包名白名单挡命令/选项注入，合法包正常透传。
func TestPipInstallRejectsInjection(t *testing.T) {
	root := t.TempDir()
	venv := makeFakeVenv(t, root)
	fake := &venvFake{}
	bad := []string{
		"pkg; rm -rf /",
		"--index-url=http://evil.example/simple",
		"pkg extra",
		"pkg$(whoami)",
		"pkg`id`",
		"",
	}
	for _, pkg := range bad {
		_, perr := Call("runtime-python.pip.install",
			json.RawMessage(`{"venv_path":"`+venv+`","packages":["`+pkg+`"]}`), root, fake)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("包名 %q 期望 E_INVALID，得到 %+v", pkg, perr)
		}
	}
	if fake.installCalls != 0 {
		t.Fatalf("注入包不应触达执行面: %d", fake.installCalls)
	}
	result := mustCallVenv[PipInstallResult](t, "runtime-python.pip.install",
		`{"venv_path":"`+venv+`","packages":["requests==2.31.0","Django"]}`, root, fake)
	if !result.NetworkUsed || len(result.Packages) != 2 || result.VenvPath != venv {
		t.Fatalf("合法安装结果不对: %+v", result)
	}
	if strings.Join(fake.installSpec.Packages, ",") != "requests==2.31.0,Django" {
		t.Fatalf("透传包列表不对: %+v", fake.installSpec)
	}
}

// TestVenvCreateExistingNoOverwrite 校验真实执行面遇到已存在目录时 created=false 且不覆盖。
func TestVenvCreateExistingNoOverwrite(t *testing.T) {
	root := t.TempDir()
	venv := filepath.Join(root, DefaultVenvSubdir, "venv")
	if err := os.MkdirAll(venv, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(venv, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), []byte("version = 3.11.9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := realVenvManager{}.CreateVenv(root, VenvSpec{Path: venv})
	if err != nil {
		t.Fatalf("已存在 venv 不应报错: %v", err)
	}
	if result.Created || result.PythonVersion != "3.11.9" {
		t.Fatalf("created/python_version 不对: %+v", result)
	}
	if payload, err := os.ReadFile(marker); err != nil || string(payload) != "keep" {
		t.Fatalf("已存在目录被覆盖: %v %q", err, payload)
	}
}

// TestPipTimeoutAndTruncation 校验超时映射与输出截断。
func TestPipTimeoutAndTruncation(t *testing.T) {
	root := t.TempDir()
	venv := makeFakeVenv(t, root)
	timeoutFake := &venvFake{installErr: ErrVenvTimeout}
	_, perr := Call("runtime-python.pip.install",
		json.RawMessage(`{"venv_path":"`+venv+`","packages":["requests"]}`), root, timeoutFake)
	if perr == nil || perr.Code != pluginapi.E_UPSTREAM || !strings.Contains(perr.Message, "超时") {
		t.Fatalf("超时应映射 E_UPSTREAM: %+v", perr)
	}
	big := strings.Repeat("x", MaxPipOutputBytes+4096)
	fake := &venvFake{installResult: PipInstallResult{Output: big}}
	result := mustCallVenv[PipInstallResult](t, "runtime-python.pip.install",
		`{"venv_path":"`+venv+`","packages":["requests"]}`, root, fake)
	if !result.Truncated || len(result.Output) >= len(big) || !strings.Contains(result.Output, "截断") {
		t.Fatalf("输出应被截断并标记: truncated=%v len=%d", result.Truncated, len(result.Output))
	}
}

// TestVenvAndPipList 校验两个只读列表方法，空列表返回 [] 而非 null。
func TestVenvAndPipList(t *testing.T) {
	root := t.TempDir()
	venv := makeFakeVenv(t, root)
	fake := &venvFake{venvsErr: nil}
	list := mustCallVenv[VenvListResult](t, "runtime-python.venv.list", `{}`, root, fake)
	if list.Count != 0 || list.Venvs == nil || !strings.Contains(list.Hint, "venv.create") {
		t.Fatalf("空 venv 列表不对: %+v", list)
	}
	fake.packages = PipListResult{Packages: []PackageInfo{{Name: "requests", Version: "2.31.0"}}}
	pkgs := mustCallVenv[PipListResult](t, "runtime-python.pip.list", `{"venv_path":"`+venv+`"}`, root, fake)
	if pkgs.Count != 1 || pkgs.Packages[0].Name != "requests" {
		t.Fatalf("pip.list 结果不对: %+v", pkgs)
	}
	if _, perr := Call("runtime-python.pip.list", json.RawMessage(`{}`), root, fake); perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("缺 venv_path 期望 E_INVALID: %+v", perr)
	}
}

// makeFakeVenv 造一个含 bin/pip 的假 venv（真实 venv 需要宿主 python，测试不需要）。
func makeFakeVenv(t *testing.T, root string) string {
	t.Helper()
	venv := filepath.Join(root, DefaultVenvSubdir, "fake")
	if err := os.MkdirAll(filepath.Join(venv, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venv, "bin", "pip"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return venv
}
