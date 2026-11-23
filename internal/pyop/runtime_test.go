package pyop

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestParsePythonVersion 版本解析：CPython / 带括号横幅 / Python 2 / 非法输出。
func TestParsePythonVersion(t *testing.T) {
	valid := map[string]string{
		"Python 3.12.3":   "3.12.3",
		"Python 3.14.4\n": "3.14.4",
		"Python 3.11.13 (main, Aug 15 2024, 08:40)": "3.11.13",
		"Python 2.7.18": "2.7.18",
		"Python 3.10.14 (39dc8d3, Aug 2024)\n[PyPy 7.3.17 with GCC 13.2.0]": "3.10.14",
	}
	for raw, want := range valid {
		got, ok := parsePythonVersion(raw)
		if !ok || got != want {
			t.Fatalf("parsePythonVersion(%q)=(%q,%v)，期望 (%q,true)", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"", "python3", "Python", "Python 3", "Python x.y.z"} {
		if got, ok := parsePythonVersion(raw); ok {
			t.Fatalf("parsePythonVersion(%q) 不应接受（返回 %q）", raw, got)
		}
	}
}

// TestPythonImplementation PyPy 横幅识别，其余归 CPython。
func TestPythonImplementation(t *testing.T) {
	if got := pythonImplementation("Python 3.10.14\n[PyPy 7.3.17 with GCC]"); got != ImplementationPyPy {
		t.Fatalf("PyPy 判定=%q", got)
	}
	if got := pythonImplementation("Python 3.12.3"); got != ImplementationCPython {
		t.Fatalf("CPython 判定=%q", got)
	}
}

// TestParsePipVersion pip --version 解析与形状拒绝。
func TestParsePipVersion(t *testing.T) {
	if got := parsePipVersion("pip 25.1.1 from /usr/lib/python3/dist-packages/pip (python 3.14)"); got != "25.1.1" {
		t.Fatalf("pip 版本=%q", got)
	}
	for _, raw := range []string{"", "pip", "not-pip 1.0"} {
		if got := parsePipVersion(raw); got != "" {
			t.Fatalf("parsePipVersion(%q)=%q，期望空串", raw, got)
		}
	}
}

// TestParsePipListCount pip list 计数：容忍提示行、坏 JSON 归零。
func TestParsePipListCount(t *testing.T) {
	cases := map[string]int{
		`[{"name":"a"},{"name":"b"},{"name":"c"}]`: 3,
		"[]":                            0,
		"notice: x\n[{\"name\":\"a\"}]": 1,
		"   {\"name\":\"a\"}":           0,
		"not-json":                      0,
		"":                              0,
		"[{\"name\":\"a\"},":            0,
	}
	for raw, want := range cases {
		if got := parsePipListCount(raw); got != want {
			t.Fatalf("parsePipListCount(%q)=%d，期望 %d", raw, got, want)
		}
	}
}

// TestNormalizeRequestedVersion info.version 入参归一与非法形状拒绝。
func TestNormalizeRequestedVersion(t *testing.T) {
	valid := map[string]string{
		"3.12":          "3.12",
		"3.12.3":        "3.12.3",
		"v3.11.2":       "3.11.2",
		"Python 3.14.4": "3.14.4",
		" 3.9 ":         "3.9",
	}
	for raw, want := range valid {
		got, ok := normalizeRequestedVersion(raw)
		if !ok || got != want {
			t.Fatalf("normalizeRequestedVersion(%q)=(%q,%v)，期望 (%q,true)", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"", "3", "abc", "3.12.3.1", "Python", "3."} {
		if got, ok := normalizeRequestedVersion(raw); ok {
			t.Fatalf("normalizeRequestedVersion(%q) 不应接受（返回 %q）", raw, got)
		}
	}
}

// TestCompareVersions 版本比较按数字段而非字符串。
func TestCompareVersions(t *testing.T) {
	if compareVersions("3.9.0", "3.10.0") >= 0 {
		t.Fatal("3.9.0 应小于 3.10.0")
	}
	if compareVersions("3.12.3", "3.11.9") <= 0 {
		t.Fatal("3.12.3 应大于 3.11.9")
	}
	if compareVersions("3.12.3", "3.12.3") != 0 {
		t.Fatal("同版本应相等")
	}
	if triple, ok := versionTriple("3.12"); !ok || triple != [3]int{3, 12, 0} {
		t.Fatalf("versionTriple(3.12)=%v,%v", triple, ok)
	}
}

// fakeLayout 构造一个包含 PATH python3 / 常见路径 python / pyenv 软链 / 依赖工具的临时环境。
type fakeLayout struct {
	base        string
	pathPython  string
	knownPython string
	pyenvPython string
	pipPath     string
	tools       map[string]string
	runner      fakeRunner
}

func newFakeLayout(t *testing.T) fakeLayout {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	layout := fakeLayout{
		base:        base,
		pathPython:  filepath.Join(base, "bin", "python3"),
		knownPython: filepath.Join(base, "opt", "python3.11", "bin", "python3.11"),
		pyenvPython: filepath.Join(home, ".pyenv", "versions", "3.12.3", "bin", "python3"),
		tools:       map[string]string{},
	}
	writeFile(t, layout.pathPython, "#!/bin/sh\n")
	writeFile(t, layout.knownPython, "#!/bin/sh\n")
	layout.pipPath = filepath.Join(base, "bin", "pip3")
	writeFile(t, layout.pipPath, "#!/bin/sh\n")
	for _, name := range optionalTools {
		path := filepath.Join(base, "bin", name)
		writeFile(t, path, "#!/bin/sh\n")
		layout.tools[name] = path
	}
	// pyenv 候选通过软链指回 PATH 解释器，用于验证按真实路径去重。
	if err := os.MkdirAll(filepath.Dir(layout.pyenvPython), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(layout.pathPython, layout.pyenvPython); err != nil {
		t.Skipf("当前文件系统不支持符号链接: %v", err)
	}
	layout.runner = fakeRunner{
		responses: map[string]string{
			layout.pathPython + " --version":          "Python 3.12.3\n",
			layout.knownPython + " --version":         "Python 3.11.9\n",
			layout.tools["virtualenv"] + " --version": "virtualenv 20.26.5 from /x",
			layout.tools["poetry"] + " --version":     "Poetry (version 1.8.3)",
			layout.tools["pipenv"] + " --version":     "pipenv, version 2024.0.1",
		},
		failures: map[string]error{},
	}
	return layout
}

// runtime 组装注入后的 Runtime（模拟宿主 PATH/pyenv/known_path 三种来源）。
func (l fakeLayout) runtime(getenv func(string) string) *Runtime {
	return &Runtime{
		runner:   l.runner,
		lookPath: l.lookPath,
		homeDir:  func() (string, error) { return filepath.Join(l.base, "home"), nil },
		glob: func(pattern string) ([]string, error) {
			if strings.Contains(pattern, ".pyenv") {
				return []string{l.pyenvPython}, nil
			}
			return nil, nil
		},
		getenv:     getenv,
		knownPaths: []string{l.knownPython},
	}
}

// lookPath 模拟 PATH 解析：python3 / python 指向同一解释器，工具各自命中。
func (l fakeLayout) lookPath(name string) (string, error) {
	switch name {
	case "python3", "python":
		return l.pathPython, nil
	case "pip3", "pip":
		return l.pipPath, nil
	}
	if path, ok := l.tools[name]; ok {
		return path, nil
	}
	return "", errors.New("not found")
}

// addProbeResponses 为默认解释器补齐 info/doctor 用到的固定探测输出。
func (l fakeLayout) addProbeResponses(t *testing.T, siteDir string) {
	t.Helper()
	l.runner.responses[l.pathPython+" -I -c "+scriptExecutable] = "/usr/bin/python3.12"
	l.runner.responses[l.pathPython+" -I -c "+scriptPrefix] = "/usr"
	l.runner.responses[l.pathPython+" -I -c "+scriptSitePackages] = siteDir
	l.runner.responses[l.pathPython+" -I -m pip --version"] = "pip 25.1.1 from /usr/lib/python3/dist-packages/pip (python 3.12)"
	l.runner.responses[l.pathPython+" -I -m pip list --format=json"] = `[{"name":"a"},{"name":"b"},{"name":"c"}]`
	l.runner.responses[l.pathPython+" -I -c "+scriptImportVenv] = ""
}

// TestDetectDedupAndDefault 探测去重（PATH 与 pyenv 软链同一文件）、默认标记与排序。
func TestDetectDedupAndDefault(t *testing.T) {
	layout := newFakeLayout(t)
	result, err := layout.runtime(os.Getenv).Versions()
	if err != nil {
		t.Fatalf("Versions 失败: %v", err)
	}
	if result.Count != 2 {
		t.Fatalf("期望 2 个去重后的安装，实际=%+v", result.Versions)
	}
	if !result.Versions[0].IsDefault || result.Versions[0].Version != "3.12.3" || result.Versions[0].Source != SourcePath {
		t.Fatalf("默认条目错误: %+v", result.Versions[0])
	}
	if result.Versions[0].Implementation != ImplementationCPython {
		t.Fatalf("implementation=%q，期望 CPython", result.Versions[0].Implementation)
	}
	if result.Versions[1].Version != "3.11.9" || result.Versions[1].Source != SourceKnownPath {
		t.Fatalf("第二条目错误: %+v", result.Versions[1])
	}
	if result.DefaultVersion != "3.12.3" {
		t.Fatalf("default_version=%q", result.DefaultVersion)
	}
	defaults := 0
	for _, item := range result.Versions {
		if item.IsDefault {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatalf("is_default=true 的条目不唯一: %d", defaults)
	}
}

// TestDetectSkipsBrokenCandidate 候选 --version 失败只跳过该候选。
func TestDetectSkipsBrokenCandidate(t *testing.T) {
	layout := newFakeLayout(t)
	runtime := layout.runtime(os.Getenv)
	runtime.runner = fakeRunner{
		failures:  map[string]error{layout.pathPython + " --version": errors.New("exec format error")},
		responses: map[string]string{layout.knownPython + " --version": "Python 3.11.9"},
	}
	result, _ := runtime.Versions()
	if result.Count != 1 || result.Versions[0].Version != "3.11.9" {
		t.Fatalf("坏候选未跳过: %+v", result.Versions)
	}
	if result.Versions[0].IsDefault {
		t.Fatalf("PATH 解释器不可用时不应有默认标记: %+v", result.Versions[0])
	}
}

// TestVersionsNoPython 完全没有 Python 时不报错：count=0 + 固定 hint。
func TestVersionsNoPython(t *testing.T) {
	runtime := &Runtime{
		runner:     fakeRunner{},
		lookPath:   func(string) (string, error) { return "", errors.New("not found") },
		homeDir:    func() (string, error) { return t.TempDir(), nil },
		glob:       func(string) ([]string, error) { return nil, nil },
		getenv:     os.Getenv,
		knownPaths: nil,
	}
	result, err := runtime.Versions()
	if err != nil {
		t.Fatalf("无 Python 时不应报错: %v", err)
	}
	if result.Count != 0 || len(result.Versions) != 0 || result.DefaultVersion != "" {
		t.Fatalf("无 Python 结果应为空: %+v", result)
	}
	if result.Hint != NoPythonHint {
		t.Fatalf("hint=%q，期望 %q", result.Hint, NoPythonHint)
	}
}

// TestInfoRealFields 详情汇总真实命令输出（假执行面）：pip / site-packages / 包数 / venv。
func TestInfoRealFields(t *testing.T) {
	layout := newFakeLayout(t)
	siteDir := filepath.Join(layout.base, "site-packages")
	if err := os.MkdirAll(siteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	layout.addProbeResponses(t, siteDir)
	result, err := layout.runtime(os.Getenv).Info("")
	if err != nil {
		t.Fatalf("Info 失败: %v", err)
	}
	if result.Version != "3.12.3" || result.Executable != "/usr/bin/python3.12" || result.Prefix != "/usr" {
		t.Fatalf("版本/可执行/前缀错误: %+v", result)
	}
	if result.PipVersion != "25.1.1" || result.SitePackages != siteDir || result.InstalledPackages != 3 {
		t.Fatalf("pip/site/包数错误: %+v", result)
	}
	if !result.VenvAvailable {
		t.Fatalf("venv_available 应为 true: %+v", result)
	}
}

// TestInfoVersionSelection 指定版本选择：完整版本、主次版本前缀、未安装版本。
func TestInfoVersionSelection(t *testing.T) {
	layout := newFakeLayout(t)
	runtime := layout.runtime(os.Getenv)
	if result, err := runtime.Info("3.11"); err != nil || result.Version != "3.11.9" {
		t.Fatalf("主次版本前缀命中失败: %+v %v", result, err)
	}
	if result, err := runtime.Info("3.12.3"); err != nil || result.Version != "3.12.3" {
		t.Fatalf("完整版本命中失败: %+v %v", result, err)
	}
	if _, err := runtime.Info("9.9.9"); !errors.Is(err, ErrVersionNotInstalled) {
		t.Fatalf("未安装版本错误=%v，期望 ErrVersionNotInstalled", err)
	}
}

// TestInfoNoPython 完全没有 Python 时返回 ErrPythonNotInstalled。
func TestInfoNoPython(t *testing.T) {
	runtime := &Runtime{
		runner:     fakeRunner{},
		lookPath:   func(string) (string, error) { return "", errors.New("not found") },
		homeDir:    func() (string, error) { return t.TempDir(), nil },
		glob:       func(string) ([]string, error) { return nil, nil },
		getenv:     os.Getenv,
		knownPaths: nil,
	}
	if _, err := runtime.Info(""); !errors.Is(err, ErrPythonNotInstalled) {
		t.Fatalf("错误=%v，期望 ErrPythonNotInstalled", err)
	}
}

// TestDoctorChecks doctor 真实检查项：python/pip/venv/site-packages/工具全部 pass，ok=true。
func TestDoctorChecks(t *testing.T) {
	layout := newFakeLayout(t)
	siteDir := filepath.Join(layout.base, "site-packages")
	if err := os.MkdirAll(siteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	layout.addProbeResponses(t, siteDir)
	result, err := layout.runtime(os.Getenv).Doctor("")
	if err != nil {
		t.Fatalf("Doctor 失败: %v", err)
	}
	if !result.OK {
		t.Fatalf("期望 ok=true，实际 checks=%+v", result.Checks)
	}
	statuses := map[string]string{}
	for _, check := range result.Checks {
		statuses[check.Name] = check.Status
		if check.Detail == "" {
			t.Fatalf("check %s 缺少 detail", check.Name)
		}
	}
	names := []string{"python", "pip", "venv", "site_packages_writable", "virtualenv", "poetry", "pipenv"}
	for _, name := range names {
		if statuses[name] != StatusPass {
			t.Fatalf("check %s=%q，期望 pass（全部=%+v）", name, statuses[name], result.Checks)
		}
	}
}

// TestDoctorWarnsWithoutTools 可选工具缺失是 warn，不把 ok 拉成 false。
func TestDoctorWarnsWithoutTools(t *testing.T) {
	layout := newFakeLayout(t)
	siteDir := filepath.Join(layout.base, "site-packages")
	if err := os.MkdirAll(siteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	layout.addProbeResponses(t, siteDir)
	runtime := layout.runtime(os.Getenv)
	runtime.lookPath = func(name string) (string, error) {
		if name == "python3" || name == "python" {
			return layout.pathPython, nil
		}
		return "", errors.New("not found")
	}
	result, err := runtime.Doctor("")
	if err != nil {
		t.Fatalf("Doctor 失败: %v", err)
	}
	if !result.OK {
		t.Fatalf("warn 不应导致 ok=false: %+v", result.Checks)
	}
	for _, check := range result.Checks {
		switch check.Name {
		case "virtualenv", "poetry", "pipenv":
			if check.Status != StatusWarn {
				t.Fatalf("check %s=%q，期望 warn", check.Name, check.Status)
			}
		}
	}
}

// TestDoctorNoPython 无 Python 时 python/pip/venv/site 都是 fail，ok=false。
func TestDoctorNoPython(t *testing.T) {
	runtime := &Runtime{
		runner:     fakeRunner{},
		lookPath:   func(string) (string, error) { return "", errors.New("not found") },
		homeDir:    func() (string, error) { return t.TempDir(), nil },
		glob:       func(string) ([]string, error) { return nil, nil },
		getenv:     os.Getenv,
		knownPaths: nil,
	}
	result, err := runtime.Doctor("")
	if err != nil {
		t.Fatalf("Doctor 不应借用错误返回: %v", err)
	}
	if result.OK {
		t.Fatalf("无 Python 时 ok 应为 false: %+v", result.Checks)
	}
	fails := 0
	for _, check := range result.Checks {
		if check.Status == StatusFail {
			fails++
		}
	}
	if fails < 2 {
		t.Fatalf("期望至少 python/pip 两个 fail，实际=%+v", result.Checks)
	}
}

// TestOSRunnerTimeout 命令执行面必须硬超时，不允许挂死。
func TestOSRunnerTimeout(t *testing.T) {
	start := time.Now()
	_, err := (osRunner{timeout: 20 * time.Millisecond}).Run("sleep", "", "5")
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("错误=%v，期望超时错误", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("超时未生效，耗时 %s", elapsed)
	}
}

// TestDirWritableAccess 目录写权限用 access(2) 真实判定（非 root 下 0555 目录不可写）。
func TestDirWritableAccess(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	writable, reason := dirWritable(dir)
	if os.Geteuid() != 0 && writable {
		t.Fatalf("0555 目录不应判定为可写（reason=%s）", reason)
	}
	writable, reason = dirWritable(t.TempDir())
	if !writable {
		t.Fatalf("临时目录应可写（reason=%s）", reason)
	}
}

// TestDetectInstallMetadata versions 增强字段：安装目录、包管理器与包管理器路径。
func TestDetectInstallMetadata(t *testing.T) {
	layout := newFakeLayout(t)
	result, err := layout.runtime(os.Getenv).Versions()
	if err != nil {
		t.Fatalf("Versions 失败: %v", err)
	}
	if result.Count != 2 {
		t.Fatalf("期望 2 个解释器，实际 %+v", result.Versions)
	}
	for _, item := range result.Versions {
		if item.BinDir != filepath.Dir(item.Path) {
			t.Fatalf("%s 的 bin_dir=%q，期望 %q", item.Version, item.BinDir, filepath.Dir(item.Path))
		}
		if item.PackageManager != PackageManager {
			t.Fatalf("%s 的 package_manager=%q，期望 %q", item.Version, item.PackageManager, PackageManager)
		}
		if item.PackageManagerPath != layout.pipPath {
			t.Fatalf("%s 的 package_manager_path=%q，期望 %q（同目录或 PATH 回退）",
				item.Version, item.PackageManagerPath, layout.pipPath)
		}
	}
}

// TestPathsReportsSearchOrderAndDefault paths：PATH 顺序、可执行名解析、默认解析链与来源顺序。
func TestPathsReportsSearchOrderAndDefault(t *testing.T) {
	layout := newFakeLayout(t)
	runtime := layout.runtime(func(string) string { return "/usr/local/bin:/usr/bin" })
	result, err := runtime.Paths()
	if err != nil {
		t.Fatalf("Paths 失败: %v", err)
	}
	if len(result.PathEnv) != 2 || result.PathEnv[0] != "/usr/local/bin" || result.PathEnv[1] != "/usr/bin" {
		t.Fatalf("path_env=%v", result.PathEnv)
	}
	if len(result.Executables) != 2 {
		t.Fatalf("executables 应覆盖 python3/python: %+v", result.Executables)
	}
	for _, entry := range result.Executables {
		if !entry.Found || entry.Path != layout.pathPython {
			t.Fatalf("可执行名解析异常: %+v", entry)
		}
	}
	if !result.Default.Found || result.Default.Command != "python3" || result.Default.Path != layout.pathPython {
		t.Fatalf("default=%+v", result.Default)
	}
	if result.Default.Version != "3.12.3" || result.Default.Implementation != ImplementationCPython {
		t.Fatalf("default 版本/实现异常: %+v", result.Default)
	}
	if result.Default.PackageManagerPath != layout.pipPath {
		t.Fatalf("default.package_manager_path=%q", result.Default.PackageManagerPath)
	}
	if len(result.SearchOrder) != 3 {
		t.Fatalf("search_order=%+v，期望 path/known_path/pyenv 三条", result.SearchOrder)
	}
	if result.SearchOrder[0].Source != SourcePath || result.SearchOrder[0].Paths[0] != layout.pathPython {
		t.Fatalf("第一条来源异常: %+v", result.SearchOrder[0])
	}
	if result.SearchOrder[1].Source != SourceKnownPath || result.SearchOrder[2].Source != SourcePyenv {
		t.Fatalf("来源顺序异常: %+v", result.SearchOrder)
	}
	// 一致性地基：candidates() 的每条路径都必须出现在 search_order 里。
	listed := map[string]bool{}
	for _, source := range result.SearchOrder {
		for _, path := range source.Paths {
			listed[path] = true
		}
	}
	for _, item := range runtime.candidates() {
		if !listed[item.path] {
			t.Fatalf("候选 %s（source=%s）未出现在 search_order", item.path, item.source)
		}
	}
}

// TestPathsWithoutPython 未安装 Python 时 paths 不报错：default.found=false + hint。
func TestPathsWithoutPython(t *testing.T) {
	runtime := &Runtime{
		runner:     fakeRunner{responses: map[string]string{}, failures: map[string]error{}},
		lookPath:   func(string) (string, error) { return "", errors.New("not found") },
		homeDir:    func() (string, error) { return "", errors.New("no home") },
		glob:       func(string) ([]string, error) { return nil, nil },
		getenv:     func(string) string { return "" },
		knownPaths: nil,
	}
	result, err := runtime.Paths()
	if err != nil {
		t.Fatalf("Paths 失败: %v", err)
	}
	if result.Default.Found || result.Hint != NoPythonHint {
		t.Fatalf("未安装时结果异常: %+v", result)
	}
	if len(result.PathEnv) != 0 || len(result.SearchOrder) != 0 {
		t.Fatalf("空环境结果异常: %+v", result)
	}
}

// TestPathsInstalledButNotOnPath 装了 Python 但 PATH 上没有 python3：hint 指向 PATH 问题而非「未检测到」。
func TestPathsInstalledButNotOnPath(t *testing.T) {
	layout := newFakeLayout(t)
	runtime := layout.runtime(func(string) string { return "/nonexistent" })
	runtime.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	result, err := runtime.Paths()
	if err != nil {
		t.Fatalf("Paths 失败: %v", err)
	}
	if result.Default.Found {
		t.Fatalf("PATH 无 python3 时不应解析出默认条目: %+v", result.Default)
	}
	if len(result.SearchOrder) == 0 {
		t.Fatalf("known/pyenv 候选存在时 search_order 不应为空")
	}
	if result.Hint != PathDefaultHint {
		t.Fatalf("hint=%q，期望 %q", result.Hint, PathDefaultHint)
	}
}
