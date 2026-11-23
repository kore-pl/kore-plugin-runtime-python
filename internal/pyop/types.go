// Package pyop 实现 runtime-python 插件的方法面：真实探测宿主已安装的 Python、
// 汇总单个解释器详情、扫描 Python 项目与健康检查。
//
// 安全口径：只读——不执行项目脚本、不写宿主文件；所有外部命令固定 argv + 5s 超时；
// root 参数必须落在白名单目录内并做符号链接二次校验。
//
// 零第三方依赖：标准库没有 TOML 解析器，pyproject.toml / Pipfile / setup.py 一律
// 只做行级正则提取（name / requires-python / 依赖计数），不引入任何解析库。
package pyop

import (
	"errors"
	"fmt"
	"time"
)

// 采集与安全常量。
const (
	// CommandTimeout 是单条外部命令（python / pip / 依赖工具）的硬超时。
	CommandTimeout = 5 * time.Second
	// MaxConfigBytes 是项目配置文件的读取上限（只读前 64 KiB，防巨大文件）。
	MaxConfigBytes = 64 * 1024
	// MaxScanDepth 是项目扫描的目录深度上限（root 为 0，最多再下探 3 层）。
	MaxScanDepth = 3
	// MaxScanEntries 是项目扫描的目录条目上限。
	MaxScanEntries = 200
	// NoPythonHint 是未检测到 Python 时的降级提示（versions 不报错）。
	NoPythonHint = "未检测到 Python，可用系统包管理器安装 python3，或用 pyenv / conda"
)

// Source 标识一个 Python 解释器的发现渠道。
type Source string

const (
	// SourcePath 表示从 PATH 中找到（python3 / python）。
	SourcePath Source = "path"
	// SourceKnownPath 表示从常见安装路径找到。
	SourceKnownPath Source = "known_path"
	// SourcePyenv 表示从 ~/.pyenv/versions/*/bin/python3 找到。
	SourcePyenv Source = "pyenv"
	// SourceConda 表示从 conda / miniconda / anaconda 环境找到。
	SourceConda Source = "conda"
)

// 项目清单类型（project_type 取值）。
const (
	ProjectPyproject    = "pyproject"
	ProjectRequirements = "requirements"
	ProjectSetupPy      = "setup.py"
	ProjectPipfile      = "pipfile"
)

// Python 实现名（implementation 字段取值）。
const (
	ImplementationCPython = "CPython"
	ImplementationPyPy    = "PyPy"
)

// 健康检查状态取值。
const (
	StatusPass = "pass"
	StatusWarn = "warn"
	StatusFail = "fail"
)

// FaultKind 标识一次故障注入的类别（basic 级骨架的故障注入契约）。
type FaultKind string

const (
	FaultNone    FaultKind = ""
	FaultKill    FaultKind = "kill"
	FaultTimeout FaultKind = "timeout"
	FaultDisk    FaultKind = "disk"
	FaultCgroup  FaultKind = "cgroup"
)

// FaultError 是执行面注入的故障错误，Call 将其映射为协议错误码。
type FaultError struct {
	Kind    FaultKind
	Message string
}

// Error 实现 error 接口，输出 "<类别>: <说明>"。
func (e *FaultError) Error() string { return fmt.Sprintf("%s: %s", e.Kind, e.Message) }

// NewFaultError 构造给定类别的故障错误。
func NewFaultError(kind FaultKind, message string) *FaultError {
	return &FaultError{Kind: kind, Message: message}
}

// 执行面哨兵错误：用于把"没装 Python"和"指定版本没装"映射为不同协议错误码。
var (
	// ErrPythonNotInstalled 表示宿主一个 Python 都没有。
	ErrPythonNotInstalled = errors.New("未检测到 Python")
	// ErrVersionNotInstalled 表示请求的版本不在已安装列表内。
	ErrVersionNotInstalled = errors.New("指定 Python 版本未安装")
)

// CommandRunner 是外部命令执行面的最小契约：固定 argv、无 shell、带超时。
// pathDir 非空时把该目录前置到子进程 PATH。
type CommandRunner interface {
	Run(name string, pathDir string, args ...string) (string, error)
}

// Executor 是方法执行面：生产实现真实探测宿主；测试可注入故障执行器。
type Executor interface {
	Versions() (VersionsResult, error)
	Info(version string) (InfoResult, error)
	Projects(root string) (ProjectsResult, error)
	Doctor(root string) (DoctorResult, error)
}

// PythonVersion 是一个已安装的 Python 解释器。
type PythonVersion struct {
	Version        string `json:"version"`
	Path           string `json:"path"`
	Source         Source `json:"source"`
	IsDefault      bool   `json:"is_default"`
	Implementation string `json:"implementation,omitempty"`
}

// VersionsResult 是 runtime-python.versions 的结构化结果。
// 未检测到任何 Python 时 versions 为空数组、count=0、hint 给出安装指引（不报错）。
type VersionsResult struct {
	Versions       []PythonVersion `json:"versions"`
	DefaultVersion string          `json:"default_version"`
	Count          int             `json:"count"`
	Hint           string          `json:"hint,omitempty"`
}

// InfoResult 是 runtime-python.info 的结构化结果。
// pip / venv 探测失败时对应字段为空串 / false / 0（不放大为整次失败）。
type InfoResult struct {
	Version           string `json:"version"`
	Executable        string `json:"executable"`
	Prefix            string `json:"prefix"`
	PipVersion        string `json:"pip_version"`
	SitePackages      string `json:"site_packages"`
	InstalledPackages int    `json:"installed_packages"`
	VenvAvailable     bool   `json:"venv_available"`
}

// Project 是一个含 Python 项目清单的条目。
type Project struct {
	Path            string `json:"path"`
	Name            string `json:"name"`
	PythonRequires  string `json:"python_requires,omitempty"`
	DependencyCount int    `json:"dependency_count"`
	HasLock         bool   `json:"has_lock"`
	HasVenv         bool   `json:"has_venv"`
	ProjectType     string `json:"project_type"`
}

// ProjectsResult 是 runtime-python.projects 的结构化结果。
// 单个清单文件超过上限 / 编码非法 / 行级结构损坏时跳过并写入 warnings，不中断整次扫描。
type ProjectsResult struct {
	Projects []Project `json:"projects"`
	Count    int       `json:"count"`
	Warnings []string  `json:"warnings"`
}

// Check 是一条健康检查。
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// DoctorResult 是 runtime-python.doctor 的结构化结果：ok = 没有任何 fail 项。
type DoctorResult struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

// versionsParams / infoParams / projectsParams / doctorParams 是四个方法的显式入参形状。
type versionsParams struct{}

type infoParams struct {
	Version string `json:"version"`
}

type projectsParams struct {
	Root string `json:"root"`
}

type doctorParams struct {
	Root string `json:"root"`
}
