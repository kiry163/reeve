# Changelog

本项目的变更记录，格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

## [0.1.0] - 2026-11-16

首个公开版本：单二进制、单端口的本机助理编排器，网页与 Android 共用一套 REST + SSE 协议。

### 新增

- **事件溯源存储**：每个 bot 一个追加写的 `stream.jsonl`，单调 `seq`，SSE 按 `Last-Event-ID` 补发；
  消息、任务、审批与卡片状态启动时由事件重放重建，定时计划与 bot 级环境变量分别由
  `schedules.json` 与 `env` 文件恢复。临时事件（流式文字、输入中）只推送不落盘。
- **聊天层与监工**：聊天层只调度与汇报，没有执行权；监工负责队列、并发槽、优先级、暂停/继续、
  卡住检测与检查点，任务结束时叫醒聊天层汇报一次。
- **执行器**：内置 agent（读写工具、`shell`（`bash -c`，Windows 无 bash 时退回 PowerShell）、环境变量、技能）+ 外部 CLI `codex` / `pi` / `omp`，
  含可用性探测、派活前白名单校验、启动时丢弃不认识的执行器类型并记事件。
- **审批**：规则 → 策略 → 卡片三段；默认策略 `untrusted`（只读放行，写/执行弹卡）；
  批准绑定单次调用，同一任务内相同参数的调用可复用，也可用 `/approve` 处理待批项。
- **三层环境变量**：实例级、bot 级、调用级（`shell` 的 `env` 参数）；模型可读（`list_env`），
  写入按来源记审计事件且不记值。
- **密钥库**：AES-256-GCM 加密，模型只见占位符，事件与输出统一脱敏，模型无法读取明文。
- **斜线命令**：服务端解析、不进模型上下文——`/help` `/stop` `/clear` `/model` `/agent` `/env`
  `/skills` `/context` `/tasks` `/approve`。
- **技能**：扫描本地 `SKILL.md`，索引进系统提示，正文按需读取。
- **定时计划**：每个 bot 独立的计划（单次 / 每天 / 每周），到点派活。
- **记忆**：按 bot 存储的长期记忆，读取进入上下文。
- **网页控制台**：React + Vite，构建产物嵌入二进制；聊天、任务面板、审批卡片、环境变量、
  执行器、技能、渠道、计划、bot 编辑。
- **Android 客户端**（`dev.reeve.app`，Kotlin + Compose，`minSdk 26`）：配对（扫码/粘贴/手填/深链）、
  聊天与任务渲染、命令面板、环境变量/执行器/技能/渠道设置、前台保活与本地通知。深链来源必须
  勾选「我已核对地址」；通知点击经不导出的路由 Activity，锁屏不显示摘要。
- **鉴权**：Bearer token + Cookie 会话，回环免认证，限速；`reeve token rotate` 立即失效所有已登录设备。
- **常驻服务**：`reeve service install|uninstall|status`，生成 launchd / systemd `--user` 定义，
  幂等、原子写入、数据目录 `0700`。
- **发布流程**：`scripts/release.sh` 交叉编译 darwin/linux × amd64/arm64、注入版本、生成
  `SHA256SUMS`、可选签名 APK；`.github/workflows/release.yml` 分 build / publish 两个 job，
  publish 绑定 `release` 环境（在仓库设置里配置 Required reviewers 才会真正卡住发布）。
- **文档**：设计文档、Android 契约与手册、服务安装、发布说明、端到端冒烟记录。

### 已知限制

- 没有沙箱；执行器以当前用户身份运行，安全完全依赖审批。
- 没有 TLS，只适合可信局域网。
- 单用户，无多租户、无权限隔离，不可直接暴露到公网。
- 服务安装只支持 macOS 与 Linux；Windows 只能前台运行。
- Android 无系统推送、无法自更新；审批通知在其他端处理后不会自动撤销。
- 不读取旧版本数据。

[0.1.0]: https://github.com/kiry163/reeve/releases/tag/v0.1.0
