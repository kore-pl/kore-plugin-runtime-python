# Changelog

## [0.4.0] - 2026-11-25

### Added

- **进程守护（极简 pm2 同款能力，不做编排）**：`guard.add` / `guard.list` / `guard.start` /
  `guard.stop` / `guard.restart` / `guard.remove` / `guard.logs`。启动用 `<python> <script> <args...>`
  拉起脱离会话（setsid）的子进程，stdout/stderr 追加到 `<state>/guard/<name>.log`；
  停止先给进程组 SIGTERM、5s 不退升级 SIGKILL；状态含 running / stopped / crashed、PID、运行时长、RSS、
  日志大小与尾部 500 行。没有 supervisor 常驻进程（G-05 / G-19），`guard.start` 幂等、零参数 = 全部启动，
  可配合 cron 插件做「挂了自动起」。
- **默认版本切换**：`use {version?}` 钉住默认解释器（写 `<state>/default-version.json`，不改宿主 PATH），
  零参数 = 清除；`versions` 新增 `pinned_version` / `pinned_path`。

### Changed

- 插件级能力增加 `proc:write`（守护启停真实进程；方法级能力只能收窄不能跨域放大）；
  `method_capabilities` 9 条 → 17 条。

### Fixed

- 僵尸进程误判：子进程退出后由阻塞 `Wait` 的回收 goroutine 回收并落盘退出码，`Probe` 把 `Z` / `X`
  判为不在运行，停止不再出现「进程仍在运行」的假失败。

## [0.3.2] - 2026-11-24

### 元数据

- **`depth_level` 由 `production` 回落到 `basic`**（`info.json`，依据 `PLUGIN-CATALOG.md` §6.1 升级判据）：
  升 `production` 需**两条都满足**——①边界用例覆盖（异常输入 / 并发 / 权限不足 / 资源耗尽
  各 ≥1 例并进 CI）②故障注入通过。本仓 `tests/fault/` 四类故障注入真实 PASS（②达成），
  但 `tests/boundary/` 不存在、`grep -rn "t.Parallel" tests/` 0 命中，①无任何证据 →
  原 `production` 属单维度达标的**过度声明**，索引的 `basic` 才是正确值。
- **不是降级缺陷**：零代码变更，方法 / 能力 / 故障矩阵一字未改；边界用例与高级配置按
  `PLUGIN-CATALOG` §6.1 延到 v1.1。
- 版本 bump 原因：`info.json` 在 dist 包体内，改元数据即改变包 sha，必须 bump（RELEASE §1）。

## [0.3.1] - 2026-11-23

### 测试

- **故障注入补齐 4 个新方法**：`venv.create` / `venv.list` / `pip.install` / `pip.list`
  全部纳入 `tests/fault/` 矩阵（kill / timeout / disk / cgroup 四类各跑一遍），
  断言 `E_UPSTREAM` / `E_INTERNAL` / `E_UNSUPPORTED` 语义正确、不 panic；
  故障矩阵覆盖方法数 5 → 9。
- **pip 子进程被杀后可重试**：新增 `TestPipKillThenRetry`——kill 注入后 venv 目录、
  `bin/pip` 与已有文件保持完好，同一 venv 立即重试成功，且不残留临时目录。
- 无生产代码变更；仅版本号 + 测试 + CHANGELOG。

## [0.3.0] - 2026-11-23

### 新增

- **`runtime-python.venv.create`（admin）**：在插件状态目录建 venv（默认
  `<KORE_PYTHON_ROOT>/venvs/venv`），可选 `python_version` 与建完即装 `packages`；
  显式 `path` 必须是绝对路径且落在允许根内，越界 `E_INVALID` + `detail.hint`；
  已存在 `created=false` 不覆盖；venv 目录 0700；带 packages 时 `network_used=true`。
- **`runtime-python.pip.install`（admin）**：在指定 venv 内装依赖，固定 argv
  `bin/pip install --no-input --disable-pip-version-check`；包名白名单
  `[A-Za-z0-9][A-Za-z0-9._-]*` + 可选 `==version`，拒绝空格/分号/`--index-url`
  等选项注入且不触达执行面；120s 超时、输出首尾各 32 KiB + `truncated`；
  `network_used` 恒为 true（pip 默认连 PyPI）。
- **`runtime-python.venv.list` / `runtime-python.pip.list`（read）**：列出 venv 与已装包。

### 安全

- 不写系统 site-packages：pip 只经目标 venv 的 `bin/pip` 执行；venv 目标与解析后的
  真实路径都必须在允许根内（挡符号链接逃逸）。

## [0.2.4] - 2026-11-23

### Changed

- 测试解耦版本号：`main_test.go` 不再硬编码发布版本字面量，改为断言 `version` 符合
  MAJOR.MINOR.PATCH 且内嵌清单、`info.json`、`manifest.json` 三处同步（Gitea dns#1 技术债），
  此后 bump 无需改测试；本次 bump 仅版本号变化，方法契约与行为不变。

## [0.2.3] - 2026-11-23

### Changed
- 提升为 `production` 深度等级（故障注入测试已覆盖）

## 0.2.2 (2026-11-23)

- feat: 方法级能力声明（§3.5 #12）——`runtime-python.versions` / `info` / `projects` / `doctor` / `paths` 全部 `system:read`，
  只读方法不再按插件级 `system:admin` 计入写审计；插件级能力保持不变，作为未声明方法的 fail-closed 回退
- feat: `runtime-python.versions` 增强元信息——每项新增 `bin_dir`（安装目录）、`package_manager`（固定 `pip`）、
  `package_manager_path`（同目录 pip3/pip 优先、其次 PATH，**只做文件查找不执行命令**）
- feat: 新增 `runtime-python.paths`——返回 `path_env`（PATH 原始顺序）、`executables`（python3/python 解析结果）、
  `default`（which python3 解析链 + 版本 + 实现 + 包管理器）与 `search_order`（按探测优先级列出的真实来源），
  用于排查「为什么用到了这个解释器」；未检测到 Python 时 `default.found=false` + 固定 hint，不报错
- test: 新增 capability 双向校验（声明 vs 分发表 vs 内核键格式/值域）、增强字段与 `paths` 单测、
  二进制最小 PATH 链路补 `paths` 断言；故障注入扩到五个方法
- docs: README 补 5 方法契约、增强字段与 `paths` 口径、方法级能力说明
- note: **进程守护 / 扩展管理本轮不做**——守护进程要写 systemd unit、装包要写全局 site-packages，
  都是写操作且需要 root，必须走 `root_actions` + 不可逆确认，另立任务实现；本轮只交付只读增强
- build: 版本 0.2.2（只加元数据 + 只读增强 → patch），`info.json` / `manifest.json` 同步


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

## [v0.4.1] - 2026-11-25

### Added

- 补齐开源工程标准化：新增简体中文 / English / Español / Français / Deutsch / Русский / Português 七语言文档（`locale/` 与 `docs/DETAIL*`）、安全与依赖更新配置（dependabot / CODEOWNERS / SECURITY 译文）、贡献指南的分支模型说明，以及中文 CHANGELOG 语义化版本条目。
- 新增本地 `develop` 分支约定与 SSH 签名 tag 约定（历史 tag 不动）。

### Changed

- README 增加七语言切换行与 Star 支持提示；对应版本标签 `v0.4.1` 仅作为工程标准化发布标识，`info.json` / `manifest.json` 的运行时版本与 `dist` 产物保持不变，避免发布包 sha 漂移。
