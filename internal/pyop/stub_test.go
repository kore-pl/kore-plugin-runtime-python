package pyop

import (
	"fmt"
	"strings"
)

// stubExecutor 是方法层的假执行面：只返回预设结果，便于验证参数校验与错误映射。
type stubExecutor struct {
	versions    VersionsResult
	versionsErr error

	info    InfoResult
	infoErr error
	infoArg string

	projects    ProjectsResult
	projectsErr error
	projectsArg string

	doctor    DoctorResult
	doctorErr error
	doctorArg string
}

func (s *stubExecutor) Versions() (VersionsResult, error) { return s.versions, s.versionsErr }

func (s *stubExecutor) Info(version string) (InfoResult, error) {
	s.infoArg = version
	return s.info, s.infoErr
}

func (s *stubExecutor) Projects(root string) (ProjectsResult, error) {
	s.projectsArg = root
	return s.projects, s.projectsErr
}

func (s *stubExecutor) Doctor(root string) (DoctorResult, error) {
	s.doctorArg = root
	return s.doctor, s.doctorErr
}

// fakeRunner 按「命令 + 参数」返回预设输出，用于在不碰宿主环境的前提下验证探测逻辑。
type fakeRunner struct {
	responses map[string]string
	failures  map[string]error
}

func (f fakeRunner) Run(name, pathDir string, args ...string) (string, error) {
	key := name + " " + strings.Join(args, " ")
	if err, ok := f.failures[key]; ok {
		return "", err
	}
	if out, ok := f.responses[key]; ok {
		return out, nil
	}
	return "", fmt.Errorf("fakeRunner 未预设命令: %s", key)
}
