// Package fault 对 runtime-python 插件做四类故障注入（kill / timeout / disk / cgroup）。
// 每个用例向 pyop.Call 的执行面注入一种故障，断言四个方法返回的结构化错误码；
// 另外配 sanity 用例（无故障正常返回），防止"永远失败"的假通过。
package fault

import (
	"encoding/json"
	"testing"

	"github.com/pengtang1024/kore-plugin-runtime-python/internal/pyop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// faultExecutor 按注入类别让每个方法都失败，模拟四类运行时故障。
type faultExecutor struct {
	kind pyop.FaultKind
}

func (f faultExecutor) Versions() (pyop.VersionsResult, error) {
	return pyop.VersionsResult{}, pyop.NewFaultError(f.kind, "versions 执行面故障")
}

func (f faultExecutor) Info(string) (pyop.InfoResult, error) {
	return pyop.InfoResult{}, pyop.NewFaultError(f.kind, "info 执行面故障")
}

func (f faultExecutor) Projects(string) (pyop.ProjectsResult, error) {
	return pyop.ProjectsResult{}, pyop.NewFaultError(f.kind, "projects 执行面故障")
}

func (f faultExecutor) Doctor(string) (pyop.DoctorResult, error) {
	return pyop.DoctorResult{}, pyop.NewFaultError(f.kind, "doctor 执行面故障")
}

// noFaultExecutor 返回最小合法结果，供 sanity 断言"不注入故障时四方法成功"。
// 不依赖宿主是否安装 Python，保证故障断言不是环境驱动的假通过。
type noFaultExecutor struct{}

func (noFaultExecutor) Versions() (pyop.VersionsResult, error) {
	return pyop.VersionsResult{
		Versions: []pyop.PythonVersion{{
			Version:        "3.12.3",
			Path:           "/usr/bin/python3",
			Source:         pyop.SourcePath,
			IsDefault:      true,
			Implementation: pyop.ImplementationCPython,
		}},
		DefaultVersion: "3.12.3",
		Count:          1,
	}, nil
}

func (noFaultExecutor) Info(string) (pyop.InfoResult, error) {
	return pyop.InfoResult{Version: "3.12.3", Executable: "/usr/bin/python3"}, nil
}

func (noFaultExecutor) Projects(string) (pyop.ProjectsResult, error) {
	return pyop.ProjectsResult{Projects: []pyop.Project{}, Warnings: []string{}}, nil
}

func (noFaultExecutor) Doctor(string) (pyop.DoctorResult, error) {
	return pyop.DoctorResult{Checks: []pyop.Check{{Name: "python", Status: pyop.StatusPass, Detail: "ok"}}}, nil
}

// methodCase 是一个待注入故障的方法及其合法参数（root 由 t.TempDir 动态生成）。
type methodCase struct {
	name   string
	method string
	params func(root string) string
}

// methods 覆盖四个方法；root 使用状态目录白名单（t.TempDir 作为 <state>）。
var methods = []methodCase{
	{"versions", "runtime-python.versions", func(string) string { return `{}` }},
	{"info", "runtime-python.info", func(string) string { return `{}` }},
	{"projects", "runtime-python.projects", func(root string) string {
		return `{"root":` + jsonString(root) + `}`
	}},
	{"doctor", "runtime-python.doctor", func(root string) string {
		return `{"root":` + jsonString(root) + `}`
	}},
}

// assertFault 对每个方法注入故障，断言返回 nil 结果 + 预期协议错误码与类别关键词。
func assertFault(t *testing.T, kind pyop.FaultKind, wantCode, wantHint string) {
	t.Helper()
	root := t.TempDir()
	exec := faultExecutor{kind: kind}
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			result, perr := pyop.Call(m.method, json.RawMessage(m.params(root)), root, exec)
			if perr == nil {
				t.Fatalf("%s 注入 %s 后未返回错误（结果=%s）", m.method, kind, result)
			}
			if result != nil {
				t.Fatalf("%s 出错时不应带结果: %s", m.method, result)
			}
			if perr.Code != wantCode {
				t.Fatalf("%s 错误码=%q，期望 %q（message=%q）", m.method, perr.Code, wantCode, perr.Message)
			}
			if !contains(perr.Message, wantHint) {
				t.Fatalf("%s 错误信息 %q 未含类别关键词 %q", m.method, perr.Message, wantHint)
			}
		})
	}
}

// sanityNoFault 验证无故障时四方法正常返回，证明故障断言不是"永远失败"的假通过。
func sanityNoFault(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	for _, m := range methods {
		result, perr := pyop.Call(m.method, json.RawMessage(m.params(root)), root, noFaultExecutor{})
		if perr != nil {
			t.Fatalf("无故障时 %s 意外返回错误: %v", m.method, perr)
		}
		if len(result) == 0 {
			t.Fatalf("无故障时 %s 返回空结果", m.method)
		}
	}
}

// verifyErrorContract 断言错误体是标准协议结构（非空 code/message）。
func verifyErrorContract(t *testing.T, perr *pluginapi.Error) {
	t.Helper()
	if perr == nil {
		t.Fatal("错误体为 nil")
	}
	if perr.Code == "" || perr.Message == "" {
		t.Fatalf("错误体字段不完整: %+v", perr)
	}
}

// jsonString 把路径编码为 JSON 字符串字面量（路径里的反斜杠/引号会被正确转义）。
func jsonString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

// contains 是 strings.Contains 的本地封装，避免每个 fault 文件重复 import strings。
func contains(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
