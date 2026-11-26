package pyop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// venv / pip 的能力边界：只写插件状态目录（或白名单根）内的 venv，
// 不碰系统 site-packages；固定 argv、固定超时、输出截断。
const (
	// DefaultVenvSubdir 是默认 venv 根目录（相对插件状态目录）。
	DefaultVenvSubdir = "venvs"
	// VenvCommandTimeout 是 venv/pip 命令的硬超时（pip 联网可能较慢）。
	VenvCommandTimeout = 120 * time.Second
	// MaxVenvPackages 是单次安装的包数量上限。
	MaxVenvPackages = 50
	// MaxPipOutputBytes 是 pip 输出的保留上限，超出后保留首尾并标记 truncated。
	MaxPipOutputBytes = 64 * 1024
)

// packageSpecPattern 是包名白名单：字母数字开头，其余允许 . _ -，可选 ==version。
// 空格 / 分号 / 引号 / 前导 -（选项注入）一律不匹配；不接受 extras 语法。
var packageSpecPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}(==[A-Za-z0-9][A-Za-z0-9._-]{0,63})?$`)

// pythonVersionRequestPattern 与 info.version 的口径一致（x.y 或 x.y.z）。
var pythonVersionRequestPattern = regexp.MustCompile(`^\d+\.\d+(\.\d+)?$`)

var (
	ErrVenvNotFound = errors.New("venv 不存在")
	ErrVenvNoPip    = errors.New("venv 内缺少 pip")
	ErrVenvTimeout  = errors.New("venv/pip 命令超时")
)

// VenvSpec 是创建 venv 的已校验参数。
type VenvSpec struct {
	Path          string
	PythonVersion string
}

// PipInstallSpec 是安装依赖的已校验参数。
type PipInstallSpec struct {
	VenvPath string
	Packages []string
}

// VenvManager 是 venv/pip 执行面；真实实现走 os/exec，测试可注入假执行面。
type VenvManager interface {
	CreateVenv(root string, spec VenvSpec) (VenvCreateResult, error)
	InstallPackages(spec PipInstallSpec) (PipInstallResult, error)
	ListVenvs(root string) (VenvListResult, error)
	ListPackages(venvPath string) (PipListResult, error)
}

// VenvCreateResult 是 runtime-python.venv.create 的结果。
type VenvCreateResult struct {
	Path          string   `json:"path"`
	PythonVersion string   `json:"python_version"`
	Created       bool     `json:"created"`
	Packages      []string `json:"packages,omitempty"`
	NetworkUsed   bool     `json:"network_used"`
	Hint          string   `json:"hint,omitempty"`
}

// PipInstallResult 是 runtime-python.pip.install 的结果。
type PipInstallResult struct {
	VenvPath    string   `json:"venv_path"`
	Packages    []string `json:"packages"`
	Output      string   `json:"output"`
	Truncated   bool     `json:"truncated"`
	DurationMS  int64    `json:"duration_ms"`
	NetworkUsed bool     `json:"network_used"`
	Hint        string   `json:"hint,omitempty"`
}

// VenvInfo 是一个已存在的 venv。
type VenvInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Version string `json:"version"`
}

// VenvListResult 是 runtime-python.venv.list 的结果。
type VenvListResult struct {
	Root  string     `json:"root"`
	Venvs []VenvInfo `json:"venvs"`
	Count int        `json:"count"`
	Hint  string     `json:"hint,omitempty"`
}

// PackageInfo 是 venv 内已安装的一个包。
type PackageInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// PipListResult 是 runtime-python.pip.list 的结果。
type PipListResult struct {
	VenvPath string        `json:"venv_path"`
	Packages []PackageInfo `json:"packages"`
	Count    int           `json:"count"`
	Hint     string        `json:"hint,omitempty"`
}

type venvCreateParams struct {
	Path          string   `json:"path"`
	PythonVersion string   `json:"python_version"`
	Packages      []string `json:"packages"`
}

type pipInstallParams struct {
	VenvPath string   `json:"venv_path"`
	Packages []string `json:"packages"`
}

type venvListParams struct{}

type pipListParams struct {
	VenvPath string `json:"venv_path"`
}

func venvManager(exec Executor) VenvManager {
	if manager, ok := exec.(VenvManager); ok {
		return manager
	}
	return realVenvManager{}
}

func callVenvCreate(params json.RawMessage, stateRoot string, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p venvCreateParams
	if err := strictUnmarshal(params, &p); err != nil {
		return nil, invalid("runtime-python.venv.create 参数非法: " + err.Error())
	}
	target, perr := resolveVenvTarget(p.Path, stateRoot)
	if perr != nil {
		return nil, perr
	}
	version, perr := validatePythonVersion(p.PythonVersion)
	if perr != nil {
		return nil, perr
	}
	packages, perr := validatePackages(p.Packages, true)
	if perr != nil {
		return nil, perr
	}
	result, err := venvManager(exec).CreateVenv(stateRoot, VenvSpec{Path: target, PythonVersion: version})
	if err != nil {
		return nil, classify(err)
	}
	result.Path = target
	result.Packages = packages
	result.NetworkUsed = len(packages) > 0
	if !result.Created && result.Hint == "" {
		result.Hint = "目录已存在，未覆盖；如需重建请换一个 path"
	}
	return marshal(result)
}

func callPipInstall(params json.RawMessage, stateRoot string, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p pipInstallParams
	if err := strictUnmarshal(params, &p); err != nil {
		return nil, invalid("runtime-python.pip.install 参数非法: " + err.Error())
	}
	venvPath, perr := resolveExistingVenv(p.VenvPath, stateRoot)
	if perr != nil {
		return nil, perr
	}
	packages, perr := validatePackages(p.Packages, false)
	if perr != nil {
		return nil, perr
	}
	result, err := venvManager(exec).InstallPackages(PipInstallSpec{VenvPath: venvPath, Packages: packages})
	if err != nil {
		return nil, classify(err)
	}
	result.VenvPath = venvPath
	result.Packages = packages
	result.NetworkUsed = true
	if !result.Truncated && len(result.Output) > MaxPipOutputBytes {
		result.Output, result.Truncated = truncatePipOutput(result.Output)
	}
	if result.Hint == "" {
		result.Hint = "pip 默认从 PyPI 联网下载；失败时检查网络或配置镜像源"
	}
	return marshal(result)
}

func callVenvList(params json.RawMessage, stateRoot string, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p venvListParams
	if err := strictUnmarshal(params, &p); err != nil {
		return nil, invalid("runtime-python.venv.list 参数非法: " + err.Error())
	}
	result, err := venvManager(exec).ListVenvs(stateRoot)
	if err != nil {
		return nil, classify(err)
	}
	result.Root = stateRoot
	if result.Venvs == nil {
		result.Venvs = []VenvInfo{}
	}
	result.Count = len(result.Venvs)
	if result.Count == 0 && result.Hint == "" {
		result.Hint = "还没有 venv；用 runtime-python.venv.create 创建"
	}
	return marshal(result)
}

func callPipList(params json.RawMessage, stateRoot string, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p pipListParams
	if err := strictUnmarshal(params, &p); err != nil {
		return nil, invalid("runtime-python.pip.list 参数非法: " + err.Error())
	}
	venvPath, perr := resolveExistingVenv(p.VenvPath, stateRoot)
	if perr != nil {
		return nil, perr
	}
	result, err := venvManager(exec).ListPackages(venvPath)
	if err != nil {
		return nil, classify(err)
	}
	result.VenvPath = venvPath
	if result.Packages == nil {
		result.Packages = []PackageInfo{}
	}
	result.Count = len(result.Packages)
	return marshal(result)
}

// resolveVenvTarget 计算 venv 目标路径并做边界校验：
// 空值 → <state>/venvs/venv；显式路径必须是绝对路径、无 ..、落在允许根内，
// 且最近存在祖先经符号链接解析后仍在允许根内（挡软链逃逸）。
func resolveVenvTarget(raw, stateRoot string) (string, *pluginapi.Error) {
	if strings.TrimSpace(raw) == "" {
		if strings.TrimSpace(stateRoot) == "" {
			return filepath.Join(".", DefaultVenvSubdir, "venv"), nil
		}
		return filepath.Join(filepath.Clean(stateRoot), DefaultVenvSubdir, "venv"), nil
	}
	if raw != strings.TrimSpace(raw) {
		return "", invalid("path 不得含首尾空白")
	}
	if strings.ContainsRune(raw, 0) || strings.Contains(raw, "\\") {
		return "", invalid("path 含非法字符（NUL / 反斜杠）")
	}
	if !filepath.IsAbs(raw) {
		return "", invalid("path 必须是绝对路径")
	}
	for _, segment := range strings.Split(raw, "/") {
		if segment == ".." {
			return "", invalid("path 含 .. 段，禁止穿越")
		}
	}
	clean := filepath.Clean(raw)
	if !withinAny(allowedRoots(stateRoot), clean) {
		return "", venvPathOutOfRootHint(clean)
	}
	if err := ensureRealWithinRoots(clean, stateRoot); err != nil {
		return "", err
	}
	return clean, nil
}

// resolveExistingVenv 校验 venv 参数：必须存在、是目录、带 bin/pip、路径在允许根内。
func resolveExistingVenv(raw, stateRoot string) (string, *pluginapi.Error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", invalid("请填写必填字段 venv_path（虚拟环境目录）")
	}
	clean := filepath.Clean(trimmed)
	if !filepath.IsAbs(clean) || strings.Contains(clean, "\\") {
		return "", invalid("venv_path 请填绝对路径（以 / 开头）")
	}
	if !withinAny(allowedRoots(stateRoot), clean) {
		return "", venvPathOutOfRootHint(clean)
	}
	if err := ensureRealWithinRoots(clean, stateRoot); err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", invalid("虚拟环境目录 venv_path 不存在或无法访问，请先创建: " + err.Error())
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", invalid("venv_path 需要是已存在的目录，请先创建再试")
	}
	if _, err := os.Stat(filepath.Join(real, "bin", "pip")); err != nil {
		return "", invalid("venv_path 不是可用的虚拟环境（缺少 bin/pip），请重建环境")
	}
	return real, nil
}

// ensureRealWithinRoots 解析最近存在祖先的真实路径并确认仍在白名单根内。
func ensureRealWithinRoots(clean, stateRoot string) *pluginapi.Error {
	ancestor := clean
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			break
		}
		ancestor = parent
	}
	real, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return invalid("venv 路径解析失败: " + err.Error())
	}
	realRoots := make([]string, 0, len(allowedRoots(stateRoot)))
	for _, root := range allowedRoots(stateRoot) {
		if candidate, evalErr := filepath.EvalSymlinks(root); evalErr == nil {
			realRoots = append(realRoots, candidate)
		}
	}
	if !withinAny(realRoots, real) {
		return venvPathOutOfRootHint(clean)
	}
	return nil
}

// venvPathOutOfRootHint 构造 E_INVALID + 可操作 hint（允许根清单）。
func venvPathOutOfRootHint(clean string) *pluginapi.Error {
	roots := strings.Join(allowedRoots(""), "、")
	detail, _ := json.Marshal(map[string]string{
		"hint":           "venv 只能建在插件状态目录（默认 <state>/venvs）或允许根内：" + roots,
		"requested_path": clean,
	})
	return &pluginapi.Error{Code: pluginapi.E_INVALID, Message: "venv 路径越界", Detail: detail}
}

// validatePythonVersion 校验 python_version，返回空串（用 python3）或规范版本号。
func validatePythonVersion(raw string) (string, *pluginapi.Error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if !pythonVersionRequestPattern.MatchString(trimmed) {
		return "", invalid("python_version 非法：只接受 x.y 或 x.y.z")
	}
	return trimmed, nil
}

// validatePackages 校验包名白名单；required=false 时允许空列表。
func validatePackages(packages []string, optional bool) ([]string, *pluginapi.Error) {
	if len(packages) == 0 {
		if optional {
			return []string{}, nil
		}
		return nil, invalid("请填写必填字段 packages（要安装的包列表）")
	}
	if len(packages) > MaxVenvPackages {
		return nil, invalid(fmt.Sprintf("packages 数量 %d 超过上限 %d", len(packages), MaxVenvPackages))
	}
	out := make([]string, 0, len(packages))
	for _, pkg := range packages {
		if !packageSpecPattern.MatchString(pkg) {
			return nil, invalid("包名格式有误，请只用字母数字 . _ - 和可选的 ==版本号: " + pkg)
		}
		out = append(out, pkg)
	}
	return out, nil
}

// truncatePipOutput 保留首尾各一半并插入截断标记。
func truncatePipOutput(out string) (string, bool) {
	if len(out) <= MaxPipOutputBytes {
		return out, false
	}
	half := MaxPipOutputBytes / 2
	return out[:half] + fmt.Sprintf("\n...[输出截断，共 %d 字节]...\n", len(out)) + out[len(out)-half:], true
}

// realVenvManager 是真实执行面：python3 -m venv + <venv>/bin/pip install。
type realVenvManager struct{}

func (realVenvManager) CreateVenv(root string, spec VenvSpec) (VenvCreateResult, error) {
	if info, err := os.Stat(spec.Path); err == nil && info.IsDir() {
		return VenvCreateResult{Path: spec.Path, PythonVersion: readVenvVersion(spec.Path), Created: false}, nil
	}
	if err := os.MkdirAll(filepath.Dir(spec.Path), 0o700); err != nil {
		return VenvCreateResult{}, err
	}
	binary := "python3"
	if spec.PythonVersion != "" {
		binary = "python" + spec.PythonVersion
	}
	if _, err := runVenvCommand(VenvCommandTimeout, binary, "-m", "venv", spec.Path); err != nil {
		return VenvCreateResult{}, err
	}
	if err := os.Chmod(spec.Path, 0o700); err != nil {
		return VenvCreateResult{}, err
	}
	return VenvCreateResult{Path: spec.Path, PythonVersion: readVenvVersion(spec.Path), Created: true}, nil
}

func (realVenvManager) InstallPackages(spec PipInstallSpec) (PipInstallResult, error) {
	pip := filepath.Join(spec.VenvPath, "bin", "pip")
	if _, err := os.Stat(pip); err != nil {
		return PipInstallResult{}, ErrVenvNoPip
	}
	args := append([]string{"install", "--no-input", "--disable-pip-version-check"}, spec.Packages...)
	started := time.Now()
	out, err := runVenvCommand(VenvCommandTimeout, pip, args...)
	duration := time.Since(started).Milliseconds()
	truncatedOut, truncated := truncatePipOutput(out)
	result := PipInstallResult{
		VenvPath: spec.VenvPath, Output: truncatedOut, Truncated: truncated,
		DurationMS: duration, NetworkUsed: true,
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func (realVenvManager) ListVenvs(root string) (VenvListResult, error) {
	dir := filepath.Join(root, DefaultVenvSubdir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return VenvListResult{}, nil
	}
	if err != nil {
		return VenvListResult{}, err
	}
	result := VenvListResult{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if _, err := os.Stat(filepath.Join(path, "pyvenv.cfg")); err != nil {
			continue
		}
		result.Venvs = append(result.Venvs, VenvInfo{Name: entry.Name(), Path: path, Version: readVenvVersion(path)})
	}
	return result, nil
}

func (realVenvManager) ListPackages(venvPath string) (PipListResult, error) {
	pip := filepath.Join(venvPath, "bin", "pip")
	if _, err := os.Stat(pip); err != nil {
		return PipListResult{}, ErrVenvNoPip
	}
	out, err := runVenvCommand(VenvCommandTimeout, pip, "list", "--format=json", "--disable-pip-version-check")
	if err != nil {
		return PipListResult{}, err
	}
	var packages []PackageInfo
	if err := json.Unmarshal([]byte(out), &packages); err != nil {
		return PipListResult{}, fmt.Errorf("解析 pip list 输出失败: %w", err)
	}
	return PipListResult{Packages: packages}, nil
}

// readVenvVersion 从 pyvenv.cfg 读取 version= 行（缺失返回空串，不报错）。
func readVenvVersion(venvPath string) string {
	payload, err := os.ReadFile(filepath.Join(venvPath, "pyvenv.cfg"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(payload), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "version") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// runVenvCommand 固定 argv 执行命令并返回合并输出；超时返回 ErrVenvTimeout。
func runVenvCommand(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var buffer strings.Builder
	cmd.Stdout = &buffer
	cmd.Stderr = &buffer
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return buffer.String(), ErrVenvTimeout
	}
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return buffer.String(), fmt.Errorf("找不到可执行文件 %s: %w", name, err)
		}
		return buffer.String(), fmt.Errorf("命令执行失败（%s）: %w", name, err)
	}
	return buffer.String(), nil
}
