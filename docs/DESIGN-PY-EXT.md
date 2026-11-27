> 状态: proposal（doc-first，待 lead / 用户审阅） · 归属: pyext-design · 更新: 2026-11-28 · 里程碑: R42
> 关联: task-289（本轮方案）/ task-288（只读核实，缺口属实）· 插件版本 v0.4.2 → 方案目标 v0.5.0
> 写域: 本仓 `docs/DESIGN-PY-EXT.md`（新）；不碰代码、不落实现、不联网业务、不动运行实例 / kore 仓

# DESIGN：Python 运行时扩展管理 `ext.*`（doc-first 方案）

## 0. 结论先行

task-288 三侧实证确认：`runtime-python` v0.4.2 的 17 个方法里没有 `ext.install/list/remove`（[info.json](../info.json) 与 [internal/pyop/call.go](../internal/pyop/call.go) 的 `businessVerb` 均无 ext）；[RELEASE-EVIDENCE.md](../RELEASE-EVIDENCE.md) §6 记载当年「装包/装扩展要写全局目录 → 需 root」属**历史有意 deferred**，而 Node / PHP 已用「隔离安装」把这条前提消解（`runtime-node` 0.4.1 / `runtime-php` 0.4.0 的 ext 三方法已交付）。这是 backlog §5 最后一个明确标「仍缺」的功能点。按 lead 裁决补做，属**产品内 runtime 三件套对称性补齐**，竞品（1Panel / 宝塔）侧仅为常识级背景，不作原文级对标压力。

三项核心裁决：

1. **增量 = 只加 `ext.install/list/remove` 三方法**，照 Node/PHP 已验收范本，复用本仓 venv.go 的既有护栏；不重建第二套包管理体系，与 venv/pip 并存且边界清晰（§1）。
2. **安装目标固定 `<state>/global/py_ext`**，`python3 -m pip install --target …` 固定 argv；**移除走隔离目录内的受控文件删除，不用 `pip uninstall --target`**——本机实测 pip 25.1.1 的 `uninstall` 无 `--target`/`--root` 选项（§2.2，关键 Python 特化差异）。
3. **不触 G-02 / G-10 / G-31，不新增 ADR，不新增 root_action**：所有写入都在 `<state>` 内，pip 是用户显式动作（同 npm / pecl 先例，ADR-0018 插件永不持 root、不新增 rootpolicy 形状表）。

---

## 1. 现状与增量边界（覆盖①）

### 1.1 现状事实（代码实证）

| 面 | 事实（file:line） |
| --- | --- |
| 方法面 | [call.go:40-75](../internal/pyop/call.go) 分发 17 动词，`businessVerb`（:96-106）白名单同步；[info.json:13-31](../info.json) 17 条 `method_capabilities`；[capability_test.go:31-49](../internal/pyop/capability_test.go) `expectedCapabilities` 期望表双向守护。 |
| venv/pip 护栏 | [venv.go](../internal/pyop/venv.go) 已有：`packageSpecPattern`（:33，`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}(==…)?$`）、`VenvCommandTimeout=120s`（:24）、`MaxVenvPackages=50`（:26）、`MaxPipOutputBytes=64KiB` + `truncatePipOutput`（:28,:374-381）、`withinAny`+`allowedRoots`（[path.go:100-117](../internal/pyop/path.go)）、`ensureRealWithinRoots`（:303-329，最近存在祖先 `EvalSymlinks` 挡软链逃逸）、`resolveVenvTarget/resolveExistingVenv`（:241-300）、`runVenvCommand`（:481-499，固定 argv + context 超时 → `ErrVenvTimeout`）。 |
| 假执行面 | venv.go 走 `VenvManager` 接口注入假 manager（:56-62,:131-136）；Node/PHP ext.go 额外用 `extCommandRunner` 函数字段捕获固定 argv（`nodeop/ext.go:271-284`、`phpop/ext.go:276-289`）。本方案两套都用（§3.6）。 |
| 历史 deferred | RELEASE-EVIDENCE.md:104-108：当年判断「装包写全局目录需 root + root_actions 一次性确认 + 审计」是独立工作量；隔离 `--target` 让全局写消失，这条前提不再成立。 |

### 1.2 增量边界：三方法与 venv/pip 并存

新增 `runtime-python.ext.install` / `ext.list` / `ext.remove`，落在 `pyop` 包新文件 `internal/pyop/ext.go`（照 Node/PHP 同构），`call.go` 加三分支 + `businessVerb` 加三动词，`info.json` 方法级能力 17→20，`capability_test.go` 期望表同步。

**并存边界（必须写清，避免两套语义打架）**：

- `venv.create` / `venv.list` / `pip.install` / `pip.list` **保持原样**，操作对象是 `<state>/venvs/<name>` 内的完整虚拟环境（`bin/pip`），供应用按环境依赖隔离使用。
- `ext.*` 操作的是**另一块固定隔离前缀** `<state>/global/py_ext`，用 pip `--target` 把包平铺进去，供「只想给运行时补几个全局可见工具包」的场景；**不建 venv、不改 `pyvenv.cfg`、不写 `<state>/venvs`**。
- 两者共用同一套包白名单正则与输出截断工具（复用 venv.go 常量与函数，不复制常量值），但**前缀解析各自独立**：`ext.*` 用新的 `resolveExtPrefix`（不接受调用方传路径，`resolveVenvTarget` 仍接受 venv 路径参数）。
- `ext.*` 与外部项目目录（`/var/www`、`/opt`、`/home` 白名单根）**无关**：不接 `root` 参数，不写项目目录。

---

## 2. Python 特化 argv 与目录语义（覆盖②）

隔离前缀 `prefix = <state>/global/py_ext`（`DefaultGlobalSubdir="global"` + `pyExtSubdir="py_ext"`，对齐 PHP 的子目录做法）。

### 2.1 install：`python3 -m pip install --target <prefix>`（固定 argv，无 shell）

```
python3 -m pip install --target <state>/global/py_ext --no-input --disable-pip-version-check <包名...>
```

- `exec.CommandContext(ctx, "python3", args...)`，argv 数组拼接、无 `sh -c`，包名经白名单后才进 argv（§3.2）。
- `--target` 让包落进隔离前缀；**不写宿主 site-packages、不改 PATH、不设 PYTHONPATH**（宿主环境零侵入）。pip 自身若触发 PEP 668 externally-managed 提示，只影响 `<target>` 内的写入，不回退到系统目录（本机 `--no-index --target` 探测已确认只写目标目录，失败时不落盘）。
- `python3` 固定解释器名，不接 `python_version` 参数（避免 `python3.11` 之类拼接面）；如需指定版本属 venv 场景，不是 ext 场景。
- `NetworkUsed=true`、`DurationMS` 计时、`Hint` 说明「只装进插件隔离目录，不碰宿主 site-packages」。

### 2.2 remove：**受控文件删除**，不用 `pip uninstall --target`（关键差异）

**实测证据**（本机 pip 25.1.1，`python3 -m pip uninstall --help`）：`uninstall` 仅有 `-r/--requirement`、`-y/--yes`、`--root-user-action`、`--break-system-packages` 与通用选项，**没有 `--target` / `--root` / `--path`**；`python3 -m pip uninstall --target /tmp/foo …` 直接打回 usage。Node 的 `npm uninstall --prefix` 与 PHP 的 `pecl uninstall` 都没有这层限制，所以**不能照抄 uninstall argv**。

裁决：`ext.remove` 用纯 Go 标准库在隔离前缀内做**受控删除**，不起子进程：

1. 对每个白名单包名做 PEP 503 归一（`lower()` + `-`→`_`），得到候选目录名集合；只枚举 `<prefix>/py_ext/` 下**直接子项**。
2. 每个候选路径先 `filepath.Rel(prefix, candidate)` 确认未逃出前缀，再 `os.Lstat` + `realAncestor`/`EvalSymlinks` 确认符号链接解析后仍在 `<prefix>` 真实路径内；命中符号链接指向外部一律拒绝（`E_INVALID`），不跟随删除。
3. 删除该包目录及同名 `*.dist-info` / `*.egg-info` 元数据目录（`os.RemoveAll`，仅限通过第 1–2 步的候选）。
4. 结果 `Removed=[]string` + `Output` 记删除清单（不走截断也可以，但统一走 `truncateExtOutput` 保持一致）；`Hint` 说明「只移除隔离目录内扩展，宿主 site-packages 不受影响」。

**这不是安全门降级**：remove 无 argv、无子进程、无 shell，攻击面比 install 更小；「固定 argv」要素在 remove 上按「N/A（无 exec）」记录并给出理由（§3 清单）。代价是需自己处理包名归一，测试要覆盖（`requests` ↔ `requests-2.31.0.dist-info`）。

### 2.3 list：读隔离目录元数据（无子进程、无 pip 调用）

- 只扫 `<prefix>/py_ext/*` 下的 `*.dist-info/METADATA`（与 `*.egg-info/PKG-INFO` 兜底），解析 `Name:` / `Version:` 两个字段；解析失败用目录名兜底（剥 `-<version>` 尾）。
- 按 `Name` 排序、`Count` 计数，空目录返回空数组 + 可操作空态文案。与 Node 读 `package.json`、PHP 读 `ext.json` 同构，只是元数据文件名换成 Python 的 `dist-info`。
- 同 §2.2 的符号链接 containment 校验：只列真实落在前缀内的条目。

### 2.4 与 Node / PHP 的语义差异（诚实标注）

| 维度 | Node `ext.*` | PHP `ext.*` | Python `ext.*`（本方案） |
| --- | --- | --- | --- |
| 隔离产物 | `<state>/global/node_modules` | `<state>/global/php_ext` | `<state>/global/py_ext` |
| install argv | `npm install --prefix …` | `pecl install -d extension_dir=…` | `python3 -m pip install --target …` |
| remove argv | `npm uninstall --prefix …` | `pecl uninstall …` | **无 argv**（受控文件删除；pip uninstall 无 --target） |
| list 元数据 | `package.json` | `ext.json` | `*.dist-info/METADATA` |
| 宿主生效方式 | 需调用方自行把 prefix 的 node_modules 纳入 NODE_PATH | 需宿主 php.ini 加载扩展（本插件不改） | 需调用方自行把 py_ext 纳入 PYTHONPATH（本插件不改） |
| 版本后缀语法 | `==版本` | `-版本` | `==版本`（复用 venv.go 正则） |

共同点：都只写 `<state>` 内、都不碰宿主全局目录、都不新增 root_action。差异根源是三种包管理器对「重定向安装目标」的支持方式不同，Python 最特殊（uninstall 无重定向）。

---

## 3. 安全门六要素逐条对齐 Node / PHP（覆盖③）

| # | 要素 | Node / PHP 范本 | Python ext.* 落地 | 判据 |
| --- | --- | --- | --- | --- |
| 1 | 隔离状态目录 | `resolveExtPrefix` + `realAncestor` + `EvalSymlinks`，固定 `<state>/global`，**不接受调用方路径** | 同名同构函数（新 `ext.go`），固定 `<state>/global/py_ext`，**无任何 `prefix`/`path` 入参**；词法层拒 NUL / 反斜杠 / `..` / 相对 / 首尾空白，真实层拒软链逃逸 | 单测覆盖四种坏 root + `<global>` 软链指向外部 → `E_INVALID` |
| 2 | 包白名单 | `extPackageSpecPattern` 正则，先校验后拼 argv | 复用 venv.go `packageSpecPattern`（同一正则，挡 `-g`/空格/分号/引号/斜杠/前导 `-`/选项注入） | 单测覆盖 `--global`、`pkg; rm`、`../evil`、空列表、未知字段（`prefix`/`global`） |
| 3 | 固定 argv 无 shell | `exec.CommandContext` + `install/uninstall` 固定前缀 | install: `exec.CommandContext("python3", "-m", "pip", "install", "--target", prefix, …)`，无 `sh -c`；remove: 无 exec（§2.2，N/A 附理由） | `extCommandRunner` 捕获 argv：断言含 `--target` + prefix、包名末尾透传、无 `--user`/`--break-system-packages` |
| 4 | 120s 超时 → E_UPSTREAM | `ExtCommandTimeout=120s`，`ErrExtTimeout` → `upstream()` | 复用 `VenvCommandTimeout`（120s）+ 新增 `ErrExtTimeout`；`classify` 加 `errors.Is(err, ErrExtTimeout) → upstream("调用超时…")` | fake 注入 `ErrExtTimeout` → `Call` 返回 `E_UPSTREAM` |
| 5 | 64KiB 首尾截断 | `truncateExtOutput`，`MaxExtOutputBytes` | 复用 `truncatePipOutput` / `MaxPipOutputBytes`（64KiB，首尾各半 + 截断标记） | 超长输出截断且标记存在、长度下降 |
| 6 | fake executor 测试 | `extFake` + `realExtManager{runner:…}` + `TestExtRealManagerArgv` | `extFake`（实现 `ExtManager`）+ `realExtManager{runner: extCommandRunner}`，`TestExtRealManagerArgv` 断言 install/remove 全部路径 | 参数校验失败不触达执行面（`installCalls==0`） |

**capability 分级**（与 Node/PHP 完全一致，写方法 `system:admin`、读方法 `system:read`）：

| 方法 | capability | 依据 |
| --- | --- | --- |
| `runtime-python.ext.install` | `system:admin` | 写 `<state>` 隔离前缀 |
| `runtime-python.ext.remove` | `system:admin` | 删 `<state>` 隔离前缀 |
| `runtime-python.ext.list` | `system:read` | 只读目录元数据 |

三键进 [info.json](../info.json) `method_capabilities`（总 17→20）、[capability_test.go:31-49](../internal/pyop/capability_test.go) `expectedCapabilities`（双向一致性由既有用例守护）。插件级 `capabilities` 不变（`system:admin` + `proc:write`），方法级只收窄不跨域放大。

**错误映射**（`classify` 扩展，照 Node `call.go:152-159`）：`ErrExtTimeout` → `E_UPSTREAM`；路径/包名非法 → `E_INVALID`（含「请」引导词，过 G-33 A3）；`ErrExtPrefixMissing` 未装过扩展 → 空态而非报错；其余 → `E_INTERNAL`。

---

## 4. 安全边界与治理（覆盖④）

- **G-02 零新依赖**：只 import `pluginapi` + 标准库（`os/exec` / `path/filepath` / `regexp` / `encoding/json` 等 venv.go 已用者），无第三方包、无 `kore/internal/*`、无 `go.work`。
- **G-10 无联网 / 无遥测**：pip 安装是**用户显式触发**的下载动作，同 npm / pecl 先例（不属 G-10 禁止的「强制账号 / 隐藏上报 / 功能封锁」）；插件不主动出站、不 telemetry。按 ADR-0018，插件**永不获得 root**，`ext.*` 以服务用户身份只对 `<state>` 内可写目录操作，**不新增 root_action、不动 rootpolicy 形状表**——这正是历史 deferred 前提被隔离消解之处。
- **G-31 零侵入**：所有写入位于 `<state>/global/py_ext`，不触 ADR-0023 §3.5 豁免清单的 4 项之外任何路径；不写宿主 site-packages、不写项目目录、不写系统目录。
- **不需新 ADR**：方案无不可逆架构选择、无新特权模型、无预算数字变更；判断留痕由本文 + CHANGELOG + 分期判据承载（G-23 由本文承担，不新增 ADR 编号）。
- **G-33 A3 文案**：所有 `invalid()` 消息沿用 0.4.2 已清理口径（≥8 字且含「请/应/需/例/如」），新文案（包名非法、隔离目录越界、空 packages）须符合，避免新增违规把存量豁免转 FAIL。

---

## 5. 涟漪面（覆盖⑤）

### 5.1 本仓（runtime-python）

`info.json` + `manifest.json` 版本 0.4.2 → **0.5.0**（`feat` → minor）；`CHANGELOG.md` 加 `Added` 条（照 Node 0.4.0 / PHP 0.4.0 措辞）；七语言 `DETAIL*.md` 的方法清单补 `ext.*`（中文先落，其余六种同步）；`locale/*.json`（de/en/es/fr/pt/ru/zh）的 meta 描述更新；`make dist` 产 `dist/runtime-python-0.5.0.tar.gz` + `.sha256`（`info.json.size_bytes` 自指估算按实测回填，差 1 字节属正常抖动）。

### 5.2 索引仓（kore-plugins）

按 RELEASE.md §0「同 id@version 只允许一个 sha、改变 dist sha 必 bump」：`index/plugins.json` 的 runtime-python 条目版本→0.5.0、sha→新值、`size_bytes`、`download_url`；`index/versions.lock.json` runtime-python 数组**追加** 0.5.0 账本（旧 0.4.2 sha 保留不覆盖）；`presets/dev.json` 与 `presets/full.json` 里写死的 `0.4.2` pin 改 `0.5.0`（当前 `dev.json:19-22`、`full.json:69-72`）；`market/plugins.js` 用 `scripts/build-market.sh` 重新生成（禁止手改）；索引 tag 重签 + `make market-check` / `validate-index.sh` 一致。

### 5.3 主壳前端（kore 仓，本方案只写方案不落地）

`web/runtimes.js` 的 `EXT_CFG`（:17-28）加一条 `'runtime-python'` 配置：`pkgExample: 'requests==2.31.0 flask'`、`emptyPkg`/`badPkg` 用 Python 口吻、`meta` 写「装到插件隔离目录，不碰宿主 site-packages」、`suffix`/`fieldHint`/`fieldLabel`/`installNote` 同构。面板 `extPanel`（:590-638）、`loadExts`/`doExtInstall`/`doExtRemove`（:304-337）已是 `EXT_CFG` 驱动，加配置即可在 Python tab 复用，**不新增一级控件 / hub / CSS**；复用 `EXT_TIMEOUT=125000`（:15，后端 120s 硬超时 + 5s 余量）。`firstBadPkg` 的 `PKG_RE`（:215）与 venv.go 正则相同，无需改。

**七语言边界（诚实标注）**：主壳 web i18n 实际只有 **zh-CN / en-US 两套**（kore 仓 `web/i18n.js:6-9`，ADR-0021 中文为主+英文懒加载），不是七套；新用户可见英文串进 `web/i18n/en-US.js` 懒加载包（不占首屏），中文走 `tr()` 原地回退。所谓「七语言」指插件自身 `locale/*.json`（7 份 meta）与 `DETAIL*.md`（7 份），按 §5.1 同步。首屏预算 `make frontend` 维持 ≤300 KiB gzip（c13 基线 90924 B）。

### 5.4 G-28 双端证据 + 三问

P2 完成后在隔离实例（非 8787，独立 `--config` + state/run/log）产出 PC 1440×900 full-page 与手机 390×844 截图，存 `kore/.agents/verify/screenshots/ui-py-ext-{desktop,mobile}.png`，并写三问自评：①解决什么用户问题（不必敲 `pip install --user` 改宿主环境，填包名即可装进隔离目录，看得见装得上卸得掉）；②与宝塔/1Panel 差距（常识级：它们有 Python 项目/pip 包管理界面，本产品有意克制——包名白名单窄、无依赖树可视化、无镜像源 UI，属产品内对称补齐非原文级对标）；③可用还是占位（判「可用」，判据=双端截图 + API 断言 + 门禁绿）。

### 5.5 真机方案（P3）

本机 `python3`（pip 25.1.1）+ `--target` 隔离，**不碰宿主 site-packages**：起独立实例（独立端口，如 C13 用 18963 的先例），真实 `ext.list → ext.install → ext.remove` 断言；install 前后各抓一次 `python3 -c 'import site; print(site.getsitepackages())'` 对应目录的顶层 `find … -printf '%f|%T@|%s\n' | sort` 快照，sha256 相同 + `diff` 无输出才算宿主零变化；remove 后 `<state>/global/py_ext/<pkg>` 与 `*.dist-info` 均 `IsNotExist`。真机联网 `pip install`（真从 PyPI 下载）**不在本方案验证范围**（任务禁联网业务）；用 `--no-index` + 假执行面验证 argv/隔离面，真机只验证本机已有 pip 的隔离行为与宿主零变化。

---

## 6. 分期判据与验收（覆盖⑥）

| 期 | 范围 | 完成判据（可核） |
| --- | --- | --- |
| **P1 后端** | 新 `internal/pyop/ext.go`（≈380 行，照 Node 388 / PHP 400）+ `call.go` 三分支 + `businessVerb` + `info.json` + `capability_test.go` + `ext_test.go`；版本 bump 与 CHANGELOG | `make check`（gofmt → vet → `go test -timeout 200s ./...`）exit 0；六要素单测全绿（尤其 argv 捕获、软链逃逸、超时映射、截断、白名单、假执行面）；`classify` 的 `ErrExtTimeout → E_UPSTREAM` 有用例 |
| **P2 前端 + G-28** | kore `web/runtimes.js` 加 `EXT_CFG.runtime-python` + `web/i18n/en-US.js` 懒加载串；双端截图 + 三问自评 | `make frontend` 首屏 ≤300 KiB gzip、`node --check web/runtimes.js` exit 0、`usability-check` 0 FAIL；G-28 四图（视口/全页 × PC/手机）`identify` 宽高 + 三问原文齐备 |
| **P3 真机 + 发版** | 隔离实例真机断言 + 宿主零变化快照 + `make dist` + 三仓索引/版本/preset/market 同步 + 重签 + 精确 commit | 隔离断言 10/10 PASS（照 c13 范本）、宿主 site-packages before/after diff=0；`validate-index.sh` / `build-market.sh --check` exit 0；dist/索引/versions.lock/preset/market sha 逐字节一致；三仓 commit 精确路径 + 未 push |

**验收照 `c13-node-ext-verification.md` 六项范本**：①后端三方法+白名单+capability+零 `-g`/root_action（Python 侧为零 `--user`/宿主 site-packages 写）；②插件 `make check` + kore `make check` 21 段 + usability WARN 归因；③隔离断言 + 宿主全局零变化 before/after 快照 + 临时 scaffold 删净；④版本 bump 理由 + sha/索引一致；⑤G-28 双端截图实读 + 三问原文；⑥三仓 commit 精确路径 / 未 push / worktree 单一 / 他人 dirty 原样。

---

## 7. 诚实边界

1. **对标性质**：竞品在 Python 扩展/包管理上仅常识级背景，本文是**产品内 runtime 三件套对称补齐**，不是原文级竞品对标；不据此宣称「超越竞品」。
2. **Python vs Node/PHP 语义差异**：pip `--target` 是平铺目标目录、`uninstall` 无重定向；`remove` 用受控文件删除而非包管理器卸载，与 Node/PHP 的 argv 对称是**安全目标对称、实现路径不同**（§2.4）。Python 还需调用方自行把 py_ext 纳入 PYTHONPATH，本插件不改宿主环境。
3. **未验证项**：真机联网从 PyPI 下载未验证（禁联网业务）；P1/P2 用假执行面 + `--no-index` 验证机制，不宣称「真装过某个包」。docs-check 是 kore 仓门禁（本文件在插件仓不被它扫描），已在 kore 仓原样复跑 exit 0 并把存量 WARN 如实标注；本文件按同一契约自校（元信息/相对链接/行数/占位词）。
4. **成本与风险**：低风险，粗估 2–2.5 人日（P1≈1 / P2≈0.75 / P3≈0.75），主要风险在 remove 的包名归一与软链 containment 覆盖（已列 §2.2/§3 单测判据），不涉及新权限模型或跨仓架构变更。
