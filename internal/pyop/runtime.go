package pyop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// defaultKnownPythonPaths 是常见安装位置（固定项，不依赖 PATH）。
var defaultKnownPythonPaths = []string{
	"/usr/local/bin/python3",
	"/usr/bin/python3",
}

// pythonBinaryName 只接受 `python3` 与版本化形式 `python3.12`，
// 过滤掉 `/usr/bin/python3*` 通配里命中的 python3-config / python3.12-dbg 等非解释器。
var pythonBinaryName = regexp.MustCompile(`^python3(\.[0-9]+)?$`)

// pythonVersionPattern 从 `<python> --version` 的输出里提取版本号。
// CPython 输出 `Python 3.14.4`，PyPy 首行同样是 `Python 3.10.14 (...)`。
var pythonVersionPattern = regexp.MustCompile(`(?i)\bPython\s+(\d+\.\d+(?:\.\d+)?)`)

// requestedVersionPattern 是 info.version 入参的合法形状（x.y 或 x.y.z）。
var requestedVersionPattern = regexp.MustCompile(`^\d+\.\d+(\.\d+)?$`)

// 解释器探测脚本：全部是固定片段，不接受任何用户输入拼接。
const (
	scriptExecutable   = "import sys;print(sys.executable)"
	scriptPrefix       = "import sys;print(sys.prefix)"
	scriptSitePackages = "import sysconfig;print(sysconfig.get_paths().get('purelib',''))"
	scriptImportVenv   = "import venv"
	// isolatedFlag 让探测忽略 PYTHON* 环境变量与 user site（-E -s 的合并开关）。
	isolatedFlag = "-I"
)

// globRule 是一条候选发现规则：glob 模式 + 命中后的 source 归属。
type globRule struct {
	pattern string
	source  Source
}

// Runtime 是真实探测执行面：PATH / 常见路径 / pyenv / conda + 固定 argv 命令。
// 字段可注入，便于单测在不碰宿主环境的前提下验证去重、排序与降级逻辑。
type Runtime struct {
	runner     CommandRunner
	lookPath   func(string) (string, error)
	homeDir    func() (string, error)
	glob       func(string) ([]string, error)
	getenv     func(string) string
	knownPaths []string
}

// NewExecutor 返回生产执行面（真实探测宿主 Python 安装）。
func NewExecutor() Executor { return newRuntime() }

func newRuntime() *Runtime {
	return &Runtime{
		runner:     osRunner{timeout: CommandTimeout},
		lookPath:   exec.LookPath,
		homeDir:    os.UserHomeDir,
		glob:       filepath.Glob,
		getenv:     os.Getenv,
		knownPaths: defaultKnownPythonPaths,
	}
}

// osRunner 是 CommandRunner 的生产实现：无 shell、固定 argv、硬超时。
type osRunner struct {
	timeout time.Duration
}

// Run 执行一条命令并返回去空白的 stdout；超时与退出码非 0 都返回错误。
func (r osRunner) Run(name, pathDir string, args ...string) (string, error) {
	timeout := r.timeout
	if timeout <= 0 {
		timeout = CommandTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = envWithPath(pathDir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("命令 %s 超时（%s）", filepath.Base(name), timeout)
		}
		return strings.TrimSpace(stdout.String()), fmt.Errorf("命令 %s 失败: %w（stderr: %s）",
			filepath.Base(name), err, trimForMessage(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// envWithPath 构造子进程环境：pathDir 非空时替换 PATH 并把该目录前置。
func envWithPath(pathDir string) []string {
	current := os.Getenv("PATH")
	if pathDir != "" {
		current = pathDir + string(os.PathListSeparator) + current
	}
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PATH=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "PATH="+current)
}

// trimForMessage 截断过长的 stderr，避免错误信息膨胀。
func trimForMessage(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) > 200 {
		return trimmed[:200] + "…"
	}
	return trimmed
}

// candidate 是一个待验证的 python 解释器候选。
type candidate struct {
	path   string
	source Source
}

// Versions 探测宿主全部 Python 安装并按「默认优先 + 版本降序 + 路径升序」返回。
// 任一候选执行 --version 失败只跳过该候选，不影响其他安装。
func (r *Runtime) Versions() (VersionsResult, error) {
	versions := r.detect()
	defaultVersion := ""
	for _, item := range versions {
		if item.IsDefault {
			defaultVersion = item.Version
			break
		}
	}
	result := VersionsResult{Versions: versions, DefaultVersion: defaultVersion, Count: len(versions)}
	if len(versions) == 0 {
		result.Hint = NoPythonHint
	}
	return result, nil
}

// detect 收集候选、验版本、按真实路径去重后排序。
func (r *Runtime) detect() []PythonVersion {
	candidates := r.candidates()
	seen := map[string]bool{}
	versions := make([]PythonVersion, 0, len(candidates))
	for _, item := range candidates {
		real, ok := resolveCandidate(item.path)
		if !ok || seen[real] {
			continue
		}
		version, implementation, err := r.pythonVersion(item.path)
		if err != nil {
			continue
		}
		seen[real] = true
		versions = append(versions, PythonVersion{
			Version:        version,
			Path:           item.path,
			Source:         item.source,
			Implementation: implementation,
		})
	}
	defaultReal := r.defaultRealPath()
	for i := range versions {
		if defaultReal != "" {
			if real, ok := resolveCandidate(versions[i].Path); ok && real == defaultReal {
				versions[i].IsDefault = true
			}
		}
	}
	sortVersions(versions)
	return versions
}

// candidates 按优先级收集候选：PATH → 常见固定路径 → 通配规则（含 pyenv / conda）。
func (r *Runtime) candidates() []candidate {
	out := make([]candidate, 0, 12)
	seen := map[string]bool{}
	add := func(path string, source Source) {
		cleaned := filepath.Clean(strings.TrimSpace(path))
		if cleaned == "" || seen[cleaned] {
			return
		}
		seen[cleaned] = true
		out = append(out, candidate{path: cleaned, source: source})
	}
	for _, name := range []string{"python3", "python"} {
		if path, err := r.lookPath(name); err == nil {
			add(path, SourcePath)
		}
	}
	for _, path := range r.knownPaths {
		add(path, SourceKnownPath)
	}
	for _, rule := range r.globRules() {
		matches, err := r.glob(rule.pattern)
		if err != nil {
			continue
		}
		for _, match := range matches {
			if !pythonBinaryName.MatchString(filepath.Base(match)) {
				continue
			}
			add(match, rule.source)
		}
	}
	return out
}

// globRules 返回全部通配发现规则；pyenv / conda 的规则依赖 home。
func (r *Runtime) globRules() []globRule {
	rules := []globRule{
		{pattern: "/usr/bin/python3*", source: SourceKnownPath},
		{pattern: "/usr/local/bin/python3*", source: SourceKnownPath},
		{pattern: "/opt/python*/bin/python3", source: SourceKnownPath},
		{pattern: "/opt/conda/envs/*/bin/python3", source: SourceConda},
		{pattern: "/opt/conda/bin/python3", source: SourceConda},
	}
	home, err := r.homeDir()
	if err != nil || home == "" {
		return rules
	}
	rules = append(rules,
		globRule{pattern: filepath.Join(home, ".pyenv", "versions", "*", "bin", "python3"), source: SourcePyenv},
		globRule{pattern: filepath.Join(home, ".conda", "envs", "*", "bin", "python3"), source: SourceConda},
		globRule{pattern: filepath.Join(home, "miniconda3", "envs", "*", "bin", "python3"), source: SourceConda},
		globRule{pattern: filepath.Join(home, "anaconda3", "envs", "*", "bin", "python3"), source: SourceConda},
		globRule{pattern: filepath.Join(home, "miniconda3", "bin", "python3"), source: SourceConda},
		globRule{pattern: filepath.Join(home, "anaconda3", "bin", "python3"), source: SourceConda},
	)
	return rules
}

// resolveCandidate 跟随符号链接返回真实路径，并确认目标是可执行文件（普通文件）。
func resolveCandidate(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real, true
	}
	return filepath.Clean(path), true
}

// defaultRealPath 按 `which python3` 语义（PATH 解析）返回默认解释器真实路径；
// PATH 里没有 python3 时退回 which python。
func (r *Runtime) defaultRealPath() string {
	for _, name := range []string{"python3", "python"} {
		path, err := r.lookPath(name)
		if err != nil || path == "" {
			continue
		}
		if real, ok := resolveCandidate(path); ok {
			return real
		}
	}
	return ""
}

// pythonVersion 执行 `<python> --version`，返回归一化版本与实现名。
func (r *Runtime) pythonVersion(path string) (string, string, error) {
	out, err := r.runner.Run(path, "", "--version")
	if err != nil {
		return "", "", err
	}
	version, ok := parsePythonVersion(out)
	if !ok {
		return "", "", fmt.Errorf("无法解析 %s --version 输出: %q", path, out)
	}
	return version, pythonImplementation(out), nil
}

// parsePythonVersion 从 `--version` 输出提取版本号。
// 兼容 `Python 3.14.4`、`Python 3.11.13 (…)`、`Python 2.7.18`；解析失败返回 false。
func parsePythonVersion(raw string) (string, bool) {
	match := pythonVersionPattern.FindStringSubmatch(raw)
	if match == nil {
		return "", false
	}
	return match[1], true
}

// pythonImplementation 按 `--version` 横幅判定实现名：出现 PyPy 即 PyPy，其余为 CPython。
func pythonImplementation(raw string) string {
	if strings.Contains(strings.ToLower(raw), "pypy") {
		return ImplementationPyPy
	}
	return ImplementationCPython
}

// runProbe 以隔离模式执行一段固定脚本，返回去空白的 stdout。
func (r *Runtime) runProbe(entry PythonVersion, args ...string) (string, error) {
	argv := append([]string{isolatedFlag}, args...)
	return r.runner.Run(entry.Path, "", argv...)
}

// probeScript 执行固定脚本，失败返回空串（详情字段降级，不放大为整次失败）。
func (r *Runtime) probeScript(entry PythonVersion, script string) string {
	out, err := r.runProbe(entry, "-c", script)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// pipVersion 返回该解释器绑定的 pip 版本（`-m pip --version`）。
func (r *Runtime) pipVersion(entry PythonVersion) string {
	out, err := r.runProbe(entry, "-m", "pip", "--version")
	if err != nil {
		return ""
	}
	return parsePipVersion(out)
}

// parsePipVersion 从 `pip 25.1.1 from …` 提取版本号；形状不符返回空串。
func parsePipVersion(raw string) string {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) < 2 || fields[0] != "pip" {
		return ""
	}
	return fields[1]
}

// installedPackageCount 解析 `pip list --format=json` 的包数；失败返回 0。
func (r *Runtime) installedPackageCount(entry PythonVersion) int {
	out, err := r.runProbe(entry, "-m", "pip", "list", "--format=json")
	if err != nil && strings.TrimSpace(out) == "" {
		return 0
	}
	return parsePipListCount(out)
}

// pipPackage 是 `pip list --format=json` 的数组元素（只关心 name）。
type pipPackage struct {
	Name string `json:"name"`
}

// parsePipListCount 统计 pip 清单数组长度；容忍 JSON 前后的提示行，坏 JSON 返回 0。
func parsePipListCount(raw string) int {
	trimmed := strings.TrimSpace(raw)
	start := strings.Index(trimmed, "[")
	end := strings.LastIndex(trimmed, "]")
	if start < 0 || end < start {
		return 0
	}
	var list []pipPackage
	if err := json.Unmarshal([]byte(trimmed[start:end+1]), &list); err != nil {
		return 0
	}
	return len(list)
}

// venvAvailable 判定该解释器是否带有 venv 模块。
func (r *Runtime) venvAvailable(entry PythonVersion) bool {
	_, err := r.runProbe(entry, "-c", scriptImportVenv)
	return err == nil
}

// sitePackages 返回该解释器的 site-packages 目录（sysconfig purelib）。
func (r *Runtime) sitePackages(entry PythonVersion) string {
	return r.probeScript(entry, scriptSitePackages)
}

// Info 汇总单个解释器的详情；version 为空时取默认解释器。
func (r *Runtime) Info(version string) (InfoResult, error) {
	versions := r.detect()
	if len(versions) == 0 {
		return InfoResult{}, ErrPythonNotInstalled
	}
	entry, ok := selectVersion(versions, version)
	if !ok {
		return InfoResult{}, fmt.Errorf("%w: %s（已检测: %s）", ErrVersionNotInstalled, version, joinVersionList(versions))
	}
	executable := r.probeScript(entry, scriptExecutable)
	if executable == "" {
		executable = entry.Path
	}
	result := InfoResult{
		Version:           entry.Version,
		Executable:        executable,
		Prefix:            r.probeScript(entry, scriptPrefix),
		PipVersion:        r.pipVersion(entry),
		SitePackages:      r.sitePackages(entry),
		InstalledPackages: r.installedPackageCount(entry),
		VenvAvailable:     r.venvAvailable(entry),
	}
	return result, nil
}

// selectVersion 选取请求版本；空请求优先默认版本，其次第一个。
// 请求既支持完整版本（3.12.3），也支持主次版本前缀（3.12 → 命中 3.12.x）。
func selectVersion(versions []PythonVersion, version string) (PythonVersion, bool) {
	if version == "" {
		for _, item := range versions {
			if item.IsDefault {
				return item, true
			}
		}
		if len(versions) > 0 {
			return versions[0], true
		}
		return PythonVersion{}, false
	}
	for _, item := range versions {
		if item.Version == version {
			return item, true
		}
	}
	if strings.Count(version, ".") == 1 {
		for _, item := range versions {
			if strings.HasPrefix(item.Version, version+".") {
				return item, true
			}
		}
	}
	return PythonVersion{}, false
}

// normalizeRequestedVersion 归一等 info.version 入参：接受 `3.12`、`3.12.3`、
// `v3.12.3`、`Python 3.12.3`；其余形状返回 false。
func normalizeRequestedVersion(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	for _, prefix := range []string{"Python ", "python "} {
		trimmed = strings.TrimPrefix(trimmed, prefix)
	}
	trimmed = strings.TrimPrefix(strings.TrimPrefix(trimmed, "v"), "V")
	if !requestedVersionPattern.MatchString(trimmed) {
		return "", false
	}
	return trimmed, true
}

// compareVersions 比较两个版本号；无法解析时退回字符串比较。
func compareVersions(a, b string) int {
	left, leftOK := versionTriple(a)
	right, rightOK := versionTriple(b)
	if !leftOK || !rightOK {
		return strings.Compare(a, b)
	}
	for i := 0; i < 3; i++ {
		if left[i] != right[i] {
			if left[i] < right[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// versionTriple 解析 x.y.z 为三个整数；x.y 视为缺省补 0。
func versionTriple(version string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(version, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return out, false
	}
	for i, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil {
			return out, false
		}
		out[i] = value
	}
	return out, true
}

// sortVersions 默认版本排最前，其次版本降序，最后按路径升序（保证结果确定）。
func sortVersions(versions []PythonVersion) {
	sort.Slice(versions, func(i, j int) bool {
		if versions[i].IsDefault != versions[j].IsDefault {
			return versions[i].IsDefault
		}
		if cmp := compareVersions(versions[i].Version, versions[j].Version); cmp != 0 {
			return cmp > 0
		}
		return versions[i].Path < versions[j].Path
	})
}

// firstLine 取多行输出的第一行。
func firstLine(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if idx := strings.IndexByte(trimmed, '\n'); idx >= 0 {
		return strings.TrimSpace(trimmed[:idx])
	}
	return trimmed
}

// joinVersionList 拼接已安装版本号，用于错误信息里的可重试取值。
func joinVersionList(versions []PythonVersion) string {
	out := make([]string, 0, len(versions))
	for _, item := range versions {
		out = append(out, item.Version)
	}
	if len(out) == 0 {
		return "无"
	}
	return strings.Join(out, ",")
}
