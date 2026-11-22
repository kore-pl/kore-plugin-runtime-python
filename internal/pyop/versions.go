package pyop

import (
	"encoding/json"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// versionsParams 是 runtime-python.versions 的入参形状（当前无必填字段，允许 {}）。
type versionsParams struct{}

// callVersions 处理 runtime-python.versions：返回白名单版本列表与默认版本。
func callVersions(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	var p versionsParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	versions := SupportedVersions()
	return encode(VersionResult{Versions: versions, Count: len(versions), Default: DefaultVersion()})
}
