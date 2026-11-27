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
// stateRoot 是插件状态目录（KORE_PYTHON_ROOT），它本身也是 root 白名单的一项；
// 守护数据与「默认版本」钉住记录都落在它下面。
//
// opts 只用于测试注入（生产调用不传）：WithGuard 可替换守护进程操作面，
// 让单测在不拉起真实子进程、不碰宿主的前提下验证启停状态机。
func Call(method string, params json.RawMessage, stateRoot string, exec Executor, opts ...Option) (json.RawMessage, *pluginapi.Error) {
	verb, ok := businessVerb(method)
	if !ok {
		return nil, errNotFound(method)
	}
	if exec == nil {
		exec = NewExecutor()
	}
	options := callOptions{}
	for _, apply := range opts {
		apply(&options)
	}
	guard := options.guard
	if guard == nil {
		var resolver BinResolver
		if candidate, ok := exec.(BinResolver); ok {
			resolver = candidate
		}
		guard = NewGuard(stateRoot, nil, resolver)
	}
	switch verb {
	case "versions":
		return callVersions(params, exec, stateRoot)
	case "info":
		return callInfo(params, exec)
	case "projects":
		return callProjects(params, exec, stateRoot)
	case "doctor":
		return callDoctor(params, exec, stateRoot)
	case "paths":
		return callPaths(params, exec)
	case "use":
		return callUse(params, exec, stateRoot)
	case "guard.add":
		return callGuardAdd(params, guard)
	case "guard.list":
		return callGuardList(params, guard)
	case "guard.start":
		return callGuardStart(params, guard)
	case "guard.stop":
		return callGuardStop(params, guard)
	case "guard.restart":
		return callGuardRestart(params, guard)
	case "guard.remove":
		return callGuardRemove(params, guard)
	case "guard.logs":
		return callGuardLogs(params, guard)
	case "venv.create":
		return callVenvCreate(params, stateRoot, exec)
	case "venv.list":
		return callVenvList(params, stateRoot, exec)
	case "pip.install":
		return callPipInstall(params, stateRoot, exec)
	case "pip.list":
		return callPipList(params, stateRoot, exec)
	case "ext.install":
		return callExtInstall(params, stateRoot, exec)
	case "ext.list":
		return callExtList(params, stateRoot, exec)
	case "ext.remove":
		return callExtRemove(params, stateRoot, exec)
	}
	return nil, errNotFound(method)
}

// callOptions 是 Call 的可注入依赖（仅测试用）。
type callOptions struct {
	guard *Guard
}

// Option 是 Call 的可选依赖注入。
type Option func(*callOptions)

// WithGuard 注入守护实例（测试用；生产不传，按 stateRoot 自动构造）。
func WithGuard(guard *Guard) Option {
	return func(options *callOptions) { options.guard = guard }
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
	case "versions", "info", "projects", "doctor", "paths", "use", "guard.add", "guard.list", "guard.start", "guard.stop", "guard.restart", "guard.remove", "guard.logs", "venv.create", "venv.list", "pip.install", "pip.list", "ext.install", "ext.list", "ext.remove":
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

// classify 把执行面错误映射为协议错误码。
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
	case errors.Is(err, ErrVenvNoPip):
		return unsupported("venv 内缺少 pip（python3 -m venv 可带 --upgrade-deps 重建）")
	case errors.Is(err, ErrVenvTimeout):
		return upstream("venv/pip 命令超时（上限 " + VenvCommandTimeout.String() + "）")
	case errors.Is(err, ErrExtTimeout):
		return upstream("扩展命令超时（上限 " + VenvCommandTimeout.String() + "），请检查网络或镜像源后重试")
	case errors.Is(err, ErrExtContainment):
		return invalid("扩展条目经符号链接解析后越出隔离目录，请删除该软链后重试")
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
