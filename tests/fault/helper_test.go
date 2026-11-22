// Package fault 对 runtime-python 插件做基础故障注入，验证方法级错误返回。
// 每个用例向 pyop.Call 的执行面注入一种故障，断言返回的结构化错误码。
package fault

import (
	"encoding/json"
	"testing"

	"github.com/pengtang1024/kore-plugin-runtime-python/internal/pyop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

const testRoot = "/var/lib/kore/runtime-python"

// faultExecutor 按注入类别让 venv.create 失败，模拟运行时故障。
type faultExecutor struct {
	kind pyop.FaultKind
}

func (f faultExecutor) VenvCreate(root, rel, version string) (bool, error) {
	return false, pyop.NewFaultError(f.kind, "venv.create 执行面故障")
}

// assertFault 注入故障，断言返回 nil 结果 + 预期的协议错误码与类别关键词。
func assertFault(t *testing.T, kind pyop.FaultKind, wantCode, wantHint string) {
	t.Helper()
	params := json.RawMessage(`{"version":"3.11","path":"envs/app"}`)
	result, perr := pyop.Call("runtime-python.venv.create", params, testRoot, faultExecutor{kind: kind})
	if perr == nil {
		t.Fatalf("注入 %s 后未返回错误（结果=%s）", kind, result)
	}
	if result != nil {
		t.Fatalf("出错时不应带结果: %s", result)
	}
	if perr.Code != wantCode {
		t.Fatalf("错误码=%q，期望 %q（message=%q）", perr.Code, wantCode, perr.Message)
	}
	if !contains(perr.Message, wantHint) {
		t.Fatalf("错误信息 %q 未含类别关键词 %q", perr.Message, wantHint)
	}
}

// contains 是 strings.Contains 的本地封装，避免重复 import。
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

// sanityNoFault 验证无故障时正常返回，证明故障断言不是"永远失败"的假通过。
func sanityNoFault(t *testing.T) {
	t.Helper()
	params := json.RawMessage(`{"version":"3.11","path":"envs/app"}`)
	result, perr := pyop.Call("runtime-python.venv.create", params, testRoot, nil)
	if perr != nil {
		t.Fatalf("无故障时意外返回错误: %v", perr)
	}
	if len(result) == 0 {
		t.Fatal("无故障时返回空结果")
	}
}

// verifyErrorContract 断言错误体是标准协议结构（含 code 字段）。
func verifyErrorContract(t *testing.T, perr *pluginapi.Error) {
	t.Helper()
	if perr == nil {
		t.Fatal("错误体为 nil")
	}
	if perr.Code == "" || perr.Message == "" {
		t.Fatalf("错误体字段不完整: %+v", perr)
	}
}
