# Python 运行时插件（runtime-python）

## 它做什么

真实检测宿主已安装的 Python（CPython / PyPy，含 pyenv 与 conda 环境）、汇总解释器详情、扫描 Python 项目并做健康检查、建虚拟环境与装依赖；0.4.0 起补默认版本切换与单进程守护（启动 / 停止 / 重启 / 状态 / 日志尾部）。
不内置 python 二进制、不引入常驻中间件、不做容器编排；守护没有 supervisor 进程，被守护应用 setsid 脱离会话独立运行，状态全部落盘。

## 协议与兼容性

协议 `kore.plugin.v1`，`lifecycle=lazy`，`depth_level=basic`，`requires_kore >=0.1.0 <0.2.0`。
插件级能力 `system:admin` + `proc:write`；20 条方法级能力中只读探测 / 列表 / 日志为 `system:read`，venv / pip / 扩展安装移除 / 版本钉住 / 守护配置增删为 `system:admin`，守护启停为 `proc:write`。

## 方法

- `versions` / `info` / `projects` / `doctor` / `paths`：探测与详情、项目扫描（含 TOML 行级提取，不引第三方库）、健康检查与路径解析解释；未检测到 Python 给安装提示而非报错。
- `use {version?}`：钉住默认版本（零参数清除），不改宿主 PATH。
- `venv.create` / `venv.list` / `pip.install` / `pip.list`：建虚拟环境、列环境、按环境装依赖与列包。
- `ext.install` / `ext.list` / `ext.remove`（0.5.0 起）：隔离扩展管理，包平铺装进 `<state>/global/py_ext`，`python3 -m pip install --target` 固定 argv、不写宿主 site-packages；移除用隔离目录内受控文件删除（pip uninstall 无 `--target`），列表只读 `*.dist-info/METADATA`。与 venv/pip 并存但互不干扰。
- `guard.add` / `guard.list` / `guard.start` / `guard.stop` / `guard.restart` / `guard.remove` / `guard.logs`：单机单进程守护，启动幂等，停止先 SIGTERM 再按需 SIGKILL。

## 安装与验证

`make check` 为本地门禁（gofmt → go vet → go test -timeout 200s），`make dist` 产出位级可复现的 `dist/runtime-python-0.5.0.tar.gz`。
漏洞按 [SECURITY.md](SECURITY.md) 私下披露；MIT 许可；贡献见 [CONTRIBUTING.md](CONTRIBUTING.md)。