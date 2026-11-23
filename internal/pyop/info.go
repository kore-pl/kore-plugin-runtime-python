package pyop

import (
	"encoding/json"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callInfo 处理 runtime-python.info：version 可省略（取默认解释器），
// 给出时既接受完整版本（3.12.3）也接受主次版本（3.12）。
func callInfo(params json.RawMessage, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p infoParams
	if err := strictUnmarshal(params, &p); err != nil {
		return nil, invalid("runtime-python.info 参数非法: " + err.Error())
	}
	if p.Version != "" {
		normalized, ok := normalizeRequestedVersion(p.Version)
		if !ok {
			return nil, invalid("runtime-python.info version 格式非法（期望 X.Y 或 X.Y.Z）: " + p.Version)
		}
		p.Version = normalized
	}
	result, err := exec.Info(p.Version)
	if err != nil {
		return nil, classify(err)
	}
	return marshal(result)
}
