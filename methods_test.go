package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestDispatchMethods 两个 runtime-python 方法经 dispatch 返回结构化结果。
func TestDispatchMethods(t *testing.T) {
	cases := []struct {
		method string
		params string
		want   string
	}{
		{"python.versions", `{}`, `"count":5`},
		{"python.venv.create", `{"version":"3.12","path":"envs/app"}`, `"version":"3.12"`},
		{"versions", `{}`, `"default":"3.11"`},
	}
	for _, c := range cases {
		req := &pluginapi.Request{
			Type: pluginapi.TypeRequest, ID: "id", ReqID: "r",
			Method: c.method, Params: json.RawMessage(c.params),
		}
		resp, shutdown := dispatch(req, testConfig(), testLogger())
		if shutdown {
			t.Fatalf("%s 不应触发 shutdown", c.method)
		}
		if resp.Error != nil {
			t.Fatalf("%s 意外错误: %v", c.method, resp.Error)
		}
		if !strings.Contains(string(resp.Result), c.want) {
			t.Fatalf("%s 结果=%s，未含 %q", c.method, resp.Result, c.want)
		}
	}
}

// TestDispatchReject 越界路径、非法版本与未知方法经 dispatch 返回协议错误。
func TestDispatchReject(t *testing.T) {
	cases := []struct {
		method string
		params string
		code   string
	}{
		{"python.venv.create", `{"version":"3.11","path":"../../etc"}`, pluginapi.E_INVALID},
		{"python.venv.create", `{"version":"2.7","path":"envs/app"}`, pluginapi.E_UNSUPPORTED},
		{"python.exec", `{}`, pluginapi.E_NOTFOUND},
	}
	for _, c := range cases {
		req := &pluginapi.Request{
			Type: pluginapi.TypeRequest, ID: "id", ReqID: "r",
			Method: c.method, Params: json.RawMessage(c.params),
		}
		resp, _ := dispatch(req, testConfig(), testLogger())
		if resp.Error == nil || resp.Error.Code != c.code {
			t.Fatalf("%s 错误=%v，期望 %q", c.method, resp.Error, c.code)
		}
		if resp.Result != nil {
			t.Fatalf("%s 出错时不应带结果", c.method)
		}
	}
}

// TestPingResult kore.ping 返回 ready 状态。
func TestPingResult(t *testing.T) {
	req := &pluginapi.Request{Type: pluginapi.TypeRequest, ID: "id", ReqID: "r", Method: "kore.ping"}
	resp, _ := dispatch(req, testConfig(), testLogger())
	if !strings.Contains(string(resp.Result), `"status":"ready"`) {
		t.Fatalf("ping 结果=%s", resp.Result)
	}
}
