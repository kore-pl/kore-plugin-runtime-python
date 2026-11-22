package pyop

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// Call 执行一个 runtime-python 方法：先校验方法名与参数，再调用执行面并结构化结果。
// exec 为 nil 时使用 noopExecutor（骨架口径，不真建 venv）。
func Call(method string, params json.RawMessage, root string, exec Executor) (json.RawMessage, *pluginapi.Error) {
	verb, ok := businessVerb(method)
	if !ok {
		return nil, errNotFound(method)
	}
	if exec == nil {
		exec = noopExecutor{}
	}
	switch verb {
	case "versions":
		return callVersions(params)
	case "venv.create":
		return callVenvCreate(params, root, exec)
	}
	return nil, errNotFound(method)
}

// businessVerb 把 python.<verb> 或裸 <verb> 归一为动词名。
func businessVerb(method string) (string, bool) {
	verb := method
	if strings.HasPrefix(method, "python.") {
		verb = strings.TrimPrefix(method, "python.")
	}
	switch verb {
	case "versions", "venv.create":
		return verb, true
	}
	return "", false
}

// classify 把执行面错误映射为协议错误码：四类故障各有对应，其余归 E_INTERNAL。
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
	return internalErr("执行面失败: " + err.Error())
}

func invalid(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_INVALID, Message: msg}
}

func errNotFound(method string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_NOTFOUND, Message: "未知方法: " + method}
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
