package fault

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengtang1024/kore-plugin-runtime-python/internal/pyop"
)

// killOnceVenvExec 复用无故障执行面，只在 pip.install 上注入一次 kill -9。
type killOnceVenvExec struct {
	noFaultExecutor
	calls int
}

func (k *killOnceVenvExec) InstallPackages(pyop.PipInstallSpec) (pyop.PipInstallResult, error) {
	k.calls++
	return pyop.PipInstallResult{}, pyop.NewFaultError(pyop.FaultKill, "pip 子进程被信号 9 终止")
}

// TestPipKillThenRetry 校验 pip 子进程被杀后：错误码正确、venv 目录与 bin/pip 完好、
// 可立即用同一 venv 重试成功，且不残留临时目录。
func TestPipKillThenRetry(t *testing.T) {
	root := t.TempDir()
	venv := filepath.Join(root, "venvs", "fault")
	if err := os.MkdirAll(filepath.Join(venv, "bin"), 0o700); err != nil {
		t.Fatalf("建 venv: %v", err)
	}
	pipPath := filepath.Join(venv, "bin", "pip")
	if err := os.WriteFile(pipPath, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("写 pip: %v", err)
	}
	sentinel := filepath.Join(venv, "site-packages-marker")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatalf("写 marker: %v", err)
	}

	params := json.RawMessage(`{"venv_path":` + jsonString(venv) + `,"packages":["requests==2.31.0"]}`)
	killed := &killOnceVenvExec{}
	result, perr := pyop.Call("runtime-python.pip.install", params, root, killed)
	if perr == nil || perr.Code != "E_UPSTREAM" || !strings.Contains(perr.Message, "信号 9") {
		t.Fatalf("kill 注入应返回 E_UPSTREAM（含 信号 9）: result=%s err=%+v", result, perr)
	}
	if killed.calls != 1 {
		t.Fatalf("kill 注入应恰好触发一次执行面: %d", killed.calls)
	}
	// venv 未被打坏：pip 与已有文件仍在，未被删除或重建。
	for _, path := range []string{pipPath, sentinel} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("kill 后 venv 内容丢失 %s: %v", path, err)
		}
	}
	if payload, err := os.ReadFile(sentinel); err != nil || string(payload) != "keep" {
		t.Fatalf("kill 后 venv 内容被改写: %q err=%v", payload, err)
	}

	// 同一 venv 立即重试：不重建、不报错、结果正常。
	raw, perr := pyop.Call("runtime-python.pip.install", params, root, noFaultExecutor{})
	if perr != nil {
		t.Fatalf("kill 后重试失败: %+v", perr)
	}
	var got pyop.PipInstallResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("解析重试结果: %v", err)
	}
	if got.VenvPath != venv || !got.NetworkUsed || len(got.Packages) != 1 {
		t.Fatalf("重试结果不对: %+v", got)
	}

	// 不泄漏临时目录：venvs 下只有 fault 一个目录，root 下只有 venvs。
	entries, err := os.ReadDir(filepath.Join(root, "venvs"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "fault" {
		t.Fatalf("venvs 下出现多余临时目录: %v err=%v", namesOf(entries), err)
	}
	rootEntries, err := os.ReadDir(root)
	if err != nil || len(rootEntries) != 1 || rootEntries[0].Name() != "venvs" {
		t.Fatalf("root 下出现多余临时目录: %v err=%v", namesOf(rootEntries), err)
	}
}

// namesOf 提取目录项名称，便于失败信息可读。
func namesOf(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	return out
}
