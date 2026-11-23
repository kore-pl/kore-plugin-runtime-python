package pyop

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// Call 执行一个 runtime-python 方法：先校验方法名与参数（含 root 白名单），
// 再调用执行面并结构化结果。exec 为 nil 时使用真实探测执行面。
//
// stateRoot 是插件状态目录（KORE_PYTHON_ROOT），它本身也是 root 白名单的一项。
func Call(method string, params json.RawMessage, stateRoot string, exec Executor) (json.RawMessage, *pluginapi.Error) {
	verb, ok := businessVerb(method)
	if !ok {
		return nil, errNotFound(method)
	}
	if exec == nil {
		exec = NewExecutor()
	}
	switch verb {
	case "versions":
		return callVersions(params, exec)
	case "info":
		return callInfo(params, exec)
	case "projects":
		return callProjects(params, exec, stateRoot)
	case "doctor":
		return callDoctor(params, exec, stateRoot)
	}
	return nil, errNotFound(method)
}

// methodPrefix 是方法名的插件域前缀：协议要求 `<plugin-id>.<action>`（PLUGIN-PROTOCOL §7.2）。
const methodPrefix = "runtime-python."

// businessVerb 把 runtime-python.<verb> 归一为动词名；域不是本插件 id 的一律拒绝。
func businessVerb(method string) (string, bool) {
	verb, ok := strings.CutPrefix(method, methodPrefix)
	if !ok {
		return "", false
	}
	switch verb {
	case "versions", "info", "projects", "doctor":
		return verb, true
	}
	return "", false
}

// strictUnmarshal 只接受 JSON 对象入参（拒绝数组/标量/null/空载荷），且拒绝未知字段。
func strictUnmarshal(params json.RawMessage, out any) error {
	trimmed := bytes.TrimSpace(params)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("参数必须是 JSON 对象")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("参数含多余内容")
	}
	return nil
}

// marshal 把结构化结果编码为 result 载荷（关闭 HTML 转义，便于人读与证据留档）。
func marshal(v any) (json.RawMessage, *pluginapi.Error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return nil, internalErr("序列化结果失败: " + err.Error())
	}
	return json.RawMessage(bytes.TrimRight(buffer.Bytes(), "\n")), nil
}

// classify 把执行面错误映射为协议错误码：
// 未装 Python → E_NOTFOUND；指定版本未装 → E_INVALID；四类故障各有对应；其余 E_INTERNAL。
func classify(err error) *pluginapi.Error {
	var fe *FaultError
	if errors.As(err, &fe) {
		switch fe.Kind {
		case FaultKill:
			return upstream("进程被信号 9 终止: " + fe.Message)
		case FaultTimeout:
			return upstream("调用超时: " + fe.Message)
		case FaultDisk:
			return internalErr("磁盘满: " + fe.Message)
		case FaultCgroup:
			return unsupported("cgroup 限额触发: " + fe.Message)
		}
	}
	switch {
	case errors.Is(err, ErrPythonNotInstalled):
		return notFound(NoPythonHint)
	case errors.Is(err, ErrVersionNotInstalled):
		return invalid(err.Error())
	}
	return internalErr("执行面失败: " + err.Error())
}

func invalid(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_INVALID, Message: msg}
}

func errNotFound(method string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_NOTFOUND, Message: "未知方法: " + method}
}

func notFound(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_NOTFOUND, Message: msg}
}

func upstream(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_UPSTREAM, Message: msg}
}

func internalErr(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_INTERNAL, Message: msg}
}

func unsupported(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_UNSUPPORTED, Message: msg}
}
