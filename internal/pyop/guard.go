package pyop

// 单进程守护（对标 pm2 的极简版，G-11 不做容器编排）：
//   - 只守护本机单个 Python 进程：启动 / 停止 / 重启 / 状态 / 日志尾部；
//   - 不引入常驻中间件（G-05）：没有 supervisor 进程，插件 lazy 拉起、回完响应即退出
//     （G-19 不自起 ticker 续命）；被守护进程用 setsid 脱离会话后独立存活；
//   - 状态全部落盘（<state>/guard/），PID 存活用「PID + 启动时刻」双判据防 PID 复用误判；
//   - 不自动重启：没有常驻进程就没人做崩溃拉起；guard.start 是幂等的「确保在跑」，
//     可由 cron 插件按需调用完成自动拉起（不在本仓自建轮询）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// 守护相关常量。
const (
	// GuardStateDir 是守护数据在插件状态目录下的子目录名。
	GuardStateDir = "guard"
	// GuardAppsFile 是应用注册表文件名。
	GuardAppsFile = "apps.json"
	// GuardStopTimeout 是 SIGTERM 后等待进程自行退出的上限，超时升级 SIGKILL。
	GuardStopTimeout = 5 * time.Second
	// GuardStopPoll 是等待退出时的轮询间隔（只在一次调用内循环，不是常驻 ticker）。
	GuardStopPoll = 100 * time.Millisecond
	// GuardKillTimeout 是 SIGKILL 后确认消失的上限。
	GuardKillTimeout = 3 * time.Second
	// GuardMaxLogBytes 是一次读取日志尾部的字节上限（G-03：不把大日志读进内存）。
	GuardMaxLogBytes = 64 * 1024
	// GuardDefaultLogLines 是 logs 默认返回行数。
	GuardDefaultLogLines = 100
	// GuardMaxLogLines 是 logs 允许的最大行数。
	GuardMaxLogLines = 500
	// GuardMaxApps 是注册应用数上限（单机单进程守护，不做编排）。
	GuardMaxApps = 32
	// GuardMaxArgs 是单个应用的附加参数上限。
	GuardMaxArgs = 16
	// GuardMaxArgLen 是单个参数长度上限。
	GuardMaxArgLen = 256
	// GuardLogFileMode 是日志文件权限。
	GuardLogFileMode = 0o640
	// GuardDirMode 是守护目录权限。
	GuardDirMode = 0o750
)

// GuardAppState 是应用运行状态（对外取值，前端据此渲染色）。
const (
	GuardRunning = "running"
	GuardStopped = "stopped"
	GuardCrashed = "crashed"
)

// ErrGuardPlatform 表示当前平台不支持进程守护（降级而不是静默失败）。
var ErrGuardPlatform = errors.New("当前平台不支持进程守护（仅 Linux / macOS）")

// ErrGuardNotConfigured 表示请求的应用未在守护列表里注册。
var ErrGuardNotConfigured = errors.New("未找到该应用，请先在「进程守护」里添加名称、目录与入口脚本")

// guardInvalidError 是「用户填写内容不合法」的守护错误：统一映射 E_INVALID，
// 文案自带「怎么办」，不混进内部错误（否则用户看到 500 级的 E_INTERNAL 无从下手）。
type guardInvalidError struct{ message string }

// Error 实现 error 接口。
func (e *guardInvalidError) Error() string { return e.message }

// guardInvalid 构造一条入参不合法错误。
func guardInvalid(format string, args ...any) error {
	return &guardInvalidError{message: fmt.Sprintf(format, args...)}
}

// guardAppNamePattern 是应用名单约束：小写字母开头，仅小写字母/数字/连字符，2–32 字符。
var guardAppNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// AppSpec 是一个被守护应用的声明（持久化在 <state>/guard/apps.json）。
type AppSpec struct {
	Name    string   `json:"name"`
	Dir     string   `json:"dir"`
	Script  string   `json:"script"`
	Args    []string `json:"args"`
	Version string   `json:"version,omitempty"`
}

// appState 是一个应用的运行态（持久化在 <state>/guard/<name>.state.json）。
type appState struct {
	PID        int       `json:"pid"`
	StartTicks uint64    `json:"start_ticks"`
	StartedAt  time.Time `json:"started_at"`
	LogPath    string    `json:"log_path"`
	StoppedAt  time.Time `json:"stopped_at,omitempty"`
	ExitCode   int       `json:"exit_code,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
}

// appsFile 是应用注册表的落盘形状。
type appsFile struct {
	Apps      []AppSpec `json:"apps"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ProcInfo 是一次进程探测结果：启动时刻（防 PID 复用）+ 常驻内存。
type ProcInfo struct {
	StartTicks uint64 `json:"start_ticks"`
	RSSKB      int64  `json:"rss_kb"`
}

// SpawnSpec 是一次拉起请求（固定 argv、无 shell、工作目录与日志由守护侧决定）。
// OnExit 在子进程退出时被调用一次（生产实现用 Wait 回收，避免僵尸进程；
// 退出码据此落盘，状态页就能显示「已退出（退出码 N）」而不是只能让人猜日志）。
type SpawnSpec struct {
	Bin     string
	Args    []string
	Dir     string
	LogPath string
	OnExit  func(code int)
}

// SpawnResult 是拉起结果：PID + 启动时刻（用于后续存活判定）。
type SpawnResult struct {
	PID        int
	StartTicks uint64
}

// GuardIO 是进程操作面：生产实现走 os/exec + syscall，测试注入假实现。
type GuardIO interface {
	// Spawn 拉起一个脱离会话的子进程，stdout/stderr 追加到 LogPath。
	Spawn(spec SpawnSpec) (SpawnResult, error)
	// Signal 向进程（及其进程组）发信号；force=false 为 SIGTERM，true 为 SIGKILL。
	Signal(pid int, force bool) error
	// Probe 探测进程是否仍存活，返回启动时刻与 RSS；ok=false 表示已不存在。
	Probe(pid int) (ProcInfo, bool)
}

// BinResolver 把「运行时版本」解析为可执行文件路径（由各 runtime 执行面实现）。
type BinResolver interface {
	ResolveBin(version string) (string, error)
}

// Guard 是单进程守护的状态机：所有状态落盘，不持有常驻 goroutine 与 ticker。
type Guard struct {
	stateRoot string
	root      string
	io        GuardIO
	resolver  BinResolver
	now       func() time.Time
	// stopTimeout / poll 是停止等待参数（默认 5s / 100ms；单测可缩短，避免拖慢用例）。
	stopTimeout time.Duration
	poll        time.Duration
	mu          sync.Mutex
}

// NewGuard 构造守护实例：io 为 nil 时使用当前平台实现。
func NewGuard(stateRoot string, io GuardIO, resolver BinResolver) *Guard {
	if io == nil {
		io = NewSystemGuardIO()
	}
	root := filepath.Join(filepath.Clean(stateRoot), GuardStateDir)
	return &Guard{stateRoot: filepath.Clean(stateRoot), root: root, io: io, resolver: resolver,
		now: time.Now, stopTimeout: GuardStopTimeout, poll: GuardStopPoll}
}

// Root 返回守护数据目录（前端与文档用来解释日志位置）。
func (g *Guard) Root() string { return g.root }

// appsPath / statePath / logPath 是三个落盘路径的唯一来源。
func (g *Guard) appsPath() string             { return filepath.Join(g.root, GuardAppsFile) }
func (g *Guard) statePath(name string) string { return filepath.Join(g.root, name+".state.json") }
func (g *Guard) LogPath(name string) string   { return filepath.Join(g.root, name+".log") }

// Add 校验并注册（或更新）一个应用；不启动进程，不碰宿主以外的路径。
func (g *Guard) Add(spec AppSpec) (AppSpec, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	cleaned, err := g.validateSpec(spec)
	if err != nil {
		return AppSpec{}, err
	}
	file, err := g.loadApps()
	if err != nil {
		return AppSpec{}, err
	}
	replaced := false
	for i := range file.Apps {
		if file.Apps[i].Name == cleaned.Name {
			file.Apps[i] = cleaned
			replaced = true
			break
		}
	}
	if !replaced {
		if len(file.Apps) >= GuardMaxApps {
			return AppSpec{}, guardInvalid("守护应用已达上限 %d 个（单机单进程守护，不做编排）", GuardMaxApps)
		}
		file.Apps = append(file.Apps, cleaned)
	}
	sort.Slice(file.Apps, func(i, j int) bool { return file.Apps[i].Name < file.Apps[j].Name })
	file.UpdatedAt = g.now().UTC()
	if err := g.saveApps(file); err != nil {
		return AppSpec{}, err
	}
	return cleaned, nil
}

// Remove 注销一个应用；仍在运行时拒绝，避免留下无主进程。
func (g *Guard) Remove(name string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	file, err := g.loadApps()
	if err != nil {
		return err
	}
	index := -1
	for i := range file.Apps {
		if file.Apps[i].Name == name {
			index = i
			break
		}
	}
	if index < 0 {
		return ErrGuardNotConfigured
	}
	if g.running(file.Apps[index].Name) {
		return guardInvalid("应用 %q 仍在运行，请先停止再删除", name)
	}
	file.Apps = append(file.Apps[:index], file.Apps[index+1:]...)
	file.UpdatedAt = g.now().UTC()
	if err := g.saveApps(file); err != nil {
		return err
	}
	_ = os.Remove(g.statePath(name))
	return nil
}

// List 返回全部守护应用及其当前状态（未注册时返回空列表，不报错）。
func (g *Guard) List() (GuardListResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	file, err := g.loadApps()
	if err != nil {
		return GuardListResult{}, err
	}
	result := GuardListResult{Apps: make([]GuardApp, 0, len(file.Apps)), Root: g.root}
	for _, spec := range file.Apps {
		result.Apps = append(result.Apps, g.describe(spec))
	}
	result.Count = len(result.Apps)
	if result.Count == 0 {
		result.Hint = GuardEmptyHint
	}
	return result, nil
}

// Start 启动指定应用；names 为空表示「全部启动」。已运行的应用原样返回（幂等）。
func (g *Guard) Start(names []string) (GuardActionResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	specs, err := g.targets(names)
	if err != nil {
		return GuardActionResult{}, err
	}
	result := GuardActionResult{Action: "start", Apps: make([]GuardApp, 0, len(specs))}
	if len(specs) == 0 {
		result.Hint = GuardEmptyHint
	}
	for _, spec := range specs {
		app := g.startOne(spec)
		if app.State == GuardRunning {
			result.Started++
			if app.AlreadyRunning {
				result.AlreadyRunning++
				result.Started--
			}
		} else if app.Error != "" {
			result.Failed++
		}
		result.Apps = append(result.Apps, app)
	}
	result.Count = len(result.Apps)
	return result, nil
}

// Stop 停止指定应用；names 为空表示「全部停止」。未在运行的应用原样返回（幂等）。
func (g *Guard) Stop(names []string) (GuardActionResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	specs, err := g.targets(names)
	if err != nil {
		return GuardActionResult{}, err
	}
	result := GuardActionResult{Action: "stop", Apps: make([]GuardApp, 0, len(specs))}
	if len(specs) == 0 {
		result.Hint = GuardEmptyHint
	}
	for _, spec := range specs {
		app := g.stopOne(spec)
		switch {
		case app.Error != "":
			result.Failed++
		case app.State != GuardRunning:
			result.Stopped++ // 已停止 / 已退出都算「现在是停的」，停止是幂等操作
		}
		result.Apps = append(result.Apps, app)
	}
	result.Count = len(result.Apps)
	return result, nil
}

// Restart 重启一个应用：先停后启。名称必填（重启全部等于先停全部再启全部，语义含糊）。
func (g *Guard) Restart(name string) (GuardActionResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	spec, err := g.lookup(name)
	if err != nil {
		return GuardActionResult{}, err
	}
	stopped := g.stopOne(spec)
	if stopped.Error != "" {
		return GuardActionResult{}, errors.New(stopped.Error)
	}
	started := g.startOne(spec)
	started.Restarted = true
	result := GuardActionResult{Action: "restart", Apps: []GuardApp{started}, Count: 1}
	if started.State == GuardRunning {
		result.Started++
	} else {
		result.Failed++
	}
	return result, nil
}

// Logs 返回一个应用的日志尾部（默认 100 行，最多 500 行 / 64 KiB）。
func (g *Guard) Logs(name string, lines int) (GuardLogsResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, err := g.lookup(name); err != nil {
		return GuardLogsResult{}, err
	}
	if lines <= 0 {
		lines = GuardDefaultLogLines
	}
	if lines > GuardMaxLogLines {
		lines = GuardMaxLogLines
	}
	path := g.LogPath(name)
	result := GuardLogsResult{Name: name, Path: path, Lines: []string{}, Requested: lines}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			result.Hint = "还没有日志：应用尚未启动过"
			return result, nil
		}
		return GuardLogsResult{}, fmt.Errorf("打开日志失败: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, statErr := file.Stat()
	if statErr != nil {
		return GuardLogsResult{}, fmt.Errorf("读取日志属性失败: %w", statErr)
	}
	result.Bytes = info.Size()
	offset := int64(0)
	if info.Size() > GuardMaxLogBytes {
		offset = info.Size() - GuardMaxLogBytes
		result.Truncated = true
	}
	if _, err := file.Seek(offset, 0); err != nil {
		return GuardLogsResult{}, fmt.Errorf("定位日志失败: %w", err)
	}
	buffer := make([]byte, GuardMaxLogBytes)
	read, err := file.Read(buffer)
	if err != nil && read == 0 {
		return GuardLogsResult{}, fmt.Errorf("读取日志失败: %w", err)
	}
	text := string(buffer[:read])
	if offset > 0 {
		if index := strings.IndexByte(text, '\n'); index >= 0 {
			text = text[index+1:] // 丢掉被截断的首行
		}
	}
	all := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(all) == 1 && all[0] == "" {
		all = nil
	}
	result.TotalLines = len(all)
	from := 0
	if len(all) > lines {
		from = len(all) - lines
	}
	result.Lines = all[from:]
	result.Returned = len(result.Lines)
	return result, nil
}

// targets 解析 names：空表示全部应用；任一名称未注册即整体报错。
func (g *Guard) targets(names []string) ([]AppSpec, error) {
	file, err := g.loadApps()
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		// 零参数 = 全部：没有注册任何应用时是「无事可做」的成功（空结果 + hint），
		// 不是参数错误——用户点「全部启动」但还没添加应用时不该看到红字报错（G-33 A1）。
		return file.Apps, nil
	}
	specs := make([]AppSpec, 0, len(names))
	for _, name := range names {
		spec, ok := findApp(file.Apps, name)
		if !ok {
			return nil, fmt.Errorf("%w（%s）", ErrGuardNotConfigured, name)
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// lookup 按名称取一个应用声明。
func (g *Guard) lookup(name string) (AppSpec, error) {
	file, err := g.loadApps()
	if err != nil {
		return AppSpec{}, err
	}
	spec, ok := findApp(file.Apps, name)
	if !ok {
		return AppSpec{}, fmt.Errorf("%w（%s）", ErrGuardNotConfigured, name)
	}
	return spec, nil
}

// startOne 拉起一个应用并落盘运行态；已运行时返回 already_running（幂等 ensure）。
func (g *Guard) startOne(spec AppSpec) GuardApp {
	app := g.describe(spec)
	if app.State == GuardRunning {
		app.AlreadyRunning = true
		app.Hint = "已在运行，未重复启动"
		return app
	}
	bin, err := g.resolveBin(spec.Version)
	if err != nil {
		app.State = GuardStopped
		app.Error = err.Error()
		app.Hint = "先在「版本」区安装对应运行时，或清空版本改用默认版本"
		return app
	}
	state := g.loadState(spec.Name)
	logPath := g.LogPath(spec.Name)
	if err := os.MkdirAll(g.root, GuardDirMode); err != nil {
		app.Error = "创建守护目录失败: " + err.Error()
		return app
	}
	header := fmt.Sprintf("==== kore runtime-guard: 启动 %s（%s）\n==== 命令: %s %s\n",
		spec.Name, g.now().Format(time.RFC3339), bin, strings.Join(append([]string{spec.Script}, spec.Args...), " "))
	if err := appendLogHeader(logPath, header); err != nil {
		app.Error = "写日志失败: " + err.Error()
		return app
	}
	args := append([]string{spec.Script}, spec.Args...)
	spawned, err := g.io.Spawn(SpawnSpec{Bin: bin, Args: args, Dir: spec.Dir, LogPath: logPath,
		OnExit: func(code int) { g.recordExit(spec.Name, code) }})
	if err != nil {
		state.LastError = err.Error()
		state.PID = 0
		state.StartTicks = 0
		_ = g.saveState(spec.Name, state)
		app.Error = "启动失败: " + err.Error()
		app.Hint = "检查入口脚本是否存在、运行时是否有执行权限，以及端口是否被占用"
		return app
	}
	state = appState{PID: spawned.PID, StartTicks: spawned.StartTicks, StartedAt: g.now().UTC(), LogPath: logPath}
	if err := g.saveState(spec.Name, state); err != nil {
		app.Error = "保存运行状态失败: " + err.Error()
		return app
	}
	return g.describe(spec)
}

// stopOne 停止一个应用：SIGTERM → 等待 → SIGKILL；未在运行时幂等返回。
func (g *Guard) stopOne(spec AppSpec) GuardApp {
	app := g.describe(spec)
	state := g.loadState(spec.Name)
	if app.State != GuardRunning || state.PID <= 0 {
		// 本来就没在跑：保留「已退出（带退出码）」的显示，别把崩溃记录伪装成「已停止」。
		app.PID = 0
		app.Hint = "当前未在运行"
		return app
	}
	_ = g.io.Signal(state.PID, false)
	if g.waitGone(state, g.stopTimeout) {
		g.finishStop(spec.Name, state)
		return g.describe(spec)
	}
	_ = g.io.Signal(state.PID, true)
	if g.waitGone(state, GuardKillTimeout) {
		g.finishStop(spec.Name, state)
		app = g.describe(spec)
		app.Hint = "进程未响应正常停止，已强制结束"
		return app
	}
	app.Error = "停止失败：进程仍在运行"
	app.Hint = "可能提示权限不足（进程属主不同）；用系统进程页确认后手动结束"
	return app
}

// waitGone 在一次调用内轮询等待进程消失（不是常驻 ticker）。
func (g *Guard) waitGone(state appState, timeout time.Duration) bool {
	deadline := g.now().Add(timeout)
	for {
		if _, ok := g.io.Probe(state.PID); !ok {
			return true
		}
		if !g.now().Before(deadline) {
			return false
		}
		time.Sleep(g.poll)
	}
}

// finishStop 落盘「已停止」状态（保留最后一次错误便于解释崩溃原因）。
func (g *Guard) finishStop(name string, state appState) {
	state.PID = 0
	state.StartTicks = 0
	state.ExitCode = 0
	state.LastError = ""
	state.StoppedAt = g.now().UTC()
	_ = g.saveState(name, state)
}

// recordExit 在子进程退出时落盘退出码（只在该 PID 仍是当前记录时写，避免覆盖重启后的新进程）。
func (g *Guard) recordExit(name string, code int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.loadState(name)
	if state.PID <= 0 {
		return
	}
	state.ExitCode = code
	state.PID = 0
	state.StartTicks = 0
	state.StoppedAt = g.now().UTC()
	state.LastError = fmt.Sprintf("进程已退出（退出码 %d）", code)
	_ = g.saveState(name, state)
}

// describe 计算一个应用的当前状态（不启动、不停止）。
func (g *Guard) describe(spec AppSpec) GuardApp {
	state := g.loadState(spec.Name)
	app := GuardApp{
		Name: spec.Name, Dir: spec.Dir, Script: spec.Script, Args: spec.Args,
		Version: spec.Version, LogPath: g.LogPath(spec.Name),
		State: GuardStopped, LastError: state.LastError, ExitCode: state.ExitCode,
	}
	if info, err := os.Stat(app.LogPath); err == nil {
		app.LogBytes = info.Size()
	}
	if state.PID <= 0 {
		if !state.StoppedAt.IsZero() || state.LastError != "" {
			app.State = GuardCrashed
			if state.LastError == "" {
				app.State = GuardStopped
			}
		}
		app.Hint = GuardStartHint
		return app
	}
	info, alive := g.io.Probe(state.PID)
	if !alive || (state.StartTicks != 0 && info.StartTicks != 0 && info.StartTicks != state.StartTicks) {
		app.State = GuardCrashed
		app.PID = 0
		app.Hint = GuardCrashedHint
		return app
	}
	app.State = GuardRunning
	app.PID = state.PID
	app.RSSKB = info.RSSKB
	if !state.StartedAt.IsZero() {
		app.UptimeS = int64(g.now().Sub(state.StartedAt).Seconds())
		if app.UptimeS < 0 {
			app.UptimeS = 0
		}
	}
	app.Hint = "运行中；日志见「日志」"
	return app
}

// running 是 describe 的布尔快捷方式。
func (g *Guard) running(name string) bool {
	return g.describe(AppSpec{Name: name}).State == GuardRunning
}

// resolveBin 解析可执行文件：显式版本优先，其次钉住的默认版本，最后运行时默认。
func (g *Guard) resolveBin(version string) (string, error) {
	if g.resolver == nil {
		return "", errors.New("运行时版本解析不可用，请刷新后重试")
	}
	if version == "" {
		if pin, ok := readPin(g.stateRoot); ok {
			version = pin.Version
		}
	}
	bin, err := g.resolver.ResolveBin(version)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(bin) == "" {
		return "", errors.New("未解析到可执行文件")
	}
	return bin, nil
}

// validateSpec 校验应用声明：名称、目录白名单、入口脚本归属与参数形状。
func (g *Guard) validateSpec(spec AppSpec) (AppSpec, error) {
	name := strings.TrimSpace(spec.Name)
	if !guardAppNamePattern.MatchString(name) {
		return AppSpec{}, guardInvalid("应用名 %q 不合法：请用小写字母开头、只用小写字母/数字/连字符（2–32 位），例如 my-api", spec.Name)
	}
	dir, perr := ValidateRoot(spec.Dir, g.stateRoot)
	if perr != nil {
		return AppSpec{}, guardInvalid("目录不可用：%s", perr.Message)
	}
	script := strings.TrimSpace(spec.Script)
	if script == "" {
		return AppSpec{}, guardInvalid("请填写入口脚本（例如 app.py）")
	}
	if strings.ContainsRune(script, 0) || strings.Contains(script, "\\") {
		return AppSpec{}, guardInvalid("入口脚本含非法字符")
	}
	for _, segment := range strings.Split(script, "/") {
		if segment == ".." {
			return AppSpec{}, guardInvalid("入口脚本不能含 .. 段（请在「目录」里指定工作目录）")
		}
	}
	full := script
	if !filepath.IsAbs(full) {
		full = filepath.Join(dir, full)
	}
	full = filepath.Clean(full)
	if !within(dir, full) {
		return AppSpec{}, guardInvalid("入口脚本必须位于「目录」之内")
	}
	info, err := os.Stat(full)
	if err != nil || !info.Mode().IsRegular() {
		return AppSpec{}, guardInvalid("入口脚本不存在或不是文件：%s", full)
	}
	if len(spec.Args) > GuardMaxArgs {
		return AppSpec{}, guardInvalid("附加参数最多 %d 个", GuardMaxArgs)
	}
	args := make([]string, 0, len(spec.Args))
	for _, arg := range spec.Args {
		if len(arg) > GuardMaxArgLen {
			return AppSpec{}, guardInvalid("参数过长（上限 %d 字符）：%s", GuardMaxArgLen, arg)
		}
		if strings.ContainsRune(arg, 0) {
			return AppSpec{}, guardInvalid("参数含 NUL 字节")
		}
		args = append(args, arg)
	}
	version := strings.TrimSpace(spec.Version)
	return AppSpec{Name: name, Dir: dir, Script: full, Args: args, Version: version}, nil
}

// loadApps 读取应用注册表；文件缺失视为空表（首次使用）。
func (g *Guard) loadApps() (appsFile, error) {
	var file appsFile
	data, err := os.ReadFile(g.appsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return appsFile{Apps: []AppSpec{}}, nil
		}
		return appsFile{}, fmt.Errorf("读取守护列表失败: %w", err)
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return appsFile{}, fmt.Errorf("守护列表损坏（%s）: %w", g.appsPath(), err)
	}
	if file.Apps == nil {
		file.Apps = []AppSpec{}
	}
	return file, nil
}

// saveApps 原子写应用注册表（临时文件 + rename，避免半截文件）。
func (g *Guard) saveApps(file appsFile) error {
	if err := os.MkdirAll(g.root, GuardDirMode); err != nil {
		return fmt.Errorf("创建守护目录失败: %w", err)
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化守护列表失败: %w", err)
	}
	return writeFileAtomic(g.appsPath(), append(data, '\n'))
}

// loadState 读取运行态；文件缺失或损坏都按「无运行态」处理（不影响列表可用）。
func (g *Guard) loadState(name string) appState {
	var state appState
	data, err := os.ReadFile(g.statePath(name))
	if err != nil {
		return appState{}
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return appState{}
	}
	return state
}

// saveState 原子写运行态。
func (g *Guard) saveState(name string, state appState) error {
	if err := os.MkdirAll(g.root, GuardDirMode); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(g.statePath(name), append(data, '\n'))
}

// appendLogHeader 在日志尾部追加一行启动标记（只写一次，不做日志轮转）。
func appendLogHeader(path, header string) error {
	if err := os.MkdirAll(filepath.Dir(path), GuardDirMode); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, GuardLogFileMode)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	_, err = file.WriteString(header)
	return err
}

// writeFileAtomic 写临时文件后 rename，避免读者看到半截内容。
func writeFileAtomic(path string, data []byte) error {
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, GuardLogFileMode); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}

// findApp 在注册表里按名称查找。
func findApp(apps []AppSpec, name string) (AppSpec, bool) {
	for _, app := range apps {
		if app.Name == name {
			return app, true
		}
	}
	return AppSpec{}, false
}
