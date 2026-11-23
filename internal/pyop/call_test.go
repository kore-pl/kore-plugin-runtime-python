package pyop

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestCallVersionsNoPython 未检测到 Python 时不报错：空数组 + count=0 + 固定 hint。
func TestCallVersionsNoPython(t *testing.T) {
	exec := &stubExecutor{versions: VersionsResult{}}
	raw, perr := Call("runtime-python.versions", json.RawMessage(`{}`), "", exec)
	if perr != nil {
		t.Fatalf("未安装 Python 时不应报错: %v", perr)
	}
	var result VersionsResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析结果失败: %v", err)
	}
	if result.Count != 0 || len(result.Versions) != 0 {
		t.Fatalf("期望空版本列表: %+v", result)
	}
	if result.Hint != NoPythonHint {
		t.Fatalf("hint=%q，期望 %q", result.Hint, NoPythonHint)
	}
	if !strings.Contains(string(raw), `"versions":[]`) {
		t.Fatalf("versions 必须是空数组而非 null: %s", raw)
	}
}

// TestCallVersionsResult 已安装时返回版本列表、默认版本与计数。
func TestCallVersionsResult(t *testing.T) {
	exec := &stubExecutor{versions: VersionsResult{
		Versions: []PythonVersion{
			{Version: "3.12.3", Path: "/usr/bin/python3", Source: SourcePath, IsDefault: true, Implementation: ImplementationCPython},
		},
		DefaultVersion: "3.12.3",
	}}
	raw, perr := Call("runtime-python.versions", json.RawMessage(`{}`), "", exec)
	if perr != nil {
		t.Fatalf("versions 意外错误: %v", perr)
	}
	for _, want := range []string{`"default_version":"3.12.3"`, `"count":1`, `"implementation":"CPython"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("结果=%s，未含 %q", raw, want)
		}
	}
}

// TestCallInfoNormalizesVersion 入参归一：剥离 Python 前缀与 v 前缀，主次版本保留。
func TestCallInfoNormalizesVersion(t *testing.T) {
	cases := map[string]string{
		`{"version":"Python 3.12.3"}`: "3.12.3",
		`{"version":"v3.11"}`:         "3.11",
		`{"version":" 3.14.4 "}`:      "3.14.4",
	}
	for params, want := range cases {
		exec := &stubExecutor{info: InfoResult{Version: want}}
		if _, perr := Call("runtime-python.info", json.RawMessage(params), "", exec); perr != nil {
			t.Fatalf("info(%s) 意外错误: %v", params, perr)
		}
		if exec.infoArg != want {
			t.Fatalf("info(%s) 执行面收到版本=%q，期望 %q", params, exec.infoArg, want)
		}
	}
}

// TestCallErrorMapping 执行面错误按协议映射：未装 Python → E_NOTFOUND，版本未装 → E_INVALID。
func TestCallErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"python_missing", ErrPythonNotInstalled, pluginapi.E_NOTFOUND},
		{"version_missing", wrapVersionError("9.9.9"), pluginapi.E_INVALID},
		{"kill", NewFaultError(FaultKill, "被终止"), pluginapi.E_UPSTREAM},
		{"timeout", NewFaultError(FaultTimeout, "超时"), pluginapi.E_UPSTREAM},
		{"disk", NewFaultError(FaultDisk, "磁盘满"), pluginapi.E_INTERNAL},
		{"cgroup", NewFaultError(FaultCgroup, "限额"), pluginapi.E_UNSUPPORTED},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exec := &stubExecutor{infoErr: c.err}
			_, perr := Call("runtime-python.info", json.RawMessage(`{}`), "", exec)
			if perr == nil || perr.Code != c.code {
				t.Fatalf("错误=%v，期望 %q", perr, c.code)
			}
		})
	}
}

// wrapVersionError 构造与生产实现同形的「指定版本未安装」错误。
func wrapVersionError(version string) error {
	return &versionError{version: version}
}

type versionError struct{ version string }

func (e *versionError) Error() string { return ErrVersionNotInstalled.Error() + ": " + e.version }

func (e *versionError) Is(target error) bool { return target == ErrVersionNotInstalled }

// TestCallParamValidation 非法 JSON 形状、未知字段、非法版本与越权 root 均返回 E_INVALID。
func TestCallParamValidation(t *testing.T) {
	stateRoot := t.TempDir()
	cases := []struct {
		method string
		params string
	}{
		{"runtime-python.versions", `[]`},
		{"runtime-python.versions", `null`},
		{"runtime-python.versions", `{"family":"python"}`},
		{"runtime-python.info", `{"version":3}`},
		{"runtime-python.info", `{"version":"3"}`},
		{"runtime-python.info", `{"version":"3.12","junk":1}`},
		{"runtime-python.projects", `{}`},
		{"runtime-python.projects", `{"root":"relative/path"}`},
		{"runtime-python.projects", `{"root":"/etc"}`},
		{"runtime-python.projects", `{"root":"/var/www/../etc"}`},
		{"runtime-python.doctor", `{"root":"/etc"}`},
	}
	for _, c := range cases {
		_, perr := Call(c.method, json.RawMessage(c.params), stateRoot, &stubExecutor{})
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("Call(%s,%s) 错误=%v，期望 E_INVALID", c.method, c.params, perr)
		}
	}
}

// TestCallProjectsPassesValidatedRoot 合法 root 经白名单解析后传给执行面。
func TestCallProjectsPassesValidatedRoot(t *testing.T) {
	stateRoot := t.TempDir()
	exec := &stubExecutor{projects: ProjectsResult{}}
	raw, perr := Call("runtime-python.projects", json.RawMessage(`{"root":"`+stateRoot+`"}`), stateRoot, exec)
	if perr != nil {
		t.Fatalf("projects 意外错误: %v", perr)
	}
	if exec.projectsArg == "" {
		t.Fatal("执行面未收到 root")
	}
	if !strings.Contains(string(raw), `"projects":[]`) || !strings.Contains(string(raw), `"warnings":[]`) {
		t.Fatalf("结果应为空数组而非 null: %s", raw)
	}
}

// TestCallDoctorOptionalRoot doctor 的 root 可省略（不触发项目检查）。
func TestCallDoctorOptionalRoot(t *testing.T) {
	exec := &stubExecutor{doctor: DoctorResult{Checks: []Check{{Name: "python", Status: StatusPass, Detail: "ok"}}}}
	raw, perr := Call("runtime-python.doctor", json.RawMessage(`{}`), t.TempDir(), exec)
	if perr != nil {
		t.Fatalf("doctor 意外错误: %v", perr)
	}
	if exec.doctorArg != "" {
		t.Fatalf("未给 root 时执行面应收空串，实际=%q", exec.doctorArg)
	}
	if !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("结果=%s", raw)
	}
}

// TestCallUnknownMethod 未知方法返回 E_NOTFOUND；0.2.0 起骨架方法 venv.create 已移除。
func TestCallUnknownMethod(t *testing.T) {
	for _, method := range []string{"runtime-python.exec", "runtime-python.venv.create", "versions", "python.versions"} {
		_, perr := Call(method, json.RawMessage(`{}`), "", &stubExecutor{})
		if perr == nil || perr.Code != pluginapi.E_NOTFOUND {
			t.Fatalf("Call(%q) 错误=%v，期望 E_NOTFOUND", method, perr)
		}
	}
}
