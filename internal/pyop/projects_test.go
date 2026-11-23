package pyop

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeFile 创建目录（如不存在）并写入内容。
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestParsePyprojectLineLevel pyproject 行级提取：project / poetry / 内联数组 / 未闭合。
func TestParsePyprojectLineLevel(t *testing.T) {
	project := `[project]
name = "demo-app"
version = "1.2.3"
requires-python = ">=3.9"
dependencies = [
  "requests>=2.0",
  "flask>=3.0",
]
`
	meta, err := parsePyproject([]byte(project))
	if err != nil {
		t.Fatalf("project 风格解析失败: %v", err)
	}
	if meta.name != "demo-app" || meta.pythonRequires != ">=3.9" || meta.dependencyCount != 2 {
		t.Fatalf("project 风格提取错误: %+v", meta)
	}

	poetry := `[tool.poetry]
name = "poetry-app"
[tool.poetry.dependencies]
python = "^3.11"
requests = "^2.31"
`
	meta, err = parsePyproject([]byte(poetry))
	if err != nil {
		t.Fatalf("poetry 风格解析失败: %v", err)
	}
	if meta.name != "poetry-app" || meta.pythonRequires != "^3.11" || meta.dependencyCount != 1 {
		t.Fatalf("poetry 风格提取错误: %+v", meta)
	}

	inline := `[project]
name = "inline"
dependencies = ["a", "b", "c"]
`
	meta, err = parsePyproject([]byte(inline))
	if err != nil {
		t.Fatalf("内联数组解析失败: %v", err)
	}
	if meta.dependencyCount != 3 {
		t.Fatalf("内联数组计数=%d，期望 3", meta.dependencyCount)
	}

	broken := `[project]
name = "broken"
dependencies = [
  "requests>=2.0",
`
	if _, err := parsePyproject([]byte(broken)); err == nil {
		t.Fatal("未闭合的 dependencies 数组应返回错误")
	}
}

// TestParseRequirements 依赖计数：跳过空行、注释与 -r/--index 等选项行。
func TestParseRequirements(t *testing.T) {
	content := `# runtime deps
-r base.txt
--index-url https://pypi.org/simple

requests>=2.0
flask==3.0
requests>=2.0 # 重复声明仍计入行数
`
	if got := parseRequirements([]byte(content)).dependencyCount; got != 3 {
		t.Fatalf("依赖计数=%d，期望 3", got)
	}
}

// TestParseSetupPy setup.py 行级提取 name / python_requires / install_requires。
func TestParseSetupPy(t *testing.T) {
	content := `from setuptools import setup

setup(
    name="setup-app",
    python_requires=">=3.8",
    install_requires=[
        "requests>=2.0",
        "flask>=3.0",
    ],
)
`
	meta, err := parseSetupPy([]byte(content))
	if err != nil {
		t.Fatalf("setup.py 解析失败: %v", err)
	}
	if meta.name != "setup-app" || meta.pythonRequires != ">=3.8" || meta.dependencyCount != 2 {
		t.Fatalf("setup.py 提取错误: %+v", meta)
	}

	broken := `setup(name="x", install_requires=[` + "\n" + `    "requests",` + "\n"
	if _, err := parseSetupPy([]byte(broken)); err == nil {
		t.Fatal("未闭合的 install_requires 应返回错误")
	}
}

// TestParsePipfile Pipfile：packages + dev-packages 计数，requires.python_version 作为 python_requires。
func TestParsePipfile(t *testing.T) {
	content := `[[source]]
url = "https://pypi.org/simple"

[requires]
python_version = "3.11"

[packages]
requests = "*"
flask = ">=3.0"

[dev-packages]
pytest = "*"
`
	meta := parsePipfile([]byte(content))
	if meta.pythonRequires != "3.11" || meta.dependencyCount != 3 {
		t.Fatalf("Pipfile 提取错误: %+v", meta)
	}
}

// TestProjectsScan 真实扫描：pyproject / requirements / 坏文件 / 嵌套 / 跳过目录。
func TestProjectsScan(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "good", "pyproject.toml"),
		"[project]\nname = \"good-app\"\nrequires-python = \">=3.9\"\ndependencies = [\n  \"requests>=2.0\",\n  \"flask>=3.0\",\n]\n")
	writeFile(t, filepath.Join(base, "good", "poetry.lock"), "")
	writeFile(t, filepath.Join(base, "good", ".venv", "pyvenv.cfg"), "home = /usr/bin\n")
	writeFile(t, filepath.Join(base, "req", "requirements.txt"), "# deps\nrequests>=2.0\nflask>=3.0\npytest>=8.0\n")
	writeFile(t, filepath.Join(base, "bad", "pyproject.toml"),
		"[project]\nname = \"bad\"\ndependencies = [\n  \"requests>=2.0\",\n")
	writeFile(t, filepath.Join(base, "none", "readme.md"), "no project manifest here")
	writeFile(t, filepath.Join(base, "nested", "deep", "app", "pyproject.toml"), "[project]\nname = \"nested-app\"\n")
	writeFile(t, filepath.Join(base, "too-deep", "a", "b", "c", "pyproject.toml"), "[project]\nname = \"too-deep\"\n")
	writeFile(t, filepath.Join(base, "node_modules", "dep", "pyproject.toml"), "[project]\nname = \"dep\"\n")
	writeFile(t, filepath.Join(base, ".cache", "pyproject.toml"), "[project]\nname = \"hidden\"\n")

	result, err := (&Runtime{}).Projects(base)
	if err != nil {
		t.Fatalf("Projects 失败: %v", err)
	}
	if result.Count != 3 || len(result.Projects) != 3 {
		t.Fatalf("期望 3 个项目（good + nested-app + req），实际=%+v warnings=%v", result.Projects, result.Warnings)
	}
	byName := map[string]Project{}
	for _, project := range result.Projects {
		byName[project.Name] = project
	}
	good, ok := byName["good-app"]
	if !ok {
		t.Fatalf("缺少 good-app: %+v", result.Projects)
	}
	if good.PythonRequires != ">=3.9" || good.DependencyCount != 2 || !good.HasLock || !good.HasVenv || good.ProjectType != ProjectPyproject {
		t.Fatalf("good-app 字段错误: %+v", good)
	}
	if !strings.HasSuffix(good.Path, string(filepath.Separator)+"good") {
		t.Fatalf("路径错误: %q", good.Path)
	}
	req, ok := byName["req"]
	if !ok {
		t.Fatalf("缺少 requirements 项目（name 应回退目录名）: %+v", result.Projects)
	}
	if req.DependencyCount != 3 || req.HasVenv || req.ProjectType != ProjectRequirements {
		t.Fatalf("requirements 项目字段错误: %+v", req)
	}
	if _, ok := byName["nested-app"]; !ok {
		t.Fatalf("深度 3 的嵌套项目未被扫描: %+v", result.Projects)
	}
	if !strings.Contains(strings.Join(result.Warnings, " | "), "解析失败") {
		t.Fatalf("坏文件未写 warnings: %v", result.Warnings)
	}
	if _, ok := byName["too-deep"]; ok {
		t.Fatalf("深度 4 的项目不应被扫描")
	}
	if _, ok := byName["dep"]; ok {
		t.Fatalf("node_modules 下的项目不应被扫描")
	}
	if _, ok := byName["hidden"]; ok {
		t.Fatalf("隐藏目录下的项目不应被扫描")
	}
}

// TestProjectsSkipsOversized 超过 64 KiB 的清单跳过并告警（只读前 64 KiB）。
func TestProjectsSkipsOversized(t *testing.T) {
	base := t.TempDir()
	payload := "[project]\nname = \"huge\"\n# " + strings.Repeat("x", MaxConfigBytes) + "\n"
	writeFile(t, filepath.Join(base, "huge", "pyproject.toml"), payload)
	result, err := (&Runtime{}).Projects(base)
	if err != nil {
		t.Fatalf("Projects 失败: %v", err)
	}
	if result.Count != 0 {
		t.Fatalf("超限文件不应产生项目: %+v", result.Projects)
	}
	if !strings.Contains(strings.Join(result.Warnings, " | "), "超过") {
		t.Fatalf("超限文件未告警: %v", result.Warnings)
	}
}

// TestProjectsSkipsInvalidUTF8 非 UTF-8 清单跳过并告警（行级提取前先验编码）。
func TestProjectsSkipsInvalidUTF8(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "broken", "pyproject.toml"), []byte("[project]\nname = \"\xff\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := (&Runtime{}).Projects(base)
	if err != nil {
		t.Fatalf("Projects 失败: %v", err)
	}
	if result.Count != 0 {
		t.Fatalf("非 UTF-8 文件不应产生项目: %+v", result.Projects)
	}
	if !strings.Contains(strings.Join(result.Warnings, " | "), "UTF-8") {
		t.Fatalf("非 UTF-8 文件未告警: %v", result.Warnings)
	}
}

// TestProjectsEntryLimit 条目达到上限 200 时停止扫描并告警。
func TestProjectsEntryLimit(t *testing.T) {
	base := t.TempDir()
	for i := 0; i < MaxScanEntries+5; i++ {
		if err := os.Mkdir(filepath.Join(base, "d"+strconv.Itoa(i)), 0o755); err != nil {
			t.Fatalf("创建目录失败: %v", err)
		}
	}
	result, err := (&Runtime{}).Projects(base)
	if err != nil {
		t.Fatalf("Projects 失败: %v", err)
	}
	if !strings.Contains(strings.Join(result.Warnings, " | "), "上限") {
		t.Fatalf("条目上限未告警: %v", result.Warnings)
	}
}
