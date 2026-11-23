package e2e

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengtang1024/kore-plugin-runtime-python/internal/pyop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestBinaryVersionsAndInfo 用真实插件二进制跑一次端到端会话：
// 只给空的 HOME 与不存在的 PATH（模拟最小环境），握手后调用 versions / info，
// 断言"有 Python 时返回真实条目、无 Python 时 count=0 + 固定 hint 且 info 按 E_NOTFOUND 降级"。
func TestBinaryVersionsAndInfo(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	binPath := filepath.Join(t.TempDir(), "runtime-python")
	build := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", binPath, ".")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("构建插件二进制失败: %v\n%s", err, out)
	}
	socketPath := filepath.Join(t.TempDir(), "plugin.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("监听 unix socket 失败: %v", err)
	}
	defer func() { _ = listener.Close() }()
	child := exec.Command(binPath)
	// 只给协议必需变量 + 空 HOME + 不存在的 PATH：PATH 上没有任何 python。
	child.Env = []string{
		"KORE_PLUGIN_SOCK=" + socketPath,
		"KORE_PLUGIN_ID=runtime-python",
		"KORE_PLUGIN_TOKEN=test-token",
		"KORE_LOG_LEVEL=error",
		"HOME=" + t.TempDir(),
		"PATH=/nonexistent-kore-test-path",
	}
	var childLog strings.Builder
	child.Stderr = &childLog
	if err := child.Start(); err != nil {
		t.Fatalf("启动插件失败: %v", err)
	}
	defer func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	}()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatalf("接受插件连接失败: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	hello := readRequest(t, conn)
	if hello.Method != "kore.hello" {
		t.Fatalf("首帧 method=%q，期望 kore.hello（stderr=%s）", hello.Method, childLog.String())
	}
	writeResponse(t, conn, hello, json.RawMessage(`{"ok":true,"protocol":"kore.plugin.v1"}`))

	versions := callBinary(t, conn, "runtime-python.versions", `{}`)
	var result struct {
		Versions []struct {
			Version        string `json:"version"`
			Path           string `json:"path"`
			Source         string `json:"source"`
			IsDefault      bool   `json:"is_default"`
			Implementation string `json:"implementation"`
		} `json:"versions"`
		DefaultVersion string `json:"default_version"`
		Count          int    `json:"count"`
		Hint           string `json:"hint"`
	}
	if err := json.Unmarshal(versions, &result); err != nil {
		t.Fatalf("解析 versions 结果失败: %v（%s）", err, versions)
	}
	if result.Count != len(result.Versions) {
		t.Fatalf("count=%d 与 versions 长度 %d 不一致: %s", result.Count, len(result.Versions), versions)
	}
	if result.Count == 0 {
		if result.Hint != pyop.NoPythonHint {
			t.Fatalf("hint=%q，期望 %q", result.Hint, pyop.NoPythonHint)
		}
		if result.DefaultVersion != "" {
			t.Fatalf("未安装时 default_version 应为空: %s", versions)
		}
	} else {
		// 该进程 PATH 上没有 python3，所以 is_default 由 which python3 决定的结果可以为空；
		// 一旦有默认条目，default_version 必须与它一致。
		if result.DefaultVersion != "" && !versionPattern.MatchString(result.DefaultVersion) {
			t.Fatalf("default_version 格式非法: %q", result.DefaultVersion)
		}
		defaults := 0
		for _, item := range result.Versions {
			if !versionPattern.MatchString(item.Version) {
				t.Fatalf("版本格式非法: %q", item.Version)
			}
			if _, statErr := os.Stat(item.Path); statErr != nil {
				t.Fatalf("版本路径不存在: %q (%v)", item.Path, statErr)
			}
			if item.Implementation == "" {
				t.Fatalf("implementation 为空: %+v", item)
			}
			if item.IsDefault {
				defaults++
				if item.Version != result.DefaultVersion {
					t.Fatalf("默认条目版本 %q 与 default_version %q 不一致", item.Version, result.DefaultVersion)
				}
			}
		}
		if defaults > 1 {
			t.Fatalf("is_default=true 的条目最多 1 个，实际 %d: %s", defaults, versions)
		}
		if defaults == 0 && result.DefaultVersion != "" {
			t.Fatalf("无默认条目但 default_version=%q（PATH 无 python3 时应为空）: %s", result.DefaultVersion, versions)
		}
	}
	t.Logf("二进制 versions => %s", versions)

	// paths：PATH 原始顺序如实回显；本进程 PATH 上没有 python3，故默认解析必然为空，
	// 但只要 known_path 检测到解释器，hint 应说明是「PATH 上没找到」而不是「未检测到 Python」。
	paths := callBinary(t, conn, "runtime-python.paths", `{}`)
	var pathsResult struct {
		PathEnv     []string `json:"path_env"`
		Executables []struct {
			Name  string `json:"name"`
			Found bool   `json:"found"`
		} `json:"executables"`
		Default struct {
			Command string `json:"command"`
			Found   bool   `json:"found"`
		} `json:"default"`
		SearchOrder []struct {
			Source string   `json:"source"`
			Paths  []string `json:"paths"`
		} `json:"search_order"`
		Hint string `json:"hint"`
	}
	if err := json.Unmarshal(paths, &pathsResult); err != nil {
		t.Fatalf("解析 paths 结果失败: %v（%s）", err, paths)
	}
	if len(pathsResult.PathEnv) != 1 || pathsResult.PathEnv[0] != "/nonexistent-kore-test-path" {
		t.Fatalf("path_env 未如实回显: %s", paths)
	}
	if len(pathsResult.Executables) != 2 {
		t.Fatalf("executables 应覆盖 python3/python: %s", paths)
	}
	if pathsResult.Default.Found {
		t.Fatalf("PATH 上没有 python3 时不应解析出默认解释器: %s", paths)
	}
	if result.Count == 0 {
		if pathsResult.Hint != pyop.NoPythonHint {
			t.Fatalf("未安装时 paths hint=%q，期望 %q", pathsResult.Hint, pyop.NoPythonHint)
		}
	} else {
		if pathsResult.Hint != pyop.PathDefaultHint {
			t.Fatalf("已安装但 PATH 无默认时 paths hint=%q，期望 %q", pathsResult.Hint, pyop.PathDefaultHint)
		}
		if len(pathsResult.SearchOrder) == 0 {
			t.Fatalf("已安装时 search_order 不应为空: %s", paths)
		}
	}
	t.Logf("二进制 paths => %s", paths)

	infoReq := &pluginapi.Request{
		Type: pluginapi.TypeRequest, ID: randomID(t), ReqID: "r-info",
		Method: "runtime-python.info", Params: json.RawMessage(`{}`), TimeoutMS: 15000,
	}
	if err := pluginapi.WriteJSON(conn, infoReq); err != nil {
		t.Fatalf("发送 info 失败: %v", err)
	}
	infoResp := readResponse(t, conn)
	if result.Count == 0 {
		if infoResp.Error == nil || infoResp.Error.Code != pluginapi.E_NOTFOUND {
			t.Fatalf("info 错误=%+v，期望 E_NOTFOUND", infoResp.Error)
		}
		t.Logf("二进制降级 info => code=%s message=%s", infoResp.Error.Code, infoResp.Error.Message)
	} else {
		if infoResp.Error != nil {
			t.Fatalf("info 意外错误: %+v", infoResp.Error)
		}
		var info pyop.InfoResult
		if err := json.Unmarshal(infoResp.Result, &info); err != nil {
			t.Fatalf("解析 info 失败: %v（%s）", err, infoResp.Result)
		}
		if info.Version == "" || info.Executable == "" || info.SitePackages == "" {
			t.Fatalf("info 字段不完整: %s", infoResp.Result)
		}
		t.Logf("二进制 info => %s", infoResp.Result)
	}

	shutdown := &pluginapi.Request{Type: pluginapi.TypeRequest, ID: randomID(t), ReqID: "r-shutdown", Method: "kore.shutdown"}
	if err := pluginapi.WriteJSON(conn, shutdown); err != nil {
		t.Fatalf("发送 shutdown 失败: %v", err)
	}
	if got := readResponse(t, conn); got.Error != nil {
		t.Fatalf("shutdown 返回错误: %+v", got.Error)
	}
}

// callBinary 发一个业务请求并返回 result 载荷（出错即失败）。
func callBinary(t *testing.T, conn net.Conn, method, params string) json.RawMessage {
	t.Helper()
	request := &pluginapi.Request{
		Type: pluginapi.TypeRequest, ID: randomID(t), ReqID: "r-" + method,
		Method: method, Params: json.RawMessage(params), TimeoutMS: 15000,
	}
	if err := pluginapi.WriteJSON(conn, request); err != nil {
		t.Fatalf("发送 %s 失败: %v", method, err)
	}
	response := readResponse(t, conn)
	if response.Error != nil {
		t.Fatalf("%s 返回错误: %+v", method, response.Error)
	}
	return response.Result
}

// readRequest 读一帧并断言是 request。
func readRequest(t *testing.T, conn net.Conn) *pluginapi.Request {
	t.Helper()
	env := readEnvelope(t, conn)
	request, ok := env.(*pluginapi.Request)
	if !ok {
		t.Fatalf("帧类型 %T，期望 request", env)
	}
	return request
}

// readResponse 读一帧并断言是 response。
func readResponse(t *testing.T, conn net.Conn) *pluginapi.Response {
	t.Helper()
	env := readEnvelope(t, conn)
	response, ok := env.(*pluginapi.Response)
	if !ok {
		t.Fatalf("帧类型 %T，期望 response", env)
	}
	return response
}

// readEnvelope 读帧并解码为信封。
func readEnvelope(t *testing.T, conn net.Conn) any {
	t.Helper()
	payload, err := pluginapi.ReadFrame(conn, pluginapi.MaxFrameSize)
	if err != nil {
		t.Fatalf("读帧失败: %v", err)
	}
	env, err := pluginapi.DecodeFrame(payload)
	if err != nil {
		t.Fatalf("解码帧失败: %v", err)
	}
	return env
}

// writeResponse 回填 request 的 id/req_id。
func writeResponse(t *testing.T, conn net.Conn, request *pluginapi.Request, result json.RawMessage) {
	t.Helper()
	response := &pluginapi.Response{Type: pluginapi.TypeResponse, ID: request.ID, ReqID: request.ReqID, Result: result}
	if err := pluginapi.WriteJSON(conn, response); err != nil {
		t.Fatalf("写响应失败: %v", err)
	}
}

// randomID 生成 32 位小写 hex 请求 id。
func randomID(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("生成请求 id 失败: %v", err)
	}
	return hex.EncodeToString(buf)
}
