// Package e2e 在本机对 runtime-python 插件做真实端到端验证：
// 真实 python/pip 探测、真实 site-packages 与包数、真实项目扫描与健康检查，
// 以及"未安装 Python"的降级路径。
package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pengtang1024/kore-plugin-runtime-python/internal/pyop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// versionPattern 是归一化后的 Python 版本号格式。
var versionPattern = regexp.MustCompile(`^\d+\.\d+(\.\d+)?$`)

// call 调用一个方法并打印真实 JSON（验收要求贴真实输出）。
func call(t *testing.T, method, params, stateRoot string) json.RawMessage {
	t.Helper()
	raw, perr := pyop.Call(method, json.RawMessage(params), stateRoot, nil)
	if perr != nil {
		t.Fatalf("%s 失败: code=%s message=%s", method, perr.Code, perr.Message)
	}
	t.Logf("%s => %s", method, raw)
	return raw
}

// requirePython 返回本机 PATH 中的 python3 路径；没有则跳过需要真实解释器的用例。
func requirePython(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("本机 PATH 中没有 python3，真实探测用例跳过（降级路径见 TestVersionsDegraded）")
	}
	return path
}

// TestVersionsReal 真实检测：版本格式、路径存在、source 合法、is_default 唯一且等于 which python3。
func TestVersionsReal(t *testing.T) {
	pythonPath := requirePython(t)
	raw := call(t, "runtime-python.versions", `{}`, "")
	var result pyop.VersionsResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析结果失败: %v", err)
	}
	if result.Count == 0 || len(result.Versions) == 0 {
		t.Fatalf("本机存在 python3（%s），但检测结果为空: %s", pythonPath, raw)
	}
	if result.Count != len(result.Versions) {
		t.Fatalf("count=%d 与 versions 长度 %d 不一致", result.Count, len(result.Versions))
	}
	if !versionPattern.MatchString(result.DefaultVersion) {
		t.Fatalf("default_version 格式非法: %q", result.DefaultVersion)
	}
	defaults := 0
	for _, item := range result.Versions {
		if !versionPattern.MatchString(item.Version) {
			t.Fatalf("版本格式非法: %q", item.Version)
		}
		if _, err := os.Stat(item.Path); err != nil {
			t.Fatalf("版本路径不存在: %q (%v)", item.Path, err)
		}
		switch item.Source {
		case pyop.SourcePath, pyop.SourceKnownPath, pyop.SourcePyenv, pyop.SourceConda:
		default:
			t.Fatalf("source 非法: %q", item.Source)
		}
		if item.Implementation == "" {
			t.Fatalf("implementation 为空: %+v", item)
		}
		if item.IsDefault {
			defaults++
			if item.Version != result.DefaultVersion {
				t.Fatalf("默认条目版本 %q 与 default_version %q 不一致", item.Version, result.DefaultVersion)
			}
			if err := sameFile(pythonPath, item.Path); err != nil {
				t.Fatalf("is_default 条目与 which python3 不一致: %v", err)
			}
		}
	}
	if defaults != 1 {
		t.Fatalf("is_default=true 的条目应恰好 1 个，实际 %d", defaults)
	}
	t.Logf("本机默认 Python %s（%s），共检测到 %d 个安装", result.DefaultVersion, pythonPath, result.Count)
}

// TestInfoReal 详情：真实 pip 版本、site-packages 与已装包数。
func TestInfoReal(t *testing.T) {
	requirePython(t)
	raw := call(t, "runtime-python.info", `{}`, "")
	var result pyop.InfoResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析结果失败: %v", err)
	}
	if !versionPattern.MatchString(result.Version) || result.Executable == "" {
		t.Fatalf("版本/可执行文件为空: %+v", result)
	}
	if _, err := os.Stat(result.Executable); err != nil {
		t.Fatalf("executable 不存在: %v", err)
	}
	if result.Prefix == "" {
		t.Fatalf("prefix 为空: %+v", result)
	}
	if result.PipVersion == "" {
		t.Fatalf("pip_version 为空（python -m pip --version 应成功）: %+v", result)
	}
	if result.SitePackages == "" {
		t.Fatalf("site_packages 为空（sysconfig purelib 应可读）: %+v", result)
	}
	if result.InstalledPackages <= 0 {
		t.Fatalf("installed_packages=%d，期望 >0（pip list --format=json 成功）", result.InstalledPackages)
	}
	if !result.VenvAvailable {
		t.Fatalf("venv_available=false，本机 python3 应带 venv 模块: %+v", result)
	}
	t.Logf("真实详情：python=%s executable=%s prefix=%s pip=%s site=%s packages=%d venv=%v",
		result.Version, result.Executable, result.Prefix, result.PipVersion,
		result.SitePackages, result.InstalledPackages, result.VenvAvailable)
}

// TestInfoVersionNotInstalled 指定未安装版本按 E_INVALID 返回，不误报。
func TestInfoVersionNotInstalled(t *testing.T) {
	requirePython(t)
	_, perr := pyop.Call("runtime-python.info", json.RawMessage(`{"version":"0.0.1"}`), "", nil)
	if perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("错误=%v，期望 E_INVALID", perr)
	}
}

// testRoot 是验收指定的真实项目目录。
const testRoot = "/tmp/kore-py-test"

// prepareProjects 造 3 个项目：pyproject / requirements / 坏文件（未闭合 dependencies 数组）。
func prepareProjects(t *testing.T) {
	t.Helper()
	if err := os.RemoveAll(testRoot); err != nil {
		t.Fatalf("清理 %s 失败: %v", testRoot, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(testRoot) })
	files := map[string]string{
		filepath.Join(testRoot, "py-app", "pyproject.toml"): `[project]
name = "py-app"
version = "1.2.3"
requires-python = ">=3.9"
dependencies = [
  "requests>=2.28",
  "flask>=3.0",
]
`,
		filepath.Join(testRoot, "py-app", "poetry.lock"):         ``,
		filepath.Join(testRoot, "py-app", ".venv", "pyvenv.cfg"): "home = /usr/bin\n",
		filepath.Join(testRoot, "req-app", "requirements.txt"): `# runtime deps
requests>=2.28
flask>=3.0
gunicorn>=21.0
`,
		filepath.Join(testRoot, "bad-app", "pyproject.toml"): `[project]
name = "bad-app"
dependencies = [
  "requests>=2.28",
`,
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("创建目录失败: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("写入文件失败: %v", err)
		}
	}
}

// TestProjectsReal 真实扫描 /tmp/kore-py-test：2 个项目 + 坏文件的 warnings。
func TestProjectsReal(t *testing.T) {
	prepareProjects(t)
	raw := call(t, "runtime-python.projects", `{"root":"`+testRoot+`"}`, testRoot)
	var result pyop.ProjectsResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析结果失败: %v", err)
	}
	if result.Count != 2 || len(result.Projects) != 2 {
		t.Fatalf("期望 2 个项目，实际 count=%d projects=%+v warnings=%v", result.Count, result.Projects, result.Warnings)
	}
	byName := map[string]pyop.Project{}
	for _, project := range result.Projects {
		byName[project.Name] = project
	}
	pyApp, ok := byName["py-app"]
	if !ok {
		t.Fatalf("缺少 py-app: %+v", result.Projects)
	}
	if pyApp.PythonRequires != ">=3.9" || pyApp.DependencyCount != 2 || !pyApp.HasLock || !pyApp.HasVenv || pyApp.ProjectType != pyop.ProjectPyproject {
		t.Fatalf("py-app 字段错误: %+v", pyApp)
	}
	reqApp, ok := byName["req-app"]
	if !ok {
		t.Fatalf("缺少 req-app（name 应回退目录名）: %+v", result.Projects)
	}
	if reqApp.DependencyCount != 3 || reqApp.HasVenv || reqApp.ProjectType != pyop.ProjectRequirements {
		t.Fatalf("req-app 字段错误: %+v", reqApp)
	}
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, " | "), "bad-app") {
		t.Fatalf("坏文件未产生 warnings: %v", result.Warnings)
	}
	t.Logf("扫描到 %d 个项目；warnings=%v", result.Count, result.Warnings)
}

// TestDoctorReal 健康检查返回真实检查项，ok 与 fail 项一致。
func TestDoctorReal(t *testing.T) {
	prepareProjects(t)
	raw := call(t, "runtime-python.doctor", `{"root":"`+testRoot+`"}`, testRoot)
	var result pyop.DoctorResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析结果失败: %v", err)
	}
	if len(result.Checks) == 0 {
		t.Fatal("checks 为空")
	}
	statuses := map[string]string{}
	fails := 0
	for _, check := range result.Checks {
		if check.Name == "" || check.Detail == "" {
			t.Fatalf("检查项字段不完整: %+v", check)
		}
		switch check.Status {
		case pyop.StatusPass, pyop.StatusWarn:
		case pyop.StatusFail:
			fails++
		default:
			t.Fatalf("status 非法: %+v", check)
		}
		statuses[check.Name] = check.Status
		t.Logf("check %-26s %-4s %s", check.Name, check.Status, check.Detail)
	}
	for _, name := range []string{"python", "pip", "venv", "site_packages_writable", "virtualenv", "poetry", "pipenv", "project_root"} {
		if _, ok := statuses[name]; !ok {
			t.Fatalf("缺少检查项 %q: %+v", name, statuses)
		}
	}
	if result.OK != (fails == 0) {
		t.Fatalf("ok=%v 与 fail 数 %d 不一致", result.OK, fails)
	}
	if _, err := exec.LookPath("python3"); err == nil && statuses["python"] != pyop.StatusPass {
		t.Fatalf("本机有 python3，但 python 检查=%q", statuses["python"])
	}
}

// TestVersionsDegraded 无 Python 时 versions 不报错：空数组 + count=0 + 固定 hint。
// 通过"空执行面"走 Call 的降级分支，验证协议层形状（真实无 Python 环境的检测降级见单测与二进制用例）。
func TestVersionsDegraded(t *testing.T) {
	raw, perr := pyop.Call("runtime-python.versions", json.RawMessage(`{}`), "", emptyExecutor{})
	if perr != nil {
		t.Fatalf("无 Python 时不应报错: %v", perr)
	}
	var result pyop.VersionsResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析结果失败: %v", err)
	}
	t.Logf("降级 JSON => %s", raw)
	if result.Count != 0 || len(result.Versions) != 0 {
		t.Fatalf("期望空版本列表: %+v", result)
	}
	if result.Hint != pyop.NoPythonHint {
		t.Fatalf("hint=%q，期望 %q", result.Hint, pyop.NoPythonHint)
	}
	if !strings.Contains(string(raw), `"versions":[]`) {
		t.Fatalf("versions 必须是空数组: %s", raw)
	}
}

// emptyExecutor 模拟"一个 Python 都没有"的宿主执行面（仅用于协议层降级断言）。
type emptyExecutor struct{}

func (emptyExecutor) Versions() (pyop.VersionsResult, error) {
	return pyop.VersionsResult{Versions: []pyop.PythonVersion{}}, nil
}

func (emptyExecutor) Info(string) (pyop.InfoResult, error) {
	return pyop.InfoResult{}, pyop.ErrPythonNotInstalled
}

func (emptyExecutor) Projects(string) (pyop.ProjectsResult, error) {
	return pyop.ProjectsResult{}, nil
}

func (emptyExecutor) Doctor(string) (pyop.DoctorResult, error) {
	return pyop.DoctorResult{Checks: []pyop.Check{{Name: "python", Status: pyop.StatusFail, Detail: pyop.NoPythonHint}}}, nil
}

func (emptyExecutor) Paths() (pyop.PathsResult, error) {
	return pyop.PathsResult{
		PathEnv:     []string{"/usr/bin"},
		Executables: []pyop.PathExecutable{{Name: "python3"}},
		Default:     pyop.DefaultRuntime{Command: "python3"},
		SearchOrder: []pyop.SearchSource{},
		Hint:        pyop.NoPythonHint,
	}, nil
}

// sameFile 判定两个路径是否指向同一文件（跟随符号链接）。
func sameFile(left, right string) error {
	leftReal, err := filepath.EvalSymlinks(left)
	if err != nil {
		return err
	}
	rightReal, err := filepath.EvalSymlinks(right)
	if err != nil {
		return err
	}
	if leftReal != rightReal {
		return &pathMismatchError{left: leftReal, right: rightReal}
	}
	return nil
}

type pathMismatchError struct{ left, right string }

func (e *pathMismatchError) Error() string { return e.left + " != " + e.right }
