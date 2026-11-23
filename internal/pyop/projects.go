package pyop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// projectMarkers 是项目清单的识别顺序（同时也是 project_type 的取值）。
// 同一目录命中多个清单时按此顺序取第一个，保证结果确定。
var projectMarkers = []struct {
	file string
	kind string
}{
	{"pyproject.toml", ProjectPyproject},
	{"requirements.txt", ProjectRequirements},
	{"setup.py", ProjectSetupPy},
	{"Pipfile", ProjectPipfile},
}

// lockFiles 是判定 has_lock 的锁文件集合（poetry / pipenv / uv / pdm）。
var lockFiles = []string{"poetry.lock", "Pipfile.lock", "uv.lock", "pdm.lock"}

// venvDirs 是判定 has_venv 的虚拟环境目录名。
var venvDirs = []string{".venv", "venv", "env"}

// 行级提取用的正则（不解析完整 TOML：标准库没有 TOML 解析器，也不引第三方库）。
var (
	assignmentName      = regexp.MustCompile(`^name\s*=\s*["']([^"']*)["']`)
	assignmentPyReq     = regexp.MustCompile(`^requires-python\s*=\s*["']([^"']*)["']`)
	setupPyRequires     = regexp.MustCompile(`^python_requires\s*=\s*["']([^"']*)["']`)
	assignmentPyVersion = regexp.MustCompile(`^python(_full)?_version\s*=\s*["']([^"']*)["']`)
	assignmentPoetryPy  = regexp.MustCompile(`^python\s*=\s*["']([^"']*)["']`)
	assignmentKey       = regexp.MustCompile(`^([A-Za-z0-9_.-]+)\s*=`)
	dependenciesOpen    = regexp.MustCompile(`^dependencies\s*=\s*\[(.*)$`)
	quotedItem          = regexp.MustCompile(`"[^"]*"|'[^']*'`)
	installRequiresPos  = regexp.MustCompile(`install_requires\s*=\s*\[`)
)

// projectMeta 是行级提取出来的项目元信息。
type projectMeta struct {
	name            string
	pythonRequires  string
	dependencyCount int
}

// callProjects 处理 runtime-python.projects：root 必填且必须在白名单内。
func callProjects(params json.RawMessage, exec Executor, stateRoot string) (json.RawMessage, *pluginapi.Error) {
	var p projectsParams
	if err := strictUnmarshal(params, &p); err != nil {
		return nil, invalid("runtime-python.projects 参数非法: " + err.Error())
	}
	root, perr := ValidateRoot(p.Root, stateRoot)
	if perr != nil {
		return nil, perr
	}
	result, err := exec.Projects(root)
	if err != nil {
		return nil, classify(err)
	}
	if result.Projects == nil {
		result.Projects = []Project{}
	}
	if result.Warnings == nil {
		result.Warnings = []string{}
	}
	result.Count = len(result.Projects)
	return marshal(result)
}

// Projects 扫描 root 下深度 ≤3 的 Python 项目（含 root 自身）。
// 条目数上限 200；清单文件只读前 64 KiB；坏文件跳过并写 warnings。
// 不跟随目录符号链接，不执行项目脚本，只读元数据。
func (r *Runtime) Projects(root string) (ProjectsResult, error) {
	result := ProjectsResult{Projects: []Project{}, Warnings: []string{}}
	scanned := 0
	truncated := false
	var walk func(dir string, depth int) bool
	walk = func(dir string, depth int) bool {
		r.appendProject(dir, &result)
		if depth >= MaxScanDepth {
			return true
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			addWarning(&result.Warnings, "目录不可读，已跳过: %s", dir)
			return true
		}
		for _, entry := range entries {
			if scanned >= MaxScanEntries {
				truncated = true
				return false
			}
			scanned++
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if name == "node_modules" || name == "__pycache__" || strings.HasPrefix(name, ".") {
				continue
			}
			if !walk(filepath.Join(dir, name), depth+1) {
				return false
			}
		}
		return true
	}
	walk(root, 0)
	if truncated {
		addWarning(&result.Warnings, "扫描条目达到上限 %d，结果可能不完整", MaxScanEntries)
	}
	sort.Slice(result.Projects, func(i, j int) bool { return result.Projects[i].Path < result.Projects[j].Path })
	result.Count = len(result.Projects)
	return result, nil
}

// appendProject 依次尝试项目清单，命中第一个就读取并追加项目；
// 缺失不是项目，坏文件写 warning 后跳过。
func (r *Runtime) appendProject(dir string, result *ProjectsResult) {
	for _, marker := range projectMarkers {
		manifestPath := filepath.Join(dir, marker.file)
		info, err := os.Lstat(manifestPath)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if info.Size() > MaxConfigBytes {
			addWarning(&result.Warnings, "%s 超过 %d 字节，已跳过: %s", marker.file, MaxConfigBytes, manifestPath)
			return
		}
		data, readErr := readLimited(manifestPath)
		if readErr != nil {
			addWarning(&result.Warnings, "读取 %s 失败，已跳过: %s", marker.file, manifestPath)
			return
		}
		data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
		if !utf8.Valid(data) {
			addWarning(&result.Warnings, "%s 不是合法 UTF-8 文本，已跳过: %s", marker.file, manifestPath)
			return
		}
		meta, parseErr := parseProjectFile(marker.kind, data)
		if parseErr != nil {
			addWarning(&result.Warnings, "%s 解析失败，已跳过: %s（%v）", marker.file, manifestPath, parseErr)
			return
		}
		name := meta.name
		if name == "" {
			name = filepath.Base(dir)
		}
		result.Projects = append(result.Projects, Project{
			Path:            dir,
			Name:            name,
			PythonRequires:  meta.pythonRequires,
			DependencyCount: meta.dependencyCount,
			HasLock:         hasAnyFile(dir, lockFiles),
			HasVenv:         hasAnyDir(dir, venvDirs),
			ProjectType:     marker.kind,
		})
		return
	}
}

// parseProjectFile 按清单类型做行级提取；行级结构损坏（如数组未闭合）返回错误。
func parseProjectFile(kind string, data []byte) (projectMeta, error) {
	switch kind {
	case ProjectPyproject:
		return parsePyproject(data)
	case ProjectRequirements:
		return parseRequirements(data), nil
	case ProjectSetupPy:
		return parseSetupPy(data)
	case ProjectPipfile:
		return parsePipfile(data), nil
	default:
		return projectMeta{}, errors.New("未知清单类型: " + kind)
	}
}

// parsePyproject 行级提取 [project] / [tool.poetry] 的 name、requires-python 与依赖数。
// 只做简单正则与数组计数；`dependencies = [` 未闭合视为文件损坏。
func parsePyproject(data []byte) (projectMeta, error) {
	var meta projectMeta
	section := ""
	inDeps := false
	poetryDeps := 0
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") && !inDeps {
			section = strings.Trim(line, "[] \t")
			continue
		}
		if inDeps {
			if closing := strings.Index(line, "]"); closing >= 0 {
				meta.dependencyCount += countQuotedItems(line[:closing])
				inDeps = false
				continue
			}
			if strings.HasPrefix(line, `"`) || strings.HasPrefix(line, `'`) {
				meta.dependencyCount++
			}
			continue
		}
		switch section {
		case "project":
			if match := assignmentName.FindStringSubmatch(line); match != nil && meta.name == "" {
				meta.name = match[1]
			}
			if match := assignmentPyReq.FindStringSubmatch(line); match != nil && meta.pythonRequires == "" {
				meta.pythonRequires = match[1]
			}
			if match := dependenciesOpen.FindStringSubmatch(line); match != nil {
				rest := match[1]
				meta.dependencyCount += countQuotedItems(rest)
				if !strings.Contains(rest, "]") {
					inDeps = true
				}
			}
		case "tool.poetry":
			if match := assignmentName.FindStringSubmatch(line); match != nil && meta.name == "" {
				meta.name = match[1]
			}
		case "tool.poetry.dependencies":
			if match := assignmentPoetryPy.FindStringSubmatch(line); match != nil {
				if meta.pythonRequires == "" {
					meta.pythonRequires = match[1]
				}
				continue
			}
			if match := assignmentKey.FindStringSubmatch(line); match != nil {
				poetryDeps++
			}
		}
	}
	if inDeps {
		return projectMeta{}, errors.New("dependencies 数组未闭合")
	}
	if meta.dependencyCount == 0 {
		meta.dependencyCount = poetryDeps
	}
	return meta, nil
}

// parseRequirements 统计 requirements.txt 的依赖行：跳过空行、注释与 -r/--index 等选项行。
func parseRequirements(data []byte) projectMeta {
	var meta projectMeta
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		meta.dependencyCount++
	}
	return meta
}

// parseSetupPy 行级提取 name / python_requires 并统计 install_requires 数组的条目数。
// install_requires 数组未闭合视为文件损坏。
func parseSetupPy(data []byte) (projectMeta, error) {
	var meta projectMeta
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if match := assignmentName.FindStringSubmatch(line); match != nil && meta.name == "" {
			meta.name = match[1]
		}
		if match := setupPyRequires.FindStringSubmatch(line); match != nil && meta.pythonRequires == "" {
			meta.pythonRequires = match[1]
		}
	}
	text := string(data)
	location := installRequiresPos.FindStringIndex(text)
	if location == nil {
		return meta, nil
	}
	remainder := text[location[1]:]
	closing := strings.Index(remainder, "]")
	if closing < 0 {
		return projectMeta{}, errors.New("install_requires 数组未闭合")
	}
	meta.dependencyCount = countQuotedItems(remainder[:closing])
	return meta, nil
}

// parsePipfile 行级提取 [packages]/[dev-packages] 依赖数与 [requires] 的 Python 版本。
func parsePipfile(data []byte) projectMeta {
	var meta projectMeta
	section := ""
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[] \t")
			continue
		}
		switch section {
		case "packages", "dev-packages":
			if assignmentKey.MatchString(line) {
				meta.dependencyCount++
			}
		case "requires":
			if match := assignmentPyVersion.FindStringSubmatch(line); match != nil {
				meta.pythonRequires = match[2]
			}
		}
	}
	return meta
}

// countQuotedItems 统计一段文本里的带引号条目数。
func countQuotedItems(text string) int {
	return len(quotedItem.FindAllString(text, -1))
}

// readLimited 只读取文件前 MaxConfigBytes 字节（防巨大文件耗内存）。
func readLimited(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(io.LimitReader(file, MaxConfigBytes))
}

// hasAnyFile 判断目录下是否存在任一给定名称的普通文件。
func hasAnyFile(dir string, names []string) bool {
	for _, name := range names {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// hasAnyDir 判断目录下是否存在任一给定名称的子目录。
func hasAnyDir(dir string, names []string) bool {
	for _, name := range names {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// addWarning 追加一条告警；相同内容只保留一次。
func addWarning(warnings *[]string, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	for _, existing := range *warnings {
		if existing == message {
			return
		}
	}
	*warnings = append(*warnings, message)
}
