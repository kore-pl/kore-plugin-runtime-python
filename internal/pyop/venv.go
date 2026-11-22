package pyop

import (
	"encoding/json"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callVenvCreate 处理 python.venv.create：校验版本白名单与路径后返回结构化结果，
// 骨架口径 applied=false，不真建 venv、不内置 python/gunicorn 二进制。
func callVenvCreate(raw json.RawMessage, root string, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p VenvCreateParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	version := p.Version
	if version == "" {
		version = DefaultVersion()
	}
	if !IsSupportedVersion(version) {
		return nil, unsupported("python 版本不在白名单 3.9-3.13 内: " + version)
	}
	abs, perr := ValidatePath(root, p.Path)
	if perr != nil {
		return nil, perr
	}
	applied, err := exec.VenvCreate(root, RelPath(root, abs), version)
	if err != nil {
		return nil, classify(err)
	}
	return encode(VenvCreateResult{
		Version:  version,
		Path:     RelPath(root, abs),
		Gunicorn: true,
		Applied:  applied,
	})
}
