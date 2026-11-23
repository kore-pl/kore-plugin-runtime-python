package pyop

import (
	"encoding/json"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callPaths 处理 runtime-python.paths：无参数，返回 PATH 解析、默认 Python 解析链与发现来源顺序。
// 未检测到 Python 时不报错：default.found=false + hint 给出安装指引（与 versions 的降级口径一致）。
func callPaths(params json.RawMessage, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p pathsParams
	if err := strictUnmarshal(params, &p); err != nil {
		return nil, invalid("runtime-python.paths 参数非法: " + err.Error())
	}
	result, err := exec.Paths()
	if err != nil {
		return nil, classify(err)
	}
	if result.PathEnv == nil {
		result.PathEnv = []string{}
	}
	if result.Executables == nil {
		result.Executables = []PathExecutable{}
	}
	if result.SearchOrder == nil {
		result.SearchOrder = []SearchSource{}
	}
	if !result.Default.Found && result.Hint == "" {
		result.Hint = NoPythonHint
	}
	return marshal(result)
}
