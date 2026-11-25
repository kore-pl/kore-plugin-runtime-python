package pyop

// 进程守护的 in-process 用例：全部走假 GuardIO，不拉起真实子进程、不碰宿主进程表，
// 也不读写状态目录以外的路径（G-29）。状态目录用 t.TempDir()。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// fakeGuardIO 是进程操作面的假实现：记录调用、按 alive 表回答存活。
type fakeGuardIO struct {
	spawns   []SpawnSpec
	signals  []fakeSignal
	alive    map[int]bool
	nextPID  int
	startTk  uint64
	spawnErr error
	// alwaysAlive 为 true 时 Probe 恒返回存活（验证 SIGKILL 升级路径）。
	alwaysAlive bool
}

type fakeSignal struct {
	pid   int
	force bool
}

func newFakeGuardIO() *fakeGuardIO {
	return &fakeGuardIO{alive: map[int]bool{}, nextPID: 4200, startTk: 999}
}

func (f *fakeGuardIO) Spawn(spec SpawnSpec) (SpawnResult, error) {
	if f.spawnErr != nil {
		return SpawnResult{}, f.spawnErr
	}
	f.spawns = append(f.spawns, spec)
	f.nextPID++
	f.alive[f.nextPID] = true
	return SpawnResult{PID: f.nextPID, StartTicks: f.startTk}, nil
}

func (f *fakeGuardIO) Signal(pid int, force bool) error {
	f.signals = append(f.signals, fakeSignal{pid: pid, force: force})
	if force {
		delete(f.alive, pid)
	}
	return nil
}

func (f *fakeGuardIO) Probe(pid int) (ProcInfo, bool) {
	if f.alwaysAlive {
		return ProcInfo{StartTicks: f.startTk}, true
	}
	if f.alive[pid] {
		return ProcInfo{StartTicks: f.startTk, RSSKB: 20480}, true
	}
	return ProcInfo{}, false
}

// fakeResolver 把版本号映射到假的可执行文件路径。
type fakeResolver struct {
	paths map[string]string
}

func (f fakeResolver) ResolveBin(version string) (string, error) {
	if path, ok := f.paths[version]; ok {
		return path, nil
	}
	if version == "" {
		return "/usr/bin/python3", nil
	}
	return "", fmt.Errorf("%w: %s", ErrVersionNotInstalled, version)
}

// newTestGuard 构造一套「状态目录 + 假 IO + 假解析器」的守护实例。
func newTestGuard(t *testing.T) (*Guard, *fakeGuardIO, string) {
	t.Helper()
	root := t.TempDir()
	io := newFakeGuardIO()
	guard := NewGuard(root, io, fakeResolver{paths: map[string]string{"3.12.4": "/opt/py312/bin/python3"}})
	guard.stopTimeout = 200 * time.Millisecond
	guard.poll = 5 * time.Millisecond
	return guard, io, root
}

// writeApp 在目录里写一个可执行的入口脚本。
func writeApp(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	path := filepath.Join(dir, "app.py")
	if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
		t.Fatalf("写入口脚本失败: %v", err)
	}
	return dir
}

// TestGuardZeroParamStartAll 是 G-33 A1 的 in-process 回归证明：
// 零参数写操作 runtime-python.guard.start（空对象 = 全部启动）不得返回 E_INVALID。
func TestGuardZeroParamStartAll(t *testing.T) {
	guard, io, root := newTestGuard(t)
	dir := writeApp(t, root, "api", "console.log('hi')")

	// 场景一：还没添加任何应用 → 零参数启动是「无事可做」的空成功。
	raw, perr := Call("runtime-python.guard.start", json.RawMessage("{}"), root, nil, WithGuard(guard))
	if perr != nil {
		t.Fatalf("空列表零参数启动不应报错: code=%s message=%s", perr.Code, perr.Message)
	}
	var empty GuardActionResult
	if err := json.Unmarshal(raw, &empty); err != nil {
		t.Fatalf("解析空结果失败: %v", err)
	}
	if empty.Count != 0 || empty.Hint == "" {
		t.Fatalf("空列表应返回 count=0 + hint，实际 %+v", empty)
	}
	if len(io.spawns) != 0 {
		t.Fatalf("没有注册应用时不应拉起进程，实际 %d 次", len(io.spawns))
	}

	// 场景二：注册一个应用后零参数启动 → 真的拉起，且不是 E_INVALID。
	if _, perr := Call("runtime-python.guard.add", mustJSON(t, guardAddParams{
		Name: "api", Dir: dir, Script: "app.py", Version: "3.12.4",
	}), root, nil, WithGuard(guard)); perr != nil {
		t.Fatalf("添加应用失败: %+v", perr)
	}
	raw, perr = Call("runtime-python.guard.start", json.RawMessage("{}"), root, nil, WithGuard(guard))
	if perr != nil {
		t.Fatalf("零参数启动写操作返回错误（A1）: code=%s message=%s", perr.Code, perr.Message)
	}
	var result GuardActionResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析启动结果失败: %v", err)
	}
	if result.Started != 1 || len(io.spawns) != 1 {
		t.Fatalf("应当拉起 1 个进程: result=%+v spawns=%d", result, len(io.spawns))
	}
	spawn := io.spawns[0]
	if spawn.Bin != "/opt/py312/bin/python3" {
		t.Fatalf("应使用指定版本的可执行文件，实际 %q", spawn.Bin)
	}
	if spawn.Dir != dir || spawn.Args[0] != filepath.Join(dir, "app.py") {
		t.Fatalf("拉起参数不对: %+v", spawn)
	}
	if spawn.LogPath != filepath.Join(root, GuardStateDir, "api.log") {
		t.Fatalf("日志路径不对: %q", spawn.LogPath)
	}
}

// TestGuardLifecycle 覆盖 list / start（幂等）/ stop / restart 的状态流转。
func TestGuardLifecycle(t *testing.T) {
	guard, io, root := newTestGuard(t)
	dir := writeApp(t, root, "web", "console.log('web')")
	if _, perr := Call("runtime-python.guard.add", mustJSON(t, guardAddParams{
		Name: "web", Dir: dir, Script: "app.py",
	}), root, nil, WithGuard(guard)); perr != nil {
		t.Fatalf("添加失败: %+v", perr)
	}

	// 启动一次 → running，PID 落盘。
	startOnce := func() GuardApp {
		t.Helper()
		raw, perr := Call("runtime-python.guard.start", json.RawMessage(`{"names":["web"]}`), root, nil, WithGuard(guard))
		if perr != nil {
			t.Fatalf("启动失败: %+v", perr)
		}
		var result GuardActionResult
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		return result.Apps[0]
	}
	app := startOnce()
	if app.State != GuardRunning || app.PID == 0 || app.RSSKB == 0 {
		t.Fatalf("启动后状态不对: %+v", app)
	}
	state := guard.loadState("web")
	if state.PID != app.PID || state.StartTicks == 0 {
		t.Fatalf("运行态未落盘: %+v", state)
	}

	// 幂等：再启动一次不重复拉起。
	if again := startOnce(); again.State != GuardRunning || !again.AlreadyRunning {
		t.Fatalf("重复启动应返回 already_running: %+v", again)
	}
	if len(io.spawns) != 1 {
		t.Fatalf("重复启动不应再次拉起进程，实际 spawns=%d", len(io.spawns))
	}

	// list 能读到运行中的状态。
	raw, perr := Call("runtime-python.guard.list", json.RawMessage("{}"), root, nil, WithGuard(guard))
	if perr != nil {
		t.Fatalf("列表失败: %+v", perr)
	}
	var list GuardListResult
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("解析列表失败: %v", err)
	}
	if list.Count != 1 || list.Apps[0].State != GuardRunning {
		t.Fatalf("列表状态不对: %+v", list)
	}
	if list.Root != filepath.Join(root, GuardStateDir) {
		t.Fatalf("守护目录不对: %q", list.Root)
	}

	// 重启 = 先停后启，PID 变化。
	raw, perr = Call("runtime-python.guard.restart", json.RawMessage(`{"name":"web"}`), root, nil, WithGuard(guard))
	if perr != nil {
		t.Fatalf("重启失败: %+v", perr)
	}
	var restarted GuardActionResult
	if err := json.Unmarshal(raw, &restarted); err != nil {
		t.Fatalf("解析重启结果失败: %v", err)
	}
	if len(restarted.Apps) != 1 || restarted.Apps[0].State != GuardRunning || !restarted.Apps[0].Restarted {
		t.Fatalf("重启结果不对: %+v", restarted)
	}
	if restarted.Apps[0].PID == app.PID {
		t.Fatalf("重启后 PID 应变化，实际仍是 %d", app.PID)
	}

	// 停止 → stopped，运行态清零。
	raw, perr = Call("runtime-python.guard.stop", json.RawMessage("{}"), root, nil, WithGuard(guard))
	if perr != nil {
		t.Fatalf("停止失败: %+v", perr)
	}
	var stopped GuardActionResult
	if err := json.Unmarshal(raw, &stopped); err != nil {
		t.Fatalf("解析停止结果失败: %v", err)
	}
	if stopped.Stopped != 1 || stopped.Apps[0].State != GuardStopped || stopped.Apps[0].PID != 0 {
		t.Fatalf("停止结果不对: %+v", stopped)
	}
	if state := guard.loadState("web"); state.PID != 0 || state.StoppedAt.IsZero() {
		t.Fatalf("停止后运行态未清零: %+v", state)
	}
}

// TestGuardStopEscalatesToKill 验证 SIGTERM 无效时升级 SIGKILL（进程不退会让停止变假成功）。
func TestGuardStopEscalatesToKill(t *testing.T) {
	guard, io, root := newTestGuard(t)
	dir := writeApp(t, root, "stuck", "while(true){}")
	if _, perr := Call("runtime-python.guard.add", mustJSON(t, guardAddParams{
		Name: "stuck", Dir: dir, Script: "app.py",
	}), root, nil, WithGuard(guard)); perr != nil {
		t.Fatalf("添加失败: %+v", perr)
	}
	if _, perr := Call("runtime-python.guard.start", json.RawMessage("{}"), root, nil, WithGuard(guard)); perr != nil {
		t.Fatalf("启动失败: %+v", perr)
	}
	io.alwaysAlive = true // 假装进程无视 SIGTERM
	go func() {
		time.Sleep(60 * time.Millisecond)
		io.alwaysAlive = false // 被 SIGKILL 后才消失
	}()
	raw, perr := Call("runtime-python.guard.stop", json.RawMessage(`{"names":["stuck"]}`), root, nil, WithGuard(guard))
	if perr != nil {
		t.Fatalf("停止失败: %+v", perr)
	}
	var result GuardActionResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(io.signals) < 2 || io.signals[0].force || !io.signals[1].force {
		t.Fatalf("应先 SIGTERM 再 SIGKILL，实际 %+v", io.signals)
	}
	if result.Apps[0].State != GuardStopped {
		t.Fatalf("强制结束后应为 stopped: %+v", result.Apps[0])
	}
}

// TestGuardLogsTail 验证日志尾部读取（行数、顺序、日志不存在时的空状态）。
func TestGuardLogsTail(t *testing.T) {
	guard, _, root := newTestGuard(t)
	dir := writeApp(t, root, "logs", "console.log(1)")
	if _, perr := Call("runtime-python.guard.add", mustJSON(t, guardAddParams{
		Name: "logs", Dir: dir, Script: "app.py",
	}), root, nil, WithGuard(guard)); perr != nil {
		t.Fatalf("添加失败: %+v", perr)
	}
	// 日志不存在 → 空列表 + hint，不报错。
	raw, perr := Call("runtime-python.guard.logs", json.RawMessage(`{"name":"logs"}`), root, nil, WithGuard(guard))
	if perr != nil {
		t.Fatalf("读空日志不应报错: %+v", perr)
	}
	var empty GuardLogsResult
	if err := json.Unmarshal(raw, &empty); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(empty.Lines) != 0 || empty.Hint == "" {
		t.Fatalf("空日志应返回空行 + hint: %+v", empty)
	}

	var buffer strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&buffer, "line %d\n", i)
	}
	if err := os.MkdirAll(filepath.Join(root, GuardStateDir), 0o750); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, GuardStateDir, "logs.log"), []byte(buffer.String()), 0o640); err != nil {
		t.Fatalf("写日志失败: %v", err)
	}
	raw, perr = Call("runtime-python.guard.logs", json.RawMessage(`{"name":"logs","lines":3}`), root, nil, WithGuard(guard))
	if perr != nil {
		t.Fatalf("读日志失败: %+v", perr)
	}
	var tail GuardLogsResult
	if err := json.Unmarshal(raw, &tail); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(tail.Lines) != 3 || tail.Lines[0] != "line 198" || tail.Lines[2] != "line 200" {
		t.Fatalf("日志尾部不对: %+v", tail.Lines)
	}
	if tail.TotalLines != 200 || tail.Bytes == 0 {
		t.Fatalf("日志元信息不对: %+v", tail)
	}
}

// TestGuardAddValidation 验证应用声明的校验与「怎么办」文案。
func TestGuardAddValidation(t *testing.T) {
	guard, _, root := newTestGuard(t)
	dir := writeApp(t, root, "ok", "console.log(1)")
	outside := t.TempDir() // 不在白名单（不是 <state> 也不是 /var/www /opt /home）
	cases := []struct {
		name   string
		params guardAddParams
		want   string
	}{
		{"名称非法", guardAddParams{Name: "Bad Name", Dir: dir, Script: "app.py"}, "应用名"},
		{"缺入口", guardAddParams{Name: "ok", Dir: dir}, "入口脚本"},
		{"穿越", guardAddParams{Name: "ok", Dir: dir, Script: "../etc/passwd"}, ".."},
		{"目录越界", guardAddParams{Name: "ok", Dir: outside, Script: "app.py"}, "允许目录"},
		{"脚本不存在", guardAddParams{Name: "ok", Dir: dir, Script: "nope.js"}, "不存在"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, perr := Call("runtime-python.guard.add", mustJSON(t, c.params), root, nil, WithGuard(guard))
			if perr == nil {
				t.Fatalf("应当被拒绝")
			}
			if perr.Code != pluginapi.E_INVALID {
				t.Fatalf("应返回 E_INVALID，实际 %s", perr.Code)
			}
			if !strings.Contains(perr.Message, c.want) {
				t.Fatalf("错误信息 %q 未含 %q", perr.Message, c.want)
			}
		})
	}

	// 删除仍在运行的应用要拒绝，避免留下无主进程。
	if _, perr := Call("runtime-python.guard.add", mustJSON(t, guardAddParams{
		Name: "ok", Dir: dir, Script: "app.py",
	}), root, nil, WithGuard(guard)); perr != nil {
		t.Fatalf("添加失败: %+v", perr)
	}
	if _, perr := Call("runtime-python.guard.start", json.RawMessage("{}"), root, nil, WithGuard(guard)); perr != nil {
		t.Fatalf("启动失败: %+v", perr)
	}
	if _, perr := Call("runtime-python.guard.remove", json.RawMessage(`{"name":"ok"}`), root, nil, WithGuard(guard)); perr == nil {
		t.Fatalf("运行中的应用不应允许删除")
	}
}

// TestUseVersionPin 验证「指定默认版本供站点使用」：钉住 → versions 回显 → 清除。
func TestUseVersionPin(t *testing.T) {
	root := t.TempDir()
	exec := &stubExecutor{versions: VersionsResult{
		Versions: []PythonVersion{
			{Version: "3.12.4", Path: "/opt/py312/bin/python3", BinDir: "/opt/node20/bin", IsDefault: true},
			{Version: "3.11.9", Path: "/opt/py311/bin/python3", BinDir: "/opt/node18/bin"},
		},
		DefaultVersion: "3.12.4",
	}}

	raw, perr := Call("runtime-python.use", json.RawMessage(`{"version":"3.11.9"}`), root, exec)
	if perr != nil {
		t.Fatalf("钉住版本失败: %+v", perr)
	}
	var used UseResult
	if err := json.Unmarshal(raw, &used); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if used.Version != "3.11.9" || used.Path != "/opt/py311/bin/python3" || used.Cleared {
		t.Fatalf("钉住结果不对: %+v", used)
	}
	if pin, ok := readPin(root); !ok || pin.Version != "3.11.9" {
		t.Fatalf("钉住记录未落盘: %+v", pin)
	}

	// versions 回显钉住版本（系统默认仍是 20.11.1，两者不混）。
	raw, perr = Call("runtime-python.versions", json.RawMessage("{}"), root, exec)
	if perr != nil {
		t.Fatalf("versions 失败: %+v", perr)
	}
	var versions VersionsResult
	if err := json.Unmarshal(raw, &versions); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if versions.PinnedVersion != "3.11.9" || versions.DefaultVersion != "3.12.4" {
		t.Fatalf("versions 未正确区分钉住与系统默认: %+v", versions)
	}

	// 未安装的版本 → E_INVALID（带已检测列表）。
	if _, perr := Call("runtime-python.use", json.RawMessage(`{"version":"3.13.0"}`), root, exec); perr == nil ||
		perr.Code != pluginapi.E_INVALID {
		t.Fatalf("未安装版本应返回 E_INVALID，实际 %+v", perr)
	}

	// 零参数 = 清除钉住，恢复跟随系统；这不是参数错误。
	raw, perr = Call("runtime-python.use", json.RawMessage("{}"), root, exec)
	if perr != nil {
		t.Fatalf("清除钉住失败: %+v", perr)
	}
	var cleared UseResult
	if err := json.Unmarshal(raw, &cleared); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !cleared.Cleared {
		t.Fatalf("应标记为已清除: %+v", cleared)
	}
	if _, ok := readPin(root); ok {
		t.Fatalf("清除后不应还有钉住记录")
	}
}

// TestGuardStartWithoutRuntime 运行时缺失时给「怎么办」提示而不是崩溃。
func TestGuardStartWithoutRuntime(t *testing.T) {
	root := t.TempDir()
	io := newFakeGuardIO()
	guard := NewGuard(root, io, nil) // 没有解析器：模拟执行面不可用
	dir := writeApp(t, root, "nores", "console.log(1)")
	if _, perr := Call("runtime-python.guard.add", mustJSON(t, guardAddParams{
		Name: "nores", Dir: dir, Script: "app.py",
	}), root, nil, WithGuard(guard)); perr != nil {
		t.Fatalf("添加失败: %+v", perr)
	}
	raw, perr := Call("runtime-python.guard.start", json.RawMessage("{}"), root, nil, WithGuard(guard))
	if perr != nil {
		t.Fatalf("单个应用启动失败不应整次报错: %+v", perr)
	}
	var result GuardActionResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if result.Failed != 1 || result.Apps[0].Error == "" || result.Apps[0].Hint == "" {
		t.Fatalf("应逐条报告失败与提示: %+v", result)
	}
	if len(io.spawns) != 0 {
		t.Fatalf("解析失败时不应拉起进程")
	}
}

// mustJSON 把入参编码为 JSON；编码失败直接终断用例。
func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("编码参数失败: %v", err)
	}
	return data
}

// TestGuardRecordsExitCode 子进程退出后状态必须是「已退出」并带退出码，不能一直显示运行中。
func TestGuardRecordsExitCode(t *testing.T) {
	guard, io, root := newTestGuard(t)
	dir := writeApp(t, root, "dying", "process.exit(3)")
	if _, perr := Call("runtime-python.guard.add", mustJSON(t, guardAddParams{
		Name: "dying", Dir: dir, Script: "app.py",
	}), root, nil, WithGuard(guard)); perr != nil {
		t.Fatalf("添加失败: %+v", perr)
	}
	if _, perr := Call("runtime-python.guard.start", json.RawMessage("{}"), root, nil, WithGuard(guard)); perr != nil {
		t.Fatalf("启动失败: %+v", perr)
	}
	spawned := io.spawns[0]
	if spawned.OnExit == nil {
		t.Fatal("拉起时必须注册退出回调（否则僵尸进程会被判成运行中）")
	}
	// 模拟子进程以退出码 3 结束（生产实现由 Wait 回收后回调）。
	delete(io.alive, io.nextPID)
	io.spawns[0].OnExit(3)

	raw, perr := Call("runtime-python.guard.list", json.RawMessage("{}"), root, nil, WithGuard(guard))
	if perr != nil {
		t.Fatalf("列表失败: %+v", perr)
	}
	var list GuardListResult
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if list.Apps[0].State != GuardCrashed || list.Apps[0].ExitCode != 3 {
		t.Fatalf("退出后应为 crashed + exit_code=3，实际 %+v", list.Apps[0])
	}
	if state := guard.loadState("dying"); state.PID != 0 {
		t.Fatalf("退出后运行态应清零: %+v", state)
	}
}
