# Changelog

## 0.2.0 (2026-11-23)

- feat: `runtime-python.versions` 改为**真实检测**宿主 Python 安装（PATH 的 `python3` / `python`、
  `/usr/bin/python3*`、`/usr/local/bin/python3*`、`/opt/python*/bin/python3`、pyenv、conda/miniconda/anaconda），
  解析 `--version` 得到完整版本与 `implementation`（CPython / PyPy），按真实路径去重，
  `is_default` 由 `which python3` 决定；未检测到 Python 时返回 `count=0` + 固定 hint，不报错
- feat: 新增 `runtime-python.info`：真实 `--version`、`sys.executable`、`sys.prefix`、
  `pip --version`、site-packages（sysconfig purelib）、`pip list --format=json` 包数、venv 模块可用性；
  支持按完整版本（3.12.3）或主次版本（3.12）选择解释器
- feat: 新增 `runtime-python.projects`：扫描 root（深度 ≤3 / 条目 ≤200）下的 Python 项目，
  识别 `pyproject.toml` / `requirements.txt` / `setup.py` / `Pipfile`，返回
  name / python_requires / dependency_count / has_lock / has_venv / project_type；
  清单文件只读前 64 KiB，坏文件跳过并写 warnings
- feat: 新增 `runtime-python.doctor`：真实检查 Python / pip / venv / site-packages 可写 /
  virtualenv / poetry / pipenv / 多版本冲突（可选项目目录检查）
- refactor: 移除骨架方法 `runtime-python.venv.create` 与 `noopExecutor`（0.2.0 方法面为
  versions / info / projects / doctor；不真建 venv）
- refactor: **零第三方依赖**——标准库没有 TOML 解析器，`pyproject.toml` / `Pipfile` / `setup.py`
  一律行级正则提取（name / requires-python / 依赖计数），不引入任何解析库
- security: root 白名单（`/var/www`、`/opt`、`/home`、`<state>`）+ `ResolveExisting` + 符号链接二次归属校验；
  外部命令固定 argv + 5s 超时，解释器探测统一 `-I` 隔离模式；只读元数据、不执行项目脚本
- test: 真实 E2E（本机 Python/pip/site-packages/包数、`/tmp/kore-py-test` 三项目、二进制空 PATH 会话）；
  `tests/fault/` 扩到四类故障注入（kill / timeout / disk / cgroup）

## 0.1.1 (2026-11-23)

- refactor: 方法名统一为 runtime-python.<action>，对齐协议域校验
- build: 位级可复现打包（stage chmod + tar --mode 归一、size_bytes 收敛），二进制改嵌 manifest.json，消除同版本 sha 漂移

## 0.1.0 (2026-11-22)

- feat: 新建 runtime-python 插件骨架，复用 kore.plugin.v1 握手与会话循环
- feat: runtime-python.versions / runtime-python.venv.create 方法级参数校验与结构化结果，版本白名单 3.9-3.13，路径限制在配置根目录内，不真建 venv
- feat: 基础故障注入测试（kill -9 / 超时），验证方法级错误返回
- build: 确定性打包（--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner + gzip -9n）
