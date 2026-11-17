# 安装为常驻服务

`reeve serve` 默认只在前台运行，关闭终端就退出。`reeve service` 把它装成**用户级**常驻服务：
macOS 用 launchd，Linux 用 systemd `--user`。只有这两个平台有实现，Windows 及其它系统会直接
报「当前平台不支持安装服务」。

所有操作都以**当前用户**身份进行，不需要 `sudo`，也不会创建系统级服务。

## 前置条件

- 一个构建好的 `reeve` 可执行文件，且路径稳定。服务定义里写死了这个绝对路径；把二进制从
  临时目录移走或删掉，服务下次启动就会失败，需要重新执行 `install`。
- `install` 会校验绝对路径：`--exec` 和数据目录传相对路径会直接报错，不会被悄悄转成绝对路径；
  `Name`/`ExecPath`/`DataDir` 里含换行或回车也会被拒绝（这类输入会破坏 plist/unit 的单行结构）。
  日志路径不是 CLI 参数，固定为 `<数据目录>/logs/reeve.log`，无法单独指定。
- 真正 `install` 时会 `stat` 一次 `ExecPath`：文件不存在或没有可执行位会直接报错，且不会创建
  目录或写服务定义。`--dry-run` 不检查这一项，它只渲染内容。
- 数据目录默认是 `$REEVE_HOME`，没设置时是 `~/.reeve`；`REEVE_HOME` 是相对路径时 `install`
  会拒绝（`status`/`uninstall` 不校验这一项，它们只按服务名找 plist/unit）。`install` 会顺带
  建好数据目录和日志目录。
- macOS 需要 launchd（`launchctl` 支持 `bootstrap`/`bootout` 子命令）。
- Linux 需要可用的 systemd 用户实例（`systemctl --user` 能正常工作）。纯容器、无 systemd 的
  环境不支持。

## install

```bash
reeve service install                    # 用当前进程的可执行文件
reeve service install --exec /usr/local/bin/reeve
reeve service install --dry-run          # 只打印将要写入的文件路径与内容，不落地
```

`--dry-run` 把路径写到 stderr、服务定义写到 stdout；`--exec` 和 `--dry-run` 只对 `install`
有效，带给 `status`/`uninstall` 会报错。

`install` 对**文件写入**是幂等的：定义内容没变就不会重写文件。但加载行为两个平台不同：

- **macOS**：无论内容有没有变，都会执行 `bootout` → `enable` → `bootstrap`，正在运行的服务
  会被重启（短暂中断）。
- **Linux**：无论内容有没有变，都会执行 `daemon-reload` 和 `enable --now`（对已运行的 unit
  不会重启）；只有内容变化**且**服务当前是 `active` 时才补一次 `try-restart`。所以内容没变时
  不会打断正在运行的进程。

### macOS（launchd）

定义写入 `~/Library/LaunchAgents/reeve.plist`，Label 为 `dev.reeve.reeve`，加载目标是
`gui/<uid>`。关键内容：

- `ProgramArguments` = `<exec> serve`
- `WorkingDirectory` = 数据目录，`EnvironmentVariables` 里同时写入 `REEVE_HOME=<数据目录>`
- `RunAtLoad` = true
- `KeepAlive` = `{ SuccessfulExit: false }`
- `StandardOutPath` / `StandardErrorPath` 都指向 `<数据目录>/logs/reeve.log`
- `PATH` 固定为 `/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin`（launchd 默认 PATH 不含
  Homebrew，不加这条外部执行器会找不到）

实际执行的命令依次是 `launchctl bootout` → `launchctl enable` → `launchctl bootstrap`。
先 `bootout` 再 `bootstrap` 是为了让更新后的定义立即生效；`enable` 必须早于 `bootstrap`，
否则存在 disabled override 时 `bootstrap` 会直接失败。`install` 的输出会打印这三条命令。

### Linux（systemd --user）

定义写入 `${XDG_CONFIG_HOME:-~/.config}/systemd/user/reeve.service`。关键内容：

- `Type=simple`、`ExecStart=<exec> serve`
- `WorkingDirectory` = 数据目录，`Environment=REEVE_HOME=<数据目录>`
- `Restart=on-failure`、`RestartSec=3`
- `StandardOutput=append:<数据目录>/logs/reeve.log`，`StandardError` 同样 append 到这个文件
- `WantedBy=default.target`
- `PATH` 固定为 `/usr/local/bin:/usr/bin:/bin`

实际执行的命令依次是 `systemctl --user daemon-reload` → `systemctl --user enable --now
reeve.service`。如果本次 unit 内容有变化、且服务当前是 `active`，再补一次
`systemctl --user try-restart reeve.service`。

两个平台执行外部命令时都先 `exec.LookPath` 解析，并要求结果是绝对路径；解析不到或结果不是
绝对路径会直接报错，不会交给 shell/PATH 去猜。安装输出里打印的也是解析后的绝对路径。

### 监听地址：服务只监听 127.0.0.1

生成的 plist/unit 只有 `ExecStart`/`ProgramArguments` 里的 `serve` 子命令，**不传 `-addr`**，
环境变量也只设 `REEVE_HOME` 和 `PATH`（外加包内 `Config.ExtraEnv`，但 CLI 没有入口，见文末）。
因此安装后的服务使用默认监听地址 `127.0.0.1:18800`，只有本机能访问。

要让手机或局域网内其它设备直连，有两条路：

- 手工编辑已生成的服务定义，追加环境变量 `REEVE_ADDR=0.0.0.0:18800`（launchd 的
  `EnvironmentVariables` 字典或 systemd 的 `Environment=` 行），然后重新 `install`/重启服务。
  注意：**再执行一次 `reeve service install` 会用生成的内容覆盖手改**，改完不要再 install，
  或者改完只重启服务（macOS `launchctl kickstart -k gui/$(id -u)/dev.reeve.reeve`，Linux
  `systemctl --user restart reeve.service`）。
- 不使用服务管理器，手工前台运行 `reeve serve -addr 0.0.0.0:18800`（或
  `REEVE_ADDR=0.0.0.0:18800 reeve serve`）。

监听非回环地址没有 TLS，只适合可信局域网；跨网络请用 Tailscale 或 SSH 隧道（详见 README）。

## 开机自启、KeepAlive 与重启语义

- **launchd**：`RunAtLoad` 让服务在登录（加载 domain）时立刻启动。`KeepAlive` 只写了
  `SuccessfulExit: false`，含义是——进程以非零状态退出（崩溃、被杀、启动失败）时由 launchd
  重新拉起；进程**正常退出（退出码 0）则不再重启**。所以「手动停掉」与「崩溃」的结果不同。
- **systemd**：`enable` 让 unit 随用户实例启动（登录时），`Restart=on-failure` 只在非零退出时
  重启，间隔 3 秒；正常退出不重启。注意用户级 unit 默认随登录会话结束而停止，要在注销后继续
  运行需另行 `loginctl enable-linger <user>`——`reeve service install` **不会**替你打开 linger。
- **更新配置/二进制**：macOS 每次 `install` 都 `bootout` + `bootstrap`，即使定义没变，服务也会
  被重启（短暂中断）；systemd 只在 unit 内容变化且服务正在运行时才 `try-restart`，内容没变就
  不会重启。`try-restart` 的语义是「**正在运行才重启，已停止则什么都不做**」，不会把停止的
  服务拉起来。

## status

```bash
reeve service status
# 已安装=true 已加载=true 路径=/Users/me/Library/LaunchAgents/reeve.plist
```

- `已安装`：服务定义文件是否存在。
- `已加载`：macOS 看 `launchctl print gui/<uid>/dev.reeve.reeve` 是否成功；Linux 看
  `systemctl --user is-active` 是否返回 `active`。
- `Detail`：launchd 成功时为 `loaded`，失败时是 `launchctl` 的输出；systemd 是 `is-active`
  的输出（如 `inactive`、`failed`）。

`status` 只反映服务管理器视角，**不会**去请求 `/api/health`，所以「已加载」不等于 HTTP 已经
可以访问；要看进程是否真的在服务，仍应请求 `http://127.0.0.1:18800/api/health`。

## uninstall

```bash
reeve service uninstall
```

- macOS：`launchctl bootout` 后删除 plist。
- Linux：`systemctl --user disable --now` 后删除 unit 文件，再 `daemon-reload`。
- 没安装时返回 `Changed=false`，退出码 0，不报错（可重复执行）。
- 如果停止服务这一步不是「本来就没加载」，其余失败会直接返回错误，**不删文件**，避免留下一个
  管不动的服务。

## 查看日志

服务进程的 stdout/stderr 都写进同一个文件，默认：

```bash
tail -f ~/.reeve/logs/reeve.log
```

如果安装时设置了 `REEVE_HOME`，路径是 `$REEVE_HOME/logs/reeve.log`。

辅助命令：

```bash
# macOS：查看 launchd 视角的进程状态、最近退出码
launchctl print gui/$(id -u)/dev.reeve.reeve

# Linux：unit 状态与生命周期日志（stdout/stderr 被重定向到文件，不在 journal 里）
systemctl --user status reeve.service
journalctl --user -u reeve.service
```

## 数据目录

数据目录（默认 `~/.reeve`，可用 `REEVE_HOME` 覆盖）同时是服务的工作目录和 `REEVE_HOME`。
`serve` 与 `service install` 都会保证它的权限是 **0700**：已存在的目录也会被 `chmod` 收紧，
两者先跑哪个结果都一样；日志子目录 `logs/` 保持 0755。
里面大致有（不同配置下未必全部存在）：

- 凭据与密钥：`token`（访问 token，0600）、`master.key`（主密钥，0600，首次 `serve` 时自动
  生成）、`secrets.json`（用主密钥加密的密钥）。
- 配置与状态：`bots.json`、`env`（实例级环境变量）、`executors.json`、`channels.json`、
  `agent-connection.json`、`schedules.json`、`stream.jsonl`（对话/事件历史）、`memory.md`、
  `shared-memory.md`。
- 目录：`skills/`（自定义技能）、`tools/`（执行器工具）、`checkpoints/`（任务检查点）、
  `logs/`（`reeve.log`）、`workspace/`（主 Bot 工作区）、`workspace-bots/`（各 Bot 工作区）、
  `bots/<id>/`（子 Bot 数据目录，含各自的 `env.json`）。

`install` 只负责建目录和加载服务，不会生成 token——token 由第一次 `serve` 启动时创建。

## 卸载干净

`reeve service uninstall` 只停止服务并删除 plist/unit 文件，**数据目录原样保留**（对话历史、
密钥、工作区都还在）。确认不再需要后，先卸载服务再删目录：

```bash
reeve service uninstall
rm -rf "${REEVE_HOME:-$HOME/.reeve}"
```

用 `${REEVE_HOME:-$HOME/.reeve}` 而不是裸 `$REEVE_HOME`，避免变量未设置时删错路径。

注意：这会**永久删除** `master.key` 与 `secrets.json`（主密钥没了，已保存的密钥无法再解密）、
访问 token、全部对话/事件历史（`stream.jsonl`）、`checkpoints/`，以及两个工作区里的文件；
不可恢复。删之前先备份需要的部分。如果主密钥是通过 `REEVE_MASTER_KEY` 环境变量提供的，它不在
这个目录里，但 `secrets.json` 仍是加密数据，删掉同样丢失。

## 已知边界

- **「未加载」的匹配偏宽**：判断外部命令失败是否只是「服务本来就没加载」时，macOS 匹配
  `No such process`、`Could not find service`、`not loaded`、`not found`、`Input/output error`、
  `Boot-out failed: 5`；Linux 匹配 `not loaded`、`not found`、`does not exist`、`No such file`。
  两边都同时检查命令输出和 `err.Error()`，且是**子串匹配**。这是为了兼容 macOS 对未加载服务
  执行 `bootout` 时的 `Input/output error` 文案，但副作用是：一次**其它原因**的停止/卸载失败
  只要输出里恰好带了这些字样，也会被当成「本来就没加载」而忽略。遇到可疑的卸载/安装结果，用
  `launchctl print` / `systemctl --user status` 再确认一次。
- **Windows 不支持**：`internal/service` 只有 darwin 与 linux 两份实现，其它平台
  `install`/`uninstall`/`status` 都返回 `ErrUnsupported`，CLI 会提示只支持 macOS launchd 和
  Linux systemd --user。
- **service 的 PATH 是固定的**：装在这些目录之外的 CLI（例如 nvm 安装的 node、用户目录下的
  脚本）在服务里可能找不到。包内的 `service.Config.ExtraEnv` 支持追加 `KEY=VALUE`，并且会校验
  key 只能是 `[A-Za-z_][A-Za-z0-9_]*`；但 `reeve service` **没有暴露这个参数的 CLI 入口**，
  所以实际安装出来的定义只有 `REEVE_HOME` 与 `PATH`。需要额外变量时只能手改 plist/unit（见
  「监听地址」一节的覆盖注意事项），或绕过 `service` 自己写 unit/plist。
