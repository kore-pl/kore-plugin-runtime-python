package pyop

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

const testRoot = "/var/lib/kore/runtime-python"

// TestVersions 版本白名单返回 3.9-3.13 五个版本且默认 3.11。
func TestVersions(t *testing.T) {
	result, perr := Call("python.versions", json.RawMessage(`{}`), testRoot, nil)
	if perr != nil {
		t.Fatalf("versions 意外错误: %v", perr)
	}
	var got VersionResult
	if err := json.Unmarshal(result, &got); err != nil {
		t.Fatalf("解析结果失败: %v", err)
	}
	if got.Count != 5 || len(got.Versions) != 5 || got.Default != "3.11" {
		t.Fatalf("版本结果异常: %+v", got)
	}
	want := []string{"3.9", "3.10", "3.11", "3.12", "3.13"}
	for i, v := range want {
		if got.Versions[i] != v {
			t.Fatalf("版本[%d]=%q，期望 %q", i, got.Versions[i], v)
		}
	}
}

// TestSupportedVersions 白名单判定覆盖边界与非法值。
func TestSupportedVersions(t *testing.T) {
	for _, v := range []string{"3.9", "3.10", "3.11", "3.12", "3.13"} {
		if !IsSupportedVersion(v) {
			t.Fatalf("%q 应在白名单内", v)
		}
	}
	for _, v := range []string{"", "2.7", "3.8", "3.14", "3.11.2", "python3.11"} {
		if IsSupportedVersion(v) {
			t.Fatalf("%q 不应在白名单内", v)
		}
	}
}

// TestVenvCreate 骨架口径 applied=false，gunicorn 模板标记为 true，不真建 venv。
func TestVenvCreate(t *testing.T) {
	result, perr := Call("python.venv.create", json.RawMessage(`{"version":"3.12","path":"envs/app"}`), testRoot, nil)
	if perr != nil {
		t.Fatalf("venv.create 意外错误: %v", perr)
	}
	var got VenvCreateResult
	if err := json.Unmarshal(result, &got); err != nil {
		t.Fatalf("解析结果失败: %v", err)
	}
	if got.Version != "3.12" || got.Path != "envs/app" || !got.Gunicorn || got.Applied {
		t.Fatalf("venv 结果异常: %+v", got)
	}
}

// TestVenvCreateDefaultVersion 缺省版本使用默认值 3.11。
func TestVenvCreateDefaultVersion(t *testing.T) {
	result, perr := Call("venv.create", json.RawMessage(`{"path":"envs/default"}`), testRoot, nil)
	if perr != nil {
		t.Fatalf("venv.create 意外错误: %v", perr)
	}
	if !strings.Contains(string(result), `"version":"3.11"`) {
		t.Fatalf("结果=%s，未含默认版本", result)
	}
}

// TestParamValidation 非法参数形状、路径穿越、非法版本均返回协议错误。
func TestParamValidation(t *testing.T) {
	cases := []struct {
		method string
		params string
		code   string
	}{
		{"python.versions", `[]`, pluginapi.E_INVALID},
		{"python.versions", `not-json`, pluginapi.E_INVALID},
		{"python.venv.create", `{"version":"3.11","path":"../../etc"}`, pluginapi.E_INVALID},
		{"python.venv.create", `{"version":"3.11","path":"/etc/passwd"}`, pluginapi.E_INVALID},
		{"python.venv.create", `{"version":"3.11","path":"envs\\app"}`, pluginapi.E_INVALID},
		{"python.venv.create", `{"version":"2.7","path":"envs/app"}`, pluginapi.E_UNSUPPORTED},
	}
	for _, c := range cases {
		_, perr := Call(c.method, json.RawMessage(c.params), testRoot, nil)
		if perr == nil || perr.Code != c.code {
			t.Fatalf("Call(%s,%s) 错误=%v，期望 %q", c.method, c.params, perr, c.code)
		}
	}
}

// TestUnknownMethod 未知方法返回 E_NOTFOUND。
func TestUnknownMethod(t *testing.T) {
	_, perr := Call("python.exec", json.RawMessage(`{}`), testRoot, nil)
	if perr == nil || perr.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("未知方法错误=%v，期望 E_NOTFOUND", perr)
	}
}

// TestValidatePath 路径校验白名单与黑名单。
func TestValidatePath(t *testing.T) {
	for raw, want := range map[string]string{
		"envs":     "/var/lib/kore/runtime-python/envs",
		"envs/app": "/var/lib/kore/runtime-python/envs/app",
		"a//b":     "/var/lib/kore/runtime-python/a/b",
		".":        testRoot,
	} {
		got, perr := ValidatePath(testRoot, raw)
		if perr != nil || got != want {
			t.Fatalf("ValidatePath(%q)=(%q,%v)，期望 %q", raw, got, perr, want)
		}
	}
	for _, raw := range []string{"", "/etc", "../etc", "a/../../b", `envs\app`, "envs/\x00a"} {
		if got, perr := ValidatePath(testRoot, raw); perr == nil {
			t.Fatalf("ValidatePath(%q) 未拒绝（返回 %q）", raw, got)
		}
	}
}
