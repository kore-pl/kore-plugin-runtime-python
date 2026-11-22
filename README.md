# kore-plugin-runtime-python

Python 运行时插件（**骨架**）：提供 `python.versions` / `python.venv.create` 两个方法面，交付 venv + gunicorn 模板的参数校验与结构化结果。本仓**不真建 venv、不内置 python / gunicorn 二进制**（裁决 37 / 提案 §2.1 批 2）。

## 协议面

- `kore.plugin.v1`，`lifecycle=lazy`，`depth_level=basic`
- 能力声明：`system:admin`
- 方法：`python.versions` / `python.venv.create`（同时接受裸 `versions` / `venv.create`）
- 握手的 stdout 留给协议帧，日志一律写 stderr

## 方法面（骨架口径）

### `python.versions`

返回 python 版本白名单与默认版本：

```json
{"versions": ["3.9", "3.10", "3.11", "3.12", "3.13"], "count": 5, "default": "3.11"}
```

### `python.venv.create`

入参 `version`（须在 3.9–3.13 白名单，缺省 `3.11`）与 `path`（配置根目录内的相对路径）：

```json
{"version": "3.12", "path": "envs/app"}
```

返回结构化结果，`applied=false` 表示骨架不落盘、`gunicorn=true` 表示采用 gunicorn 模板：

```json
{"version": "3.12", "path": "envs/app", "gunicorn": true, "applied": false}
```

## 路径约束（骨架口径）

- 拒绝绝对路径、空路径、反斜杠、NUL、`..` 段与越出根目录
- 根目录为 `KORE_PYTHON_ROOT`（缺省 `/var/lib/kore/runtime-python`）
- 只做字符串校验，**不执行 `os.MkdirAll` / `os.WriteFile`**，不调用任何 python 二进制

非法路径返回 `E_INVALID`；版本不在白名单返回 `E_UNSUPPORTED`；未知方法返回 `E_NOTFOUND`。

## 基础故障注入

`tests/fault/` 覆盖 kill -9 与超时两类基础故障，断言方法级返回结构化错误，且 `Error.Code` 与故障类别对应：

| 故障 | 文件 | 期望错误码 |
| --- | --- | --- |
| kill -9 | `kill_test.go` | `E_UPSTREAM` |
| 超时 | `timeout_test.go` | `E_UPSTREAM` |

每个用例配 sanity 检查，防止"永远失败"的假通过。

## 构建与验收

```bash
gofmt -l . && go vet ./... && go test -timeout 200s ./...
make build && make dist
tar -tzf dist/runtime-python-0.1.0.tar.gz | head -1   # info.json
```

`dist` 使用确定性参数归档（`--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner` + `gzip -9n`），重复构建 sha256 不变；`size_bytes` 由打包脚本回填，与实际包体偏差 ≤10%。
