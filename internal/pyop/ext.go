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
	"sort"
	"strings"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// 扩展管理的能力边界：只在插件状态的隔离前缀（<state>/global/py_ext）内安装/列出/移除 Python 包。
// install 走固定 argv 的 `python3 -m pip install --target`，remove 因 pip uninstall 无 --target
// 改为隔离目录内的受控文件删除（无 argv、无子进程），list 只读 dist-info/METADATA。
// 绝不写宿主 site-packages、不设 PYTHONPATH / PATH、不新增 root_action。
const (
	// DefaultGlobalSubdir 是隔离前缀相对插件状态目录的固定子目录名（与 Node/PHP 对齐）。
	DefaultGlobalSubdir = "global"
	// pyExtSubdir 是隔离前缀下平铺安装目录名（<state>/global/py_ext）。
	pyExtSubdir = "py_ext"
	// MaxExtPackages 是单次安装/移除的包数量上限（复用 venv 包上限口径）。
	MaxExtPackages = MaxVenvPackages
)

var (
	// ErrExtTimeout 表示 pip 命令超时（由 runExtCommand 返回，classify 映射为 E_UPSTREAM）。
	ErrExtTimeout = errors.New("扩展命令超时")
	// ErrExtContainment 表示候选条目经软链解析后越出隔离前缀（remove 必须拒绝，不跟随删除）。
	ErrExtContainment = errors.New("隔离扩展目录 containment 校验失败")
	// ErrExtPrefixMissing 表示隔离前缀目录不存在（未装过扩展）；list 把它当空态而非错误。
	ErrExtPrefixMissing = errors.New("隔离扩展目录不存在")
)

// pyExtVersionSuffixPattern 识别 dist-info / egg-info 目录名尾部的 -版本 段（用于元数据缺失时兜底）。
var pyExtVersionSuffixPattern = regexp.MustCompile(`^(.+)-([0-9][A-Za-z0-9.!+_-]*)$`)

// ExtInstallResult 是 runtime-python.ext.install 的结果。
type ExtInstallResult struct {
	Prefix      string   `json:"prefix"`
	Packages    []string `json:"packages"`
	Output      string   `json:"output"`
	Truncated   bool     `json:"truncated"`
	DurationMS  int64    `json:"duration_ms"`
	NetworkUsed bool     `json:"network_used"`
	Hint        string   `json:"hint,omitempty"`
}

// ExtPackage 是隔离前缀中已安装的一个扩展。
type ExtPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ExtListResult 是 runtime-python.ext.list 的结果。
type ExtListResult struct {
	Prefix   string       `json:"prefix"`
	Packages []ExtPackage `json:"packages"`
	Count    int          `json:"count"`
	Hint     string       `json:"hint,omitempty"`
}

// ExtRemoveResult 是 runtime-python.ext.remove 的结果。
type ExtRemoveResult struct {
	Prefix    string   `json:"prefix"`
	Removed   []string `json:"removed"`
	Output    string   `json:"output"`
	Truncated bool     `json:"truncated"`
	Hint      string   `json:"hint,omitempty"`
}

type extInstallParams struct {
	Packages []string `json:"packages"`
}

type extListParams struct{}

type extRemoveParams struct {
	Packages []string `json:"packages"`
}

// ExtManager 是扩展管理执行面；真实实现走 os/exec + 状态目录文件系统，测试可注入假执行面。
type ExtManager interface {
	InstallPackages(prefix string, packages []string) (ExtInstallResult, error)
	ListPackages(prefix string) (ExtListResult, error)
	RemovePackages(prefix string, packages []string) (ExtRemoveResult, error)
}

func extManager(exec Executor) ExtManager {
	if manager, ok := exec.(ExtManager); ok {
		return manager
	}
	return realExtManager{}
}

func callExtInstall(params json.RawMessage, stateRoot string, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p extInstallParams
	if err := strictUnmarshal(params, &p); err != nil {
		return nil, invalid("runtime-python.ext.install 参数非法，请传 JSON 对象: " + err.Error())
	}
	prefix, perr := resolveExtPrefix(stateRoot)
	if perr != nil {
		return nil, perr
	}
	packages, perr := validateExtPackages(p.Packages, "安装")
	if perr != nil {
		return nil, perr
	}
	result, err := extManager(exec).InstallPackages(prefix, packages)
	if err != nil {
		return nil, classify(err)
	}
	result.Prefix = prefix
	result.Packages = packages
	result.NetworkUsed = true
	if !result.Truncated && len(result.Output) > MaxPipOutputBytes {
		result.Output, result.Truncated = truncatePipOutput(result.Output)
	}
	if result.Hint == "" {
		result.Hint = "扩展只装进插件隔离目录（<state>/global/py_ext），不会写宿主 site-packages；如需在代码里使用请自行把该目录加入 PYTHONPATH。"
	}
	return marshal(result)
}

func callExtList(params json.RawMessage, stateRoot string, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p extListParams
	if err := strictUnmarshal(params, &p); err != nil {
		return nil, invalid("runtime-python.ext.list 参数非法，请传空 JSON 对象 {}: " + err.Error())
	}
	prefix, perr := resolveExtPrefix(stateRoot)
	if perr != nil {
		return nil, perr
	}
	result, err := extManager(exec).ListPackages(prefix)
	if errors.Is(err, ErrExtPrefixMissing) {
		result = ExtListResult{Packages: []ExtPackage{}}
	} else if err != nil {
		return nil, classify(err)
	}
	result.Prefix = prefix
	if result.Packages == nil {
		result.Packages = []ExtPackage{}
	}
	result.Count = len(result.Packages)
	if result.Count == 0 && result.Hint == "" {
		result.Hint = "还没有隔离扩展；请在版本详情里点「管理扩展」安装，包会写进插件状态目录。"
	}
	return marshal(result)
}

func callExtRemove(params json.RawMessage, stateRoot string, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p extRemoveParams
	if err := strictUnmarshal(params, &p); err != nil {
		return nil, invalid("runtime-python.ext.remove 参数非法，请传 JSON 对象: " + err.Error())
	}
	prefix, perr := resolveExtPrefix(stateRoot)
	if perr != nil {
		return nil, perr
	}
	packages, perr := validateExtPackages(p.Packages, "移除")
	if perr != nil {
		return nil, perr
	}
	result, err := extManager(exec).RemovePackages(prefix, packages)
	if err != nil {
		return nil, classify(err)
	}
	result.Prefix = prefix
	if result.Removed == nil {
		result.Removed = []string{}
	}
	if !result.Truncated && len(result.Output) > MaxPipOutputBytes {
		result.Output, result.Truncated = truncatePipOutput(result.Output)
	}
	if result.Hint == "" {
		result.Hint = "只移除隔离目录内的扩展；宿主 site-packages 不受影响。"
	}
	return marshal(result)
}

// resolveExtPrefix 计算隔离前缀并做路径/软链校验：
// 前缀固定为 <state>/global/py_ext，不接受调用方传路径；词法层拒绝 .. / NUL / 反斜杠 / 相对 / 首尾空白，
// 真实层把 <state>、<state>/global 与 <state>/global/py_ext 最近存在祖先逐一 EvalSymlinks，
// 全部仍须落在状态目录内（挡软链逃逸）。
func resolveExtPrefix(stateRoot string) (string, *pluginapi.Error) {
	trimmed := strings.TrimSpace(stateRoot)
	if trimmed == "" {
		return "", invalid("请配置插件状态目录后再管理扩展")
	}
	if trimmed != stateRoot {
		return "", invalid("请去掉插件状态目录首尾空白后重试")
	}
	if strings.ContainsRune(trimmed, 0) || strings.Contains(trimmed, "\\") {
		return "", invalid("插件状态目录含非法字符（NUL / 反斜杠），请改用正斜杠路径")
	}
	if !filepath.IsAbs(trimmed) {
		return "", invalid("插件状态目录请填绝对路径（以 / 开头）")
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == ".." {
			return "", invalid("插件状态目录含 .. 段，请改用允许目录内的路径")
		}
	}
	root := filepath.Clean(trimmed)
	global := filepath.Join(root, DefaultGlobalSubdir)
	prefix := filepath.Join(global, pyExtSubdir)
	if !within(root, global) || !within(root, prefix) {
		return "", invalid("隔离扩展路径越界，请检查插件状态目录配置")
	}
	realRoot, perr := realAncestor(root)
	if perr != nil {
		return "", perr
	}
	for _, candidate := range []string{global, prefix} {
		real, perr := realAncestor(candidate)
		if perr != nil {
			return "", perr
		}
		if !within(realRoot, real) {
			return "", invalid("隔离扩展目录指向了状态目录之外的位置，请删除该软链后重试")
		}
	}
	return prefix, nil
}

// realAncestor 取最近存在祖先并解析符号链接（不存在时逐层回退到父目录）。
func realAncestor(path string) (string, *pluginapi.Error) {
	ancestor := path
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
		return "", invalid("隔离扩展目录解析失败，请检查路径权限: " + err.Error())
	}
	return real, nil
}

// validateExtPackages 校验包名白名单（复用 venv.go 的 packageSpecPattern，不复制正则）；空列表拒绝。
func validateExtPackages(packages []string, action string) ([]string, *pluginapi.Error) {
	if len(packages) == 0 {
		return nil, invalid(fmt.Sprintf("请填写要%s的扩展包名（例如 requests 或 requests==2.31.0）", action))
	}
	if len(packages) > MaxExtPackages {
		return nil, invalid(fmt.Sprintf("扩展包数量 %d 超过上限 %d，请分批操作", len(packages), MaxExtPackages))
	}
	out := make([]string, 0, len(packages))
	for _, pkg := range packages {
		if !packageSpecPattern.MatchString(pkg) {
			return nil, invalid(fmt.Sprintf("请检查包名 %q：只接受字母数字 . _ - 和可选的 ==版本，不要写空格、分号、斜杠或 --user 选项", pkg))
		}
		out = append(out, pkg)
	}
	return out, nil
}

// extCommandRunner 是 pip 命令执行面的最小契约（测试可注入以核对固定 argv）。
type extCommandRunner func(timeout time.Duration, name string, args ...string) (string, error)

// realExtManager 是真实执行面：install 走 python3 -m pip install --target；
// remove 用标准库做隔离前缀内的受控删除；list 只读 dist-info/METADATA。
type realExtManager struct {
	runner extCommandRunner
}

func (m realExtManager) commandRunner() extCommandRunner {
	if m.runner != nil {
		return m.runner
	}
	return runExtCommand
}

func (m realExtManager) InstallPackages(prefix string, packages []string) (ExtInstallResult, error) {
	// 固定 argv：-m pip install --target <prefix> --no-input --disable-pip-version-check <包名...>。
	// 无 sh -c、无 --user、无 --break-system-packages，pip 只写隔离前缀。
	args := append([]string{"-m", "pip", "install", "--target", prefix,
		"--no-input", "--disable-pip-version-check"}, packages...)
	started := time.Now()
	out, err := m.commandRunner()(VenvCommandTimeout, "python3", args...)
	result := ExtInstallResult{
		Prefix: prefix, Output: out, DurationMS: time.Since(started).Milliseconds(), NetworkUsed: true,
	}
	truncated, wasTruncated := truncatePipOutput(out)
	result.Output, result.Truncated = truncated, wasTruncated
	if err != nil {
		return result, err
	}
	return result, nil
}

// RemovePackages 在隔离前缀内做受控文件删除：不起子进程、不用 pip uninstall（无 --target）。
// 每个包先做 PEP 503 归一（lower + '-'→'_'）再枚举直接子项；符号链接指向前缀外一律 ErrExtContainment。
func (m realExtManager) RemovePackages(prefix string, packages []string) (ExtRemoveResult, error) {
	realPrefix, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		if os.IsNotExist(err) {
			return ExtRemoveResult{Prefix: prefix, Removed: []string{}}, nil
		}
		return ExtRemoveResult{}, err
	}
	entries, err := os.ReadDir(prefix)
	if err != nil {
		if os.IsNotExist(err) {
			return ExtRemoveResult{Prefix: prefix, Removed: []string{}}, nil
		}
		return ExtRemoveResult{}, err
	}
	removed := make([]string, 0, len(packages))
	var lines []string
	for _, pkg := range packages {
		want := pyExtName(pkg)
		for _, entry := range entries {
			name := entry.Name()
			if !pyExtNameMatches(name, want) {
				continue
			}
			candidate := filepath.Join(prefix, name)
			rel, relErr := filepath.Rel(prefix, candidate)
			if relErr != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return ExtRemoveResult{}, fmt.Errorf("%w: %s", ErrExtContainment, name)
			}
			if entry.Type()&os.ModeSymlink != 0 {
				target, evalErr := filepath.EvalSymlinks(candidate)
				if evalErr != nil || !within(realPrefix, target) {
					return ExtRemoveResult{}, fmt.Errorf("%w: %s", ErrExtContainment, name)
				}
			}
			if err := os.RemoveAll(candidate); err != nil {
				return ExtRemoveResult{}, err
			}
			removed = append(removed, name)
			lines = append(lines, "已移除: "+name)
		}
	}
	if len(lines) == 0 {
		lines = append(lines, "隔离目录内没有匹配的扩展，无需移除")
	}
	out := strings.Join(lines, "\n")
	truncated, wasTruncated := truncatePipOutput(out)
	return ExtRemoveResult{Prefix: prefix, Removed: removed, Output: truncated, Truncated: wasTruncated}, nil
}

// ListPackages 只读隔离前缀内的 *.dist-info/METADATA（*.egg-info/PKG-INFO 兜底）。
// 条目经 Rel + EvalSymlinks containment 校验，只列真实落在前缀内的元数据目录。
func (m realExtManager) ListPackages(prefix string) (ExtListResult, error) {
	entries, err := os.ReadDir(prefix)
	if errors.Is(err, os.ErrNotExist) {
		return ExtListResult{Packages: []ExtPackage{}}, ErrExtPrefixMissing
	}
	if err != nil {
		return ExtListResult{}, err
	}
	realPrefix, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		return ExtListResult{}, err
	}
	packages := make([]ExtPackage, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		name := entry.Name()
		var metaName string
		switch {
		case strings.HasSuffix(name, ".dist-info"):
			metaName = "METADATA"
		case strings.HasSuffix(name, ".egg-info"):
			metaName = "PKG-INFO"
		default:
			continue
		}
		dir := filepath.Join(prefix, name)
		rel, relErr := filepath.Rel(prefix, dir)
		if relErr != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		// IsDir() 对软链返回 false，上面已跳过；这里再挡 METADATA 本身是指向前缀外的软链。
		metaPath := filepath.Join(dir, metaName)
		if realMeta, evalErr := filepath.EvalSymlinks(metaPath); evalErr == nil {
			if !within(realPrefix, realMeta) {
				continue
			}
		}
		packages = append(packages, readExtMetadata(name, metaPath))
	}
	sort.Slice(packages, func(i, j int) bool {
		if packages[i].Name == packages[j].Name {
			return packages[i].Version < packages[j].Version
		}
		return packages[i].Name < packages[j].Name
	})
	return ExtListResult{Packages: packages}, nil
}

// readExtMetadata 解析 METADATA / PKG-INFO 的 Name: / Version:；缺失或解析失败时用目录名兜底。
func readExtMetadata(dirName, metaPath string) ExtPackage {
	payload, err := os.ReadFile(metaPath)
	if err == nil {
		pkg := ExtPackage{}
		for _, line := range strings.Split(string(payload), "\n") {
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "name":
				pkg.Name = strings.TrimSpace(value)
			case "version":
				pkg.Version = strings.TrimSpace(value)
			}
		}
		if pkg.Name != "" {
			return pkg
		}
	}
	stem := strings.TrimSuffix(strings.TrimSuffix(dirName, ".dist-info"), ".egg-info")
	if match := pyExtVersionSuffixPattern.FindStringSubmatch(stem); match != nil {
		return ExtPackage{Name: match[1], Version: match[2]}
	}
	return ExtPackage{Name: stem}
}

// pyExtName 剥掉 ==版本 后缀并按 PEP 503 归一（lower + '-'→'_'）。
func pyExtName(pkg string) string {
	base, _, _ := strings.Cut(pkg, "==")
	return strings.ReplaceAll(strings.ToLower(base), "-", "_")
}

// pyExtNameMatches 判定 dist-info / egg-info 目录名是否归属某个包：
// 归一后相等，或「包名_版本」且版本段以数字开头（避免 foo 误匹配 foo_bar-1.0）。
func pyExtNameMatches(dirName, want string) bool {
	stem := strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(dirName), ".dist-info"), ".egg-info")
	normalized := strings.ReplaceAll(stem, "-", "_")
	if normalized == want {
		return true
	}
	if strings.HasPrefix(normalized, want+"_") {
		rest := normalized[len(want)+1:]
		if rest != "" && rest[0] >= '0' && rest[0] <= '9' {
			return true
		}
	}
	return false
}

// runExtCommand 固定 argv 执行命令（无 shell），合并输出，超时返回 ErrExtTimeout。
func runExtCommand(timeout time.Duration, name string, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = VenvCommandTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var buffer strings.Builder
	cmd.Stdout = &buffer
	cmd.Stderr = &buffer
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return buffer.String(), ErrExtTimeout
	}
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return buffer.String(), fmt.Errorf("找不到可执行文件 %s: %w", name, err)
		}
		return buffer.String(), fmt.Errorf("命令执行失败（%s）: %w", name, err)
	}
	return buffer.String(), nil
}
