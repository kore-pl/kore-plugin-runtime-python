package pyop

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// optionalTools 是常见 Python 依赖/环境工具，缺失只告警（不是所有项目都需要）。
var optionalTools = []string{"virtualenv", "poetry", "pipenv"}

// callDoctor 处理 runtime-python.doctor：root 可选，给出时同样走白名单。
func callDoctor(params json.RawMessage, exec Executor, stateRoot string) (json.RawMessage, *pluginapi.Error) {
	var p doctorParams
	if err := strictUnmarshal(params, &p); err != nil {
		return nil, invalid("runtime-python.doctor 参数非法: " + err.Error())
	}
	root := ""
	if p.Root != "" {
		validated, perr := ValidateRoot(p.Root, stateRoot)
		if perr != nil {
			return nil, perr
		}
		root = validated
	}
	result, err := exec.Doctor(root)
	if err != nil {
		return nil, classify(err)
	}
	if result.Checks == nil {
		result.Checks = []Check{}
	}
	result.OK = true
	for _, check := range result.Checks {
		if check.Status == StatusFail {
			result.OK = false
			break
		}
	}
	return marshal(result)
}

// Doctor 做真实健康检查：Python / pip / venv / site-packages 可写 / 常见工具 / 多版本冲突，
// root 非空时再检查项目目录。ok = 没有任何 fail 项（warn 不拉低 ok）。
func (r *Runtime) Doctor(root string) (DoctorResult, error) {
	versions := r.detect()
	checks := make([]Check, 0, 8)
	checks = append(checks, pythonCheck(versions))
	entry, hasEntry := selectVersion(versions, "")
	checks = append(checks, r.pipCheck(entry, hasEntry))
	checks = append(checks, r.venvCheck(entry, hasEntry))
	checks = append(checks, r.sitePackagesCheck(entry, hasEntry))
	for _, name := range optionalTools {
		checks = append(checks, r.toolCheck(name))
	}
	if len(versions) > 1 {
		checks = append(checks, Check{
			Name:   "path_conflict",
			Status: StatusWarn,
			Detail: fmt.Sprintf("检测到 %d 个 Python 安装（%s），PATH 默认 %s；请在启动脚本里固定解释器",
				len(versions), joinVersionList(versions), defaultVersionOf(versions)),
		})
	}
	if root != "" {
		checks = append(checks, r.projectRootCheck(root))
	}
	result := DoctorResult{OK: true, Checks: checks}
	for _, check := range checks {
		if check.Status == StatusFail {
			result.OK = false
			break
		}
	}
	return result, nil
}

// pythonCheck 检查是否至少有一个可用 Python。
func pythonCheck(versions []PythonVersion) Check {
	if len(versions) == 0 {
		return Check{Name: "python", Status: StatusFail, Detail: NoPythonHint}
	}
	return Check{
		Name:   "python",
		Status: StatusPass,
		Detail: fmt.Sprintf("检测到 %d 个版本，默认 %s（%s）", len(versions), defaultVersionOf(versions), defaultPathOf(versions)),
	}
}

// pipCheck 检查默认解释器的 pip 是否可用（`python -m pip --version`）。
func (r *Runtime) pipCheck(entry PythonVersion, hasEntry bool) Check {
	name := "pip"
	if !hasEntry {
		return Check{Name: name, Status: StatusFail, Detail: "Python 未安装，无法检查 pip"}
	}
	out, err := r.runProbe(entry, "-m", "pip", "--version")
	if err != nil {
		return Check{Name: name, Status: StatusFail, Detail: "pip 不可用（python -m pip --version 失败）: " + entry.Path}
	}
	return Check{Name: name, Status: StatusPass, Detail: fmt.Sprintf("pip %s（%s）", parsePipVersion(out), entry.Path)}
}

// venvCheck 检查默认解释器是否带 venv 模块（本插件建虚拟环境的前提）。
func (r *Runtime) venvCheck(entry PythonVersion, hasEntry bool) Check {
	name := "venv"
	if !hasEntry {
		return Check{Name: name, Status: StatusFail, Detail: "Python 未安装，无法检查 venv 模块"}
	}
	if r.venvAvailable(entry) {
		return Check{Name: name, Status: StatusPass, Detail: "venv 模块可用: " + entry.Path}
	}
	return Check{Name: name, Status: StatusFail, Detail: "venv 模块不可用（该解释器可能被裁剪）: " + entry.Path}
}

// sitePackagesCheck 用 sysconfig 定位 site-packages 并检查写权限位。
// 系统解释器的 site-packages 通常不可写（应改用 venv），因此不可写只告警。
func (r *Runtime) sitePackagesCheck(entry PythonVersion, hasEntry bool) Check {
	name := "site_packages_writable"
	if !hasEntry {
		return Check{Name: name, Status: StatusFail, Detail: "Python 未安装，无法定位 site-packages"}
	}
	site := r.sitePackages(entry)
	if site == "" {
		return Check{Name: name, Status: StatusWarn, Detail: "sysconfig 未返回 site-packages 路径: " + entry.Path}
	}
	if info, err := os.Stat(site); err != nil || !info.IsDir() {
		return Check{Name: name, Status: StatusWarn, Detail: "site-packages 不存在或不是目录: " + site}
	}
	writable, reason := dirWritable(site)
	if writable {
		return Check{Name: name, Status: StatusPass, Detail: fmt.Sprintf("site-packages 可写: %s（%s）", site, reason)}
	}
	return Check{Name: name, Status: StatusWarn, Detail: fmt.Sprintf("site-packages 不可写: %s（%s）；系统解释器建议改用 venv 安装依赖", site, reason)}
}

// toolCheck 检查一个可选工具是否安装（存在即 pass，缺失是 warn）。
func (r *Runtime) toolCheck(name string) Check {
	path, err := r.lookPath(name)
	if err != nil || path == "" {
		return Check{Name: name, Status: StatusWarn, Detail: "未安装（可选）: " + name}
	}
	if out, runErr := r.runner.Run(path, "", "--version"); runErr == nil && strings.TrimSpace(out) != "" {
		return Check{Name: name, Status: StatusPass, Detail: firstLine(out) + "（" + path + "）"}
	}
	return Check{Name: name, Status: StatusPass, Detail: "已安装: " + path + "（--version 无输出）"}
}

// projectRootCheck 检查指定 root 下是否能发现 Python 项目。
func (r *Runtime) projectRootCheck(root string) Check {
	result, err := r.Projects(root)
	if err != nil {
		return Check{Name: "project_root", Status: StatusFail, Detail: "项目目录扫描失败: " + err.Error()}
	}
	if result.Count == 0 {
		return Check{Name: "project_root", Status: StatusWarn, Detail: "目录下未发现 Python 项目清单: " + root}
	}
	return Check{Name: "project_root", Status: StatusPass, Detail: fmt.Sprintf("目录下发现 %d 个 Python 项目: %s", result.Count, root)}
}

// dirWritable 判定当前进程是否可写目录：优先 access(2) 真实探测，
// 不支持该调用的平台退回权限位启发式，并返回人类可读依据。
func dirWritable(path string) (bool, string) {
	info, err := os.Stat(path)
	if err != nil {
		return false, "目录不存在或不可访问"
	}
	if !info.IsDir() {
		return false, "不是目录"
	}
	if writable, supported := probeDirWritable(path); supported {
		if writable {
			return true, "access(W_OK)=可写"
		}
		return false, "access(W_OK)=不可写"
	}
	mode := info.Mode().Perm()
	if os.Geteuid() == 0 {
		return true, fmt.Sprintf("mode=%04o, uid=0", mode)
	}
	if mode&0o222 != 0 {
		return true, fmt.Sprintf("mode=%04o（权限位启发式）", mode)
	}
	return false, fmt.Sprintf("mode=%04o 无写权限位", mode)
}

// defaultVersionOf 返回默认版本号；没有默认时退回第一个。
func defaultVersionOf(versions []PythonVersion) string {
	if entry, ok := selectVersion(versions, ""); ok {
		return entry.Version
	}
	return ""
}

// defaultPathOf 返回默认解释器路径。
func defaultPathOf(versions []PythonVersion) string {
	if entry, ok := selectVersion(versions, ""); ok {
		return entry.Path
	}
	return ""
}
