package pyop

import (
	"encoding/json"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callVersions 处理 runtime-python.versions：无参数，返回宿主全部已安装 Python。
// 未检测到任何 Python 时不报错，返回空数组 + count=0 + hint。
func callVersions(params json.RawMessage, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p versionsParams
	if err := strictUnmarshal(params, &p); err != nil {
		return nil, invalid("runtime-python.versions 参数非法: " + err.Error())
	}
	result, err := exec.Versions()
	if err != nil {
		return nil, classify(err)
	}
	if result.Versions == nil {
		result.Versions = []PythonVersion{}
	}
	result.Count = len(result.Versions)
	if result.Count == 0 {
		result.DefaultVersion = ""
		result.Hint = NoPythonHint
	}
	return marshal(result)
}
