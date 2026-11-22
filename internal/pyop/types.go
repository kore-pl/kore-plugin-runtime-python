// Package pyop 实现 runtime-python 插件的方法面：参数校验、版本白名单、
// 路径约束与结构化结果。本包只做逻辑校验，不真建 venv、不写宿主文件（骨架口径）。
package pyop

import "fmt"

// supportedVersions 是 python 版本白名单（3.9–3.13），闭集合，按语义版本升序。
var supportedVersions = []string{"3.9", "3.10", "3.11", "3.12", "3.13"}

// defaultVersion 是 venv.create 未显式指定版本时的默认版本。
const defaultVersion = "3.11"

// IsSupportedVersion 判断版本是否在白名单内。
func IsSupportedVersion(v string) bool {
	for _, s := range supportedVersions {
		if s == v {
			return true
		}
	}
	return false
}

// SupportedVersions 返回白名单副本，避免调用方修改内部状态。
func SupportedVersions() []string {
	out := make([]string, len(supportedVersions))
	copy(out, supportedVersions)
	return out
}

// DefaultVersion 返回默认 python 版本。
func DefaultVersion() string { return defaultVersion }

// FaultKind 标识一次故障注入的类别。
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

// VersionResult 是 runtime-python.versions 的结构化结果。
type VersionResult struct {
	Versions []string `json:"versions"`
	Count    int      `json:"count"`
	Default  string   `json:"default"`
}

// VenvCreateParams 是 runtime-python.venv.create 的显式入参形状。
type VenvCreateParams struct {
	Version string `json:"version"`
	Path    string `json:"path"`
}

// VenvCreateResult 是 runtime-python.venv.create 的结构化结果（骨架口径 applied=false）。
type VenvCreateResult struct {
	Version  string `json:"version"`
	Path     string `json:"path"`
	Gunicorn bool   `json:"gunicorn"`
	Applied  bool   `json:"applied"`
}

// Executor 是 venv 操作的执行面；骨架实现为 noop，测试可注入故障执行器。
type Executor interface {
	VenvCreate(root, rel, version string) (bool, error)
}
