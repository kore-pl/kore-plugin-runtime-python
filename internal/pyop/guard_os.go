//go:build !windows

package pyop

// 非 Windows 平台的守护进程操作面：os/exec 拉起（setsid 脱离会话）+ syscall 发信号 +
// /proc 读取启动时刻与 RSS（Linux）。macOS 上无 /proc，退化为「信号 0 探测存活」。

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// NewSystemGuardIO 返回当前平台的进程操作面。
func NewSystemGuardIO() GuardIO { return systemGuardIO{} }

// systemGuardIO 是本机实现（无 shell、固定 argv、日志追加）。
type systemGuardIO struct{}

// Spawn 拉起脱离会话的子进程：stdin 关闭、stdout/stderr 追加到 LogPath。
// 环境变量剔除全部 KORE_*（不得把插件令牌泄漏给被守护应用），PATH 前置运行时 bin 目录。
func (systemGuardIO) Spawn(spec SpawnSpec) (SpawnResult, error) {
	file, err := os.OpenFile(spec.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, GuardLogFileMode)
	if err != nil {
		return SpawnResult{}, fmt.Errorf("打开日志失败: %w", err)
	}
	defer func() { _ = file.Close() }()
	command := exec.Command(spec.Bin, spec.Args...)
	command.Dir = spec.Dir
	command.Env = childEnv(spec.Bin)
	command.Stdout = file
	command.Stderr = file
	command.Stdin = nil
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return SpawnResult{}, err
	}
	pid := command.Process.Pid
	startTicks, _ := procStartTicks(pid)
	// 事件驱动的回收 goroutine（阻塞在 Wait 上，不是 ticker / 周期循环，符合 G-19）：
	//   - 子进程退出后立刻被 Wait 回收，不留僵尸（否则父插件进程活着时 /proc 里一直是 Z 状态，
	//     状态页会把已死进程显示成「运行中」——2026-11-25 真机验证抓到的 bug）；
	//   - 退出码交给 OnExit 落盘，用户能看到「已退出（退出码 N）」。
	// 插件进程退出时该 goroutine 随进程结束，子进程被 init 收养，不影响应用继续运行。
	go func() {
		code := -1
		if err := command.Wait(); err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			}
		} else {
			code = 0
		}
		if spec.OnExit != nil {
			spec.OnExit(code)
		}
	}()
	return SpawnResult{PID: pid, StartTicks: startTicks}, nil
}

// Signal 向进程组发信号：子进程 setsid 后是组长，kill(-pid) 连带其子进程一起收，
// 避免「父进程退了子进程还在占端口」。组不存在时退回单 PID。
func (systemGuardIO) Signal(pid int, force bool) error {
	if pid <= 0 {
		return errors.New("非法 PID")
	}
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	if err := syscall.Kill(-pid, signal); err == nil {
		return nil
	} else if !errors.Is(err, syscall.ESRCH) {
		if singleErr := syscall.Kill(pid, signal); singleErr == nil {
			return nil
		}
		return err
	}
	return syscall.Kill(pid, signal)
}

// Probe 探测进程是否存活并返回启动时刻与 RSS。
func (systemGuardIO) Probe(pid int) (ProcInfo, bool) {
	if pid <= 0 {
		return ProcInfo{}, false
	}
	if runtime.GOOS != "linux" {
		return probeSignalZero(pid)
	}
	ticks, state, ok := procStat(pid)
	if !ok {
		return ProcInfo{}, false
	}
	// Z（僵尸，已退出未回收）/ X（已死）都算「不在运行」——进程已经结束，
	// 只是退出状态还没被父进程取走，不能当成还活着。
	if state == "Z" || state == "X" {
		return ProcInfo{}, false
	}
	return ProcInfo{StartTicks: ticks, RSSKB: procRSSKB(pid)}, true
}

// probeSignalZero 是 /proc 不可用时的存活探测（macOS）：信号 0 只做可达性判断。
func probeSignalZero(pid int) (ProcInfo, bool) {
	process, err := os.FindProcess(pid)
	if err != nil {
		return ProcInfo{}, false
	}
	if err := process.Signal(syscall.Signal(0)); err != nil {
		return ProcInfo{}, false
	}
	return ProcInfo{}, true
}

// procStartTicks 只取启动时刻（拉起后立即读，用于记录进程身份）。
func procStartTicks(pid int) (uint64, bool) {
	ticks, _, ok := procStat(pid)
	return ticks, ok
}

// procStat 读取 /proc/<pid>/stat 的状态字（第 3 字段）与启动时刻（第 22 字段）。
// 启动时刻与 PID 组合成「进程身份」，挡住 PID 复用导致的假运行；状态字用来识别僵尸。
func procStat(pid int) (uint64, string, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, "", false
	}
	text := string(data)
	index := strings.LastIndexByte(text, ')')
	if index < 0 || index+2 >= len(text) {
		return 0, "", false
	}
	fields := strings.Fields(text[index+2:])
	// comm 之后从第 3 个字段（state）开始；starttime 是整体第 22 字段 → 这里 offset 19。
	const starttimeOffset = 19
	if len(fields) <= starttimeOffset {
		return 0, "", false
	}
	ticks, err := strconv.ParseUint(fields[starttimeOffset], 10, 64)
	if err != nil {
		return 0, "", false
	}
	return ticks, fields[0], true
}

// procRSSKB 读取 /proc/<pid>/statm 的常驻页数并换算为 KiB；失败返回 0（详情降级）。
func procRSSKB(pid int) int64 {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * int64(os.Getpagesize()) / 1024
}

// childEnv 构造子进程环境：剔除 KORE_*（插件令牌等），PATH 前置运行时 bin 目录。
func childEnv(bin string) []string {
	base := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "KORE_") {
			continue
		}
		if strings.HasPrefix(kv, "PATH=") {
			continue
		}
		base = append(base, kv)
	}
	path := os.Getenv("PATH")
	if dir := dirOf(bin); dir != "" {
		if path != "" {
			path = dir + string(os.PathListSeparator) + path
		} else {
			path = dir
		}
	}
	if path != "" {
		base = append(base, "PATH="+path)
	}
	return base
}

// dirOf 返回可执行文件所在目录（bin 为空时返回空串）。
func dirOf(bin string) string {
	if bin == "" {
		return ""
	}
	index := strings.LastIndexByte(bin, '/')
	if index <= 0 {
		return ""
	}
	return bin[:index]
}
