# kore-plugin-runtime-python

Python 运行时插件（**基础级**）：真实检测宿主已安装的 Python、汇总单解释器详情、
扫描 Python 项目并做健康检查。只读元数据——**不执行项目脚本、不写宿主文件、不内置 python 二进制**。

## 协议面

- `kore.plugin.v1`，`lifecycle=lazy`，`depth_level=production`，插件级能力 `system:admin`（仅作未声明方法的 fail-closed 回退）
- **方法级能力**（`method_capabilities`）：5 个方法全为 `system:read`——只读探测无需管理权限，内核据此不再把只读调用计入写审计
- 方法：`runtime-python.versions` / `runtime-python.info` / `runtime-python.projects` / `runtime-python.doctor` / `runtime-python.paths`
  （域必须等于插件 id `runtime-python`，PLUGIN-PROTOCOL §7.2；不接受裸动词或旧 `python.` 前缀）
- 握手的 stdout 留给协议帧，日志一律写 stderr；未知方法 `E_NOTFOUND`，参数非法 `E_INVALID`

## 方法

### `runtime-python.versions`（无参数）

探测宿主全部 Python 安装：PATH 里的 `python3` / `python`、`/usr/bin/python3*`、
`/usr/local/bin/python3*`、`/opt/python*/bin/python3`、`~/.pyenv/versions/*/bin/python3`、
conda / miniconda / anaconda（`~/miniconda3`、`~/anaconda3`、`~/.conda/envs/*`、`/opt/conda`）。
每个候选执行 `<path> --version`，解析出完整版本号与实现名；同一真实文件按符号链接去重。

```json
{
  "versions": [
    {"version": "3.14.4", "path": "/usr/bin/python3", "source": "path", "is_default": true,
     "implementation": "CPython", "bin_dir": "/usr/bin",
     "package_manager": "pip", "package_manager_path": "/usr/bin/pip3"}
  ],
  "default_version": "3.14.4",
  "count": 1
}
```

- `source` ∈ `path` / `known_path` / `pyenv` / `conda`；`implementation` ∈ `CPython` / `PyPy`
- `bin_dir` 是安装目录；`package_manager` 固定 `pip`；`package_manager_path` 优先同目录 `pip3` / `pip`、
  其次 PATH（**只做文件查找，不执行命令**，不会为每个解释器额外拉起进程）
- `is_default` 由 `which python3`（PATH 解析，缺失时退回 `which python`）决定，最多一个
- **未检测到 Python 不报错**：`versions=[]`、`count=0`、`default_version=""`，并给固定 `hint`：
  `未检测到 Python，可用系统包管理器安装 python3，或用 pyenv / conda`
- 候选过滤：只接受 `python3` 与 `python3.N` 两种文件名，`python3-config` / `python3.14-dbg` 等不会被误认为解释器

### `runtime-python.info {version?}`

`version` 省略时取默认解释器；给出时支持完整版本（`3.12.3`）与主次版本（`3.12`），
也接受 `v3.12.3` / `Python 3.12.3`。探测命令全部是固定 argv：

| 字段 | 探测方式 |
| --- | --- |
| `version` | `<python> --version` |
| `executable` | `<python> -I -c "import sys;print(sys.executable)"` |
| `prefix` | `<python> -I -c "import sys;print(sys.prefix)"` |
| `pip_version` | `<python> -I -m pip --version`（解析 `pip X.Y.Z`） |
| `site_packages` | `<python> -I -c "import sysconfig;print(sysconfig.get_paths().get('purelib',''))"` |
| `installed_packages` | `<python> -I -m pip list --format=json` 的数组长度，失败为 0 |
| `venv_available` | `<python> -I -c "import venv"` 是否成功 |

```json
{
  "version": "3.14.4",
  "executable": "/usr/bin/python3",
  "prefix": "/usr",
  "pip_version": "25.1.1",
  "site_packages": "/usr/local/lib/python3.14/dist-packages",
  "installed_packages": 103,
  "venv_available": true
}
```

单版本未安装返回 `E_INVALID`（message 列出已检测版本）；宿主完全没有 Python 返回 `E_NOTFOUND` + 安装提示。
pip / site-packages / venv 单项探测失败只让对应字段为空 / 0 / false，不放大为整次失败。

### `runtime-python.projects {root}`

扫描 `root` 下**深度 ≤3**、**条目 ≤200** 的目录，收集含 Python 项目清单的条目（含 root 自身）；
跳过 `node_modules`、`__pycache__`、隐藏目录，不跟随目录符号链接。
同一目录命中多个清单时按 `pyproject.toml` → `requirements.txt` → `setup.py` → `Pipfile` 取第一个。

```json
{
  "projects": [
    {"path": "/tmp/kore-py-test/py-app", "name": "py-app", "python_requires": ">=3.9",
     "dependency_count": 2, "has_lock": true, "has_venv": true, "project_type": "pyproject"}
  ],
  "count": 1,
  "warnings": ["pyproject.toml 解析失败，已跳过: /tmp/kore-py-test/bad-app/pyproject.toml（dependencies 数组未闭合）"]
}
```

- `project_type` ∈ `pyproject` / `requirements` / `setup.py` / `pipfile`
- `has_lock` 命中 `poetry.lock` / `Pipfile.lock` / `uv.lock` / `pdm.lock`
- `has_venv` 命中 `.venv` / `venv` / `env` 目录
- 清单文件**只读前 64 KiB**；超限 / 非 UTF-8 / 行级结构损坏（如 `dependencies = [` 未闭合）一律跳过并写
  `warnings[]`，不中断整次扫描
- **不执行**项目的任何脚本，只读元数据

#### TOML 为什么不引库：行级提取口径

标准库没有 TOML 解析器，而本仓硬约束是零第三方依赖，因此 `pyproject.toml` / `Pipfile` / `setup.py`
只做**行级正则提取**，不做完整 TOML 语义校验：

- 用 `[section]` 行跟踪当前段：`[project]` 抓 `name` / `requires-python` / `dependencies` 计数，
  `[tool.poetry]` 抓 `name`，`[tool.poetry.dependencies]` 抓 `python` 约束与依赖条目数，
  `[requires]` 抓 `python_version`，`[packages]` / `[dev-packages]` 抓依赖条目数
- `dependencies = [` 起多行数组：统计后续以引号开头的行，遇到 `]` 收口；整段扫完仍未闭合 → 判定文件损坏并告警
- 内联数组 `dependencies = ["a", "b"]` 直接统计引号条目
- 依赖计数是**声明条目数**，不解析版本约束、不合并重复项、不展开 extras
- 已知局限：注释里的伪键、非标准 TOML 写法可能被计入或漏计；这里服务的是"扫一遍有哪些项目、大致多少依赖"，
  不承担 `pyproject.toml` 的严格校验职责

### `runtime-python.doctor {root?}`

真实检查：Python 是否安装、pip 是否可用、venv 模块是否可用、site-packages 是否可写、
可选工具（virtualenv / poetry / pipenv）、多版本 PATH 冲突；给出 `root` 时追加项目目录检查。
`ok = 没有任何 fail 项`（warn 不拉低 ok）。

```json
{
  "ok": true,
  "checks": [
    {"name": "python", "status": "pass", "detail": "检测到 1 个版本，默认 3.14.4（/usr/bin/python3）"},
    {"name": "pip", "status": "pass", "detail": "pip 25.1.1（/usr/bin/python3）"},
    {"name": "venv", "status": "pass", "detail": "venv 模块可用: /usr/bin/python3"},
    {"name": "site_packages_writable", "status": "warn", "detail": "site-packages 不可写: /usr/local/lib/python3.14/dist-packages（access(W_OK)=不可写）；系统解释器建议改用 venv 安装依赖"},
    {"name": "virtualenv", "status": "warn", "detail": "未安装（可选）: virtualenv"}
  ]
}
```

- Python 未装、pip 不可用、venv 模块缺失 → `fail`；site-packages 不可写与可选工具缺失 → `warn`
- site-packages 不可写只告警：系统解释器本就如此，正确做法是用 venv 装依赖

## 安全

- **root 白名单**：`/var/www`、`/opt`、`/home` 与插件状态目录（`KORE_PYTHON_ROOT`，默认 `/var/lib/kore/runtime-python`）；
  必须是绝对路径、无 NUL / 反斜杠 / `..`；`ResolveExisting`（`EvalSymlinks` + 存在性 + 目录）后再比对归属，
  挡住父目录被换成软链的逃逸
- **只读**：不执行项目脚本、不写任何宿主文件；清单文件读取上限 64 KiB
- **命令固定 argv + 5s 超时**：python / pip / 工具一律走 `exec.CommandContext`，无 shell；
  解释器探测统一加 `-I`（隔离模式：忽略 `PYTHON*` 环境变量与 user site），探测脚本是编译期常量，不拼接用户输入
- **零第三方依赖**：`go.mod` 只依赖 `kore/pkg/pluginapi`；TOML 走行级正则，见上节
- **零轮询**：无 ticker / 后台 goroutine，全部探测都在单次请求内完成

### `runtime-python.paths`（无参数）

解释「**为什么用到了这个解释器**」：PATH 原始顺序、候选可执行名解析结果、默认解释器的解析链，
以及按探测优先级排列的发现来源。只读：复用 `versions` 的同一套候选收集逻辑与一次 `--version`，不写宿主。

```json
{
  "path_env": ["/usr/local/sbin", "/usr/local/bin", "/usr/bin"],
  "executables": [
    {"name": "python3", "path": "/usr/bin/python3", "found": true},
    {"name": "python", "path": "", "found": false}
  ],
  "default": {
    "command": "python3", "path": "/usr/bin/python3", "real_path": "/usr/bin/python3.12",
    "version": "3.12.3", "implementation": "CPython", "bin_dir": "/usr/bin",
    "package_manager": "pip", "package_manager_path": "/usr/bin/pip3",
    "found": true
  },
  "search_order": [
    {"source": "path", "paths": ["/usr/bin/python3"]},
    {"source": "known_path", "paths": ["/usr/local/bin/python3"]},
    {"source": "pyenv", "paths": ["/root/.pyenv/versions/3.12.3/bin/python3"]}
  ]
}
```

- `search_order` 的条目顺序 = 实际探测优先级（PATH → 常见路径 → pyenv / conda 通配），只列**真实存在**的来源
- 未检测到 Python 时不报错：`default.found=false`、`search_order=[]`，并给与 `versions` 相同的 `hint`
- 装了 Python 但 PATH 上找不到 `python3` / `python` 时，`hint` 会说明是「PATH 上没找到」而不是「未检测到 Python」

## 测试

- 单元：版本解析（`Python 3.14.4` / PyPy 横幅 / `Python 2.7.18`）、pip 版本与 `pip list` 计数、
  info.version 归一、路径白名单与软链逃逸、pyproject 行级提取（`name` / `requires-python` / 依赖数 /
  内联数组 / 未闭合数组）、requirements 计数、setup.py、Pipfile、超 64 KiB 截断、非 UTF-8 跳过、
  坏文件跳过、条目上限、venv 检测、去重 / 排序 / `is_default`、命令硬超时
- 真实 E2E（`tests/e2e/`）：本机真实 Python 版本与 `which python3` 一致、真实 pip / site-packages / 包数 / venv；
  在 `/tmp/kore-py-test` 造 3 个项目（pyproject / requirements / 坏文件）→ 返回 2 个 + warnings；
  doctor 真实检查项；**真实二进制会话**：空 `PATH` + 空 `HOME` 启动插件，versions 返回真实条目或
  `count=0 + 固定 hint`，info 按 `E_NOTFOUND` 降级（不崩溃）
- `tests/fault/` 四类故障注入（kill / timeout / disk / cgroup）验证四个方法的协议错误码，并配 sanity 防假通过

## 构建与验收

```bash
gofmt -l . && go vet ./... && go test -timeout 200s ./...
make build && make dist
tar -tzf dist/runtime-python-0.2.3.tar.gz | head -1   # info.json
```

`dist` 使用确定性参数归档（`--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner` + `gzip -9n`），
同一源码在干净 clone 重打包 sha256 不变；`size_bytes` 由打包脚本迭代回填，与实测包体偏差 ≤2 字节。

## 许可证

MIT，见 [LICENSE](LICENSE)。

<!-- oss:begin -->
## 开源与贡献

[![CI](https://github.com/kore-pl/kore-plugin-runtime-python/actions/workflows/ci.yml/badge.svg)](https://github.com/kore-pl/kore-plugin-runtime-python/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go 1.26](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)

本仓库是 **kore** 的 `Python 运行时插件`（id `runtime-python`），独立仓库、独立版本线、独立 CI（少触发：push main / tag / PR / 手动）。内核与协议见 [kore](https://github.com/kore-pl/kore)。

- **许可**：MIT（inbound = outbound，不要求 CLA）；提交用 `git commit -s` 签 DCO。
- **贡献**：[CONTRIBUTING.md](CONTRIBUTING.md)（构建、门禁、发版、边界）。
- **安全**：[SECURITY.md](SECURITY.md)——漏洞不要开公开 issue。
- **行为准则**：[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)。
- **兼容性**：`protocol: kore.plugin.v1` · `requires_kore: >=0.1.0 <0.2.0` · `lifecycle: lazy` · 深度等级 `production`。
- **验证方式**：本地 `make check` 优先（CI 只在 push main / tag / PR / 手动跑；发布前 `make dist` 连跑两次核对 sha 一致）。
- **发布产物**：`make dist` → `dist/runtime-python-0.2.3.tar.gz` + `.sha256`（位级可复现，连跑两次 sha 相同）。
- **相关仓库**：[索引 kore-plugins](https://github.com/kore-pl/kore-plugins) · [协议文档](https://github.com/kore-pl/kore/blob/main/docs/PLUGIN-PROTOCOL.md) · [SDK 指南](https://github.com/kore-pl/kore/blob/main/docs/PLUGIN-SDK.md)

<!-- oss:end -->
