package pyop

// 守护与版本切换的方法面：参数校验 + 结果结构化 + 错误码映射。
// 所有写操作都限制在插件状态目录与白名单目录内，不碰宿主其它路径。

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// 用户可见提示（写进 hint / 空状态，给「怎么办」而不是内部术语）。
const (
	// GuardEmptyHint 是还没有注册应用时的空状态提示。
	GuardEmptyHint = "还没有被守护的应用。填好应用名、目录与入口脚本后点「添加」即可启动。"
	// GuardStartHint 是未运行时的下一步提示。
	GuardStartHint = "当前未在运行。点「启动」按已保存的配置拉起进程。"
	// GuardCrashedHint 是进程消失后的提示（不自动重启，避免悄悄拉起一个坏掉的进程）。
	GuardCrashedHint = "进程已退出（退出码由系统回收，Kore 不常驻等待，无法回读）。看「日志」定位原因后点「重启」。"
)

// GuardApp 是守护列表里的一行（声明 + 实时状态合并）。
type GuardApp struct {
	Name           string   `json:"name"`
	Dir            string   `json:"dir"`
	Script         string   `json:"script"`
	Args           []string `json:"args"`
	Version        string   `json:"version"`
	State          string   `json:"state"`
	PID            int      `json:"pid"`
	UptimeS        int64    `json:"uptime_s"`
	RSSKB          int64    `json:"rss_kb"`
	LogPath        string   `json:"log_path"`
	LogBytes       int64    `json:"log_bytes"`
	Hint           string   `json:"hint,omitempty"`
	Error          string   `json:"error,omitempty"`
	LastError      string   `json:"last_error,omitempty"`
	ExitCode       int      `json:"exit_code,omitempty"`
	AlreadyRunning bool     `json:"already_running,omitempty"`
	Restarted      bool     `json:"restarted,omitempty"`
}

// GuardListResult 是 runtime-python.guard.list 的结构化结果。
type GuardListResult struct {
	Apps  []GuardApp `json:"apps"`
	Count int        `json:"count"`
	Root  string     `json:"root"`
	Hint  string     `json:"hint,omitempty"`
}

// GuardActionResult 是 guard.start / guard.stop / guard.restart 的统一结果。
type GuardActionResult struct {
	Action         string     `json:"action"`
	Apps           []GuardApp `json:"apps"`
	Count          int        `json:"count"`
	Started        int        `json:"started"`
	Stopped        int        `json:"stopped"`
	Failed         int        `json:"failed"`
	AlreadyRunning int        `json:"already_running"`
	Hint           string     `json:"hint,omitempty"`
}

// GuardLogsResult 是 runtime-python.guard.logs 的结构化结果（日志尾部）。
type GuardLogsResult struct {
	Name       string   `json:"name"`
	Path       string   `json:"path"`
	Lines      []string `json:"lines"`
	Returned   int      `json:"returned"`
	TotalLines int      `json:"total_lines"`
	Requested  int      `json:"requested"`
	Bytes      int64    `json:"bytes"`
	Truncated  bool     `json:"truncated"`
	Hint       string   `json:"hint,omitempty"`
}

// UseResult 是 runtime-python.use 的结构化结果：把某个已安装版本钉为默认。
type UseResult struct {
	Version   string    `json:"version"`
	Path      string    `json:"path"`
	BinDir    string    `json:"bin_dir"`
	Cleared   bool      `json:"cleared"`
	UpdatedAt time.Time `json:"updated_at"`
	Hint      string    `json:"hint,omitempty"`
}

// 入参形状（显式字段，拒绝未知字段）。
type guardAddParams struct {
	Name    string   `json:"name"`
	Dir     string   `json:"dir"`
	Script  string   `json:"script"`
	Args    []string `json:"args"`
	Version string   `json:"version"`
}

type guardListParams struct{}

type guardTargetsParams struct {
	Names []string `json:"names"`
}

type guardNameParams struct {
	Name string `json:"name"`
}

type guardLogsParams struct {
	Name  string `json:"name"`
	Lines int    `json:"lines"`
}

type useParams struct {
	Version string `json:"version"`
}

// callGuardAdd 注册/更新一个被守护应用（不启动）。
func callGuardAdd(params json.RawMessage, guard *Guard) (json.RawMessage, *pluginapi.Error) {
	var p guardAddParams
	if err := strictUnmarshal(params, &p); err != nil {
		return nil, invalid("runtime-python.guard.add 参数非法: " + err.Error())
	}
	if guard == nil {
		return nil, unsupported(ErrGuardPlatform.Error())
	}
	spec, err := guard.Add(AppSpec{Name: p.Name, Dir: p.Dir, Script: p.Script, Args: p.Args, Version: p.Version})
	if err != nil {
		return nil, classifyGuard(err)
	}
	return marshal(spec)
}

// callGuardList 列出全部被守护应用与实时状态（空表不报错）。
func callGuardList(params json.RawMessage, guard *Guard) (json.RawMessage, *pluginapi.Error) {
	var p guardListParams
	if err := strictUnmarshal(params, &p); err != nil {
		return nil, invalid("runtime-python.guard.list 参数非法: " + err.Error())
	}
	if guard == nil {
		return nil, unsupported(ErrGuardPlatform.Error())
	}
	result, err := guard.List()
	if err != nil {
		return nil, classifyGuard(err)
	}
	if result.Apps == nil {
		result.Apps = []GuardApp{}
	}
	return marshal(result)
}

// callGuardStart 启动（零参数 = 全部启动；幂等：已在跑的不会重复拉起）。
func callGuardStart(params json.RawMessage, guard *Guard) (json.RawMessage, *pluginapi.Error) {
	var p guardTargetsParams
	if err := strictUnmarshal(params, &p); err != nil {
		return nil, invalid("runtime-python.guard.start 参数非法: " + err.Error())
	}
	if guard == nil {
		return nil, unsupported(ErrGuardPlatform.Error())
	}
	result, err := guard.Start(p.Names)
	if err != nil {
		return nil, classifyGuard(err)
	}
	if result.Apps == nil {
		result.Apps = []GuardApp{}
	}
	if result.Failed > 0 {
		return marshal(result) // 部分失败：结果里逐条带 error，不整次报错
	}
	return marshal(result)
}

// callGuardStop 停止（零参数 = 全部停止）。
func callGuardStop(params json.RawMessage, guard *Guard) (json.RawMessage, *pluginapi.Error) {
	var p guardTargetsParams
	if err := strictUnmarshal(params, &p); err != nil {
		return nil, invalid("runtime-python.guard.stop 参数非法: " + err.Error())
	}
	if guard == nil {
		return nil, unsupported(ErrGuardPlatform.Error())
	}
	result, err := guard.Stop(p.Names)
	if err != nil {
		return nil, classifyGuard(err)
	}
	if result.Apps == nil {
		result.Apps = []GuardApp{}
	}
	return marshal(result)
}

// callGuardRestart 重启一个应用（名称必填）。
func callGuardRestart(params json.RawMessage, guard *Guard) (json.RawMessage, *pluginapi.Error) {
	var p guardNameParams
	if err := strictUnmarshal(params, &p); err != nil {
		return nil, invalid("runtime-python.guard.restart 参数非法: " + err.Error())
	}
	if guard == nil {
		return nil, unsupported(ErrGuardPlatform.Error())
	}
	if strings.TrimSpace(p.Name) == "" {
		return nil, invalid("请指定要重启的应用名（例如 my-api）")
	}
	result, err := guard.Restart(p.Name)
	if err != nil {
		return nil, classifyGuard(err)
	}
	if result.Apps == nil {
		result.Apps = []GuardApp{}
	}
	return marshal(result)
}

// callGuardRemove 注销一个应用（仍在运行时拒绝）。
func callGuardRemove(params json.RawMessage, guard *Guard) (json.RawMessage, *pluginapi.Error) {
	var p guardNameParams
	if err := strictUnmarshal(params, &p); err != nil {
		return nil, invalid("runtime-python.guard.remove 参数非法: " + err.Error())
	}
	if guard == nil {
		return nil, unsupported(ErrGuardPlatform.Error())
	}
	if strings.TrimSpace(p.Name) == "" {
		return nil, invalid("请指定要删除的应用名")
	}
	if err := guard.Remove(p.Name); err != nil {
		return nil, classifyGuard(err)
	}
	return marshal(map[string]string{"name": p.Name, "status": "removed"})
}

// callGuardLogs 读取一个应用的日志尾部。
func callGuardLogs(params json.RawMessage, guard *Guard) (json.RawMessage, *pluginapi.Error) {
	var p guardLogsParams
	if err := strictUnmarshal(params, &p); err != nil {
		return nil, invalid("runtime-python.guard.logs 参数非法: " + err.Error())
	}
	if guard == nil {
		return nil, unsupported(ErrGuardPlatform.Error())
	}
	if strings.TrimSpace(p.Name) == "" {
		return nil, invalid("请指定要查看日志的应用名")
	}
	result, err := guard.Logs(p.Name, p.Lines)
	if err != nil {
		return nil, classifyGuard(err)
	}
	if result.Lines == nil {
		result.Lines = []string{}
	}
	return marshal(result)
}

// callUse 把某个已安装版本钉为默认（空版本 = 清除钉住、恢复跟随 PATH）。
func callUse(params json.RawMessage, exec Executor, stateRoot string) (json.RawMessage, *pluginapi.Error) {
	var p useParams
	if err := strictUnmarshal(params, &p); err != nil {
		return nil, invalid("runtime-python.use 参数非法: " + err.Error())
	}
	if strings.TrimSpace(stateRoot) == "" {
		return nil, internalErr("缺少插件状态目录（KORE_PYTHON_ROOT），无法保存默认版本")
	}
	version := strings.TrimSpace(p.Version)
	if version == "" {
		if err := clearPin(stateRoot); err != nil {
			return nil, internalErr("清除默认版本失败: " + err.Error())
		}
		return marshal(UseResult{Cleared: true, UpdatedAt: time.Now().UTC(),
			Hint: "已恢复跟随系统 PATH 的默认版本"})
	}
	versions, err := exec.Versions()
	if err != nil {
		return nil, classify(err)
	}
	entry, ok := selectVersion(versions.Versions, version)
	if !ok {
		return nil, invalid(fmt.Sprintf("版本 %s 未安装（已检测: %s）", version, joinVersionList(versions.Versions)))
	}
	pin := pinFile{Version: entry.Version, Path: entry.Path, BinDir: entry.BinDir, UpdatedAt: time.Now().UTC()}
	if err := writePin(stateRoot, pin); err != nil {
		return nil, internalErr("保存默认版本失败: " + err.Error())
	}
	return marshal(UseResult{Version: pin.Version, Path: pin.Path, BinDir: pin.BinDir, UpdatedAt: pin.UpdatedAt,
		Hint: "已设为默认版本；站点与「进程守护」启动时默认用它，可随时切回"})
}

// classifyGuard 把守护错误映射为协议错误码。
func classifyGuard(err error) *pluginapi.Error {
	var invalidErr *guardInvalidError
	if errors.As(err, &invalidErr) {
		return invalid(invalidErr.message)
	}
	switch {
	case errors.Is(err, ErrGuardPlatform):
		return unsupported("当前平台不支持进程守护：请在 Linux 或 macOS 上使用（其他平台仍可查看列表）")
	case errors.Is(err, ErrGuardNotConfigured):
		return invalid(err.Error())
	}
	message := err.Error()
	if strings.Contains(message, "不合法") || strings.Contains(message, "请填写") ||
		strings.Contains(message, "必须") || strings.Contains(message, "不能") ||
		strings.Contains(message, "最多") || strings.Contains(message, "上限") ||
		strings.Contains(message, "不存在") {
		return invalid(message)
	}
	return internalErr("守护操作失败: " + message)
}
