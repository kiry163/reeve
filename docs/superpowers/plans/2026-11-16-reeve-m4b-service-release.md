# Reeve M4b：服务安装与发布 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 补齐 M2 遗留的常驻服务安装（macOS launchd / Linux systemd user），并给 M4 一条可重复的发布路径（版本注入、跨平台二进制、校验和、APK 产物、GitHub Release 工作流）。

**Architecture:** 新增 `internal/service`：纯函数生成 plist/unit + 平台无关的 `Install/Uninstall/Status`（所有副作用通过 `fs`/命令注入，便于测试）。`cmd/reeve` 增加 `service` 子命令。发布靠 `scripts/release.sh`（本地可跑）+ `.github/workflows/release.yml`（tag 触发，调同一个脚本的产物布局）。

**Tech Stack:** Go 1.22（标准库）；shell（POSIX sh + `set -eu`）；GitHub Actions。

**Spec:** `docs/superpowers/specs/2026-11-14-reeve-design.md`（§2 本机常驻、§21 M2/M4 里程碑、§16.1 配对串）

## Global Constraints

- 零新增 Go 依赖；服务安装不得要求 root（launchd `LaunchAgents` / systemd `--user`）。
- 服务的 `ProgramArguments` 必须是**绝对路径**的可执行文件；`WorkingDirectory` 必须是数据目录；环境变量只传 `REEVE_HOME`（若与默认不同）与 `PATH`（保留 GUI 会话找不到 CLI 的常见问题）。
- 日志：stdout/stderr 落到 `<data>/logs/reeve.log`（追加），不写系统日志设施。
- 所有安装/卸载动作**幂等**：重复 install 覆盖同一份配置，uninstall 不存在时返回成功。
- 测试不得触碰真实 `~/Library/LaunchAgents` 或 `~/.config/systemd`：路径全部从 `Config` 注入，测试用 `t.TempDir()`。
- 发布脚本：`go build` 必须带 `-ldflags "-X main.version=<version>"`；产物含 `SHA256SUMS`；Android APK 作为可选产物（缺 SDK 时跳过而不是失败）。
- 版本号规则：`MAJOR.MINOR.PATCH`（与 Android `versionName` 同源，`v` 前缀在 tag 上，ldflags 值不带 `v`）。

---

### Task 1: `internal/service` — plist/unit 生成与生命周期

**Files:**
- Create: `internal/service/service.go`（`Config`、`Install/Uninstall/Status`、幂等与报错）
- Create: `internal/service/launchd.go`、`internal/service/systemd.go`（各平台的 `Render` + 路径 + 命令）
- Create: `internal/service/service_other.go`（其它平台：`ErrUnsupported`）
- Test: `internal/service/service_test.go`

**Interfaces:**
- Produces:
  - `service.Config{Name, ExecPath, DataDir, LogPath string; ExtraEnv []string}`
  - `service.Kind() string`（`launchd` / `systemd` / `unsupported`）
  - `service.Install(ctx, Config) (Result, error)`、`service.Uninstall(ctx, Config) (Result, error)`、`service.Status(ctx, Config) (State, error)`
  - `service.Result{Path string, Changed bool, Commands []string}`、`service.State{Installed, Loaded bool, Path, Detail string}`
  - launchd：`<home>/Library/LaunchAgents/<Name>.plist`，`launchctl bootstrap gui/<uid>` / `bootout gui/<uid>`
  - systemd：`<XDG_CONFIG_HOME>/systemd/user/<Name>.service`，`systemctl --user daemon-reload` + `enable --now` / `disable --now`

- [ ] **Step 1: 写失败测试（生成 + 幂等 + 不碰真实目录）**

```go
//go:build darwin

package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	home := t.TempDir()
	return Config{Name: "reeve", ExecPath: filepath.Join(home, "bin", "reeve"), DataDir: filepath.Join(home, "data"), LogPath: filepath.Join(home, "data", "logs", "reeve.log")}
}

func TestLaunchdPlistContainsAbsolutePathsAndKeepAlive(t *testing.T) {
	cfg := testConfig(t)
	out, err := renderLaunchd(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<key>Label</key>", "<string>" + cfg.ExecPath + "</string>", "<key>RunAtLoad</key>", "<key>KeepAlive</key>", cfg.LogPath, cfg.DataDir} {
		if !strings.Contains(out, want) {
			t.Fatalf("plist 缺少 %q\n%s", want, out)
		}
	}
	if strings.Contains(out, ">serve<") == false {
		t.Fatalf("ProgramArguments 必须包含 serve 子命令\n%s", out)
	}
}

func TestInstallIsIdempotentAndUsesConfigPaths(t *testing.T) {
	cfg := testConfig(t)
	runner := &fakeRunner{} // 记录命令，不真的调 launchctl
	first, err := install(context.Background(), cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed {
		t.Fatal("首次安装应 Changed=true")
	}
	second, err := install(context.Background(), cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed {
		t.Fatal("同样内容重复安装不应 Changed")
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatalf("plist 未写入：%v", err)
	}
	if len(runner.calls) == 0 || !strings.Contains(strings.Join(runner.calls, " "), "bootstrap") {
		t.Fatalf("未调用 launchctl bootstrap：%v", runner.calls)
	}
}

func TestUninstallMissingIsOK(t *testing.T) {
	cfg := testConfig(t)
	res, err := uninstall(context.Background(), cfg, &fakeRunner{})
	if err != nil {
		t.Fatalf("不存在的服务卸载应成功：%v", err)
	}
	if res.Changed {
		t.Fatal("未安装时 Changed 应为 false")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/service`
Expected: FAIL（`undefined: renderLaunchd` 等）

- [ ] **Step 3: 实现**

```go
// Package service 安装 reeve 为常驻用户服务：macOS 用 launchd，Linux 用 systemd --user。
// 生成与安装都接受 Config 里的全部路径，测试与调用方都不会碰真实家目录。
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var ErrUnsupported = errors.New("当前平台不支持安装服务（只支持 macOS 的 launchd 与 Linux 的 systemd --user）")

type Config struct {
	Name     string
	ExecPath string
	DataDir  string
	LogPath  string
}

// Result 描述一次安装/卸载做了什么。
type Result struct {
	Path     string
	Changed  bool
	Commands []string
}

type State struct {
	Installed bool
	Loaded    bool
	Path      string
	Detail    string
}

// runner 执行外部命令；测试替换掉，不真的碰 launchctl/systemctl。
type runner interface {
	run(ctx context.Context, name string, args ...string) (string, error)
}

type execRunner struct{}

func (execRunner) run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func Install(ctx context.Context, cfg Config) (Result, error) { return install(ctx, cfg, execRunner{}) }
func Uninstall(ctx context.Context, cfg Config) (Result, error) { return uninstall(ctx, cfg, execRunner{}) }
func Status(ctx context.Context, cfg Config) (State, error) { return status(ctx, cfg, execRunner{}) }

// writeIfChanged 幂等写入：内容相同就不动文件，返回是否变化。
func writeIfChanged(path, content string) (bool, error) {
	if b, err := os.ReadFile(path); err == nil && string(b) == content {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func validate(cfg Config) error {
	if cfg.Name == "" || cfg.ExecPath == "" || cfg.DataDir == "" {
		return fmt.Errorf("服务配置不完整：需要 Name/ExecPath/DataDir")
	}
	if !filepath.IsAbs(cfg.ExecPath) {
		return fmt.Errorf("可执行文件必须是绝对路径：%s", cfg.ExecPath)
	}
	if !filepath.IsAbs(cfg.DataDir) {
		return fmt.Errorf("数据目录必须是绝对路径：%s", cfg.DataDir)
	}
	return nil
}
```

`launchd.go`（`//go:build darwin`）：

```go
func plistPath(cfg Config) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", cfg.Name+".plist"), nil
}

func renderLaunchd(cfg Config) (string, error) {
	if err := validate(cfg); err != nil {
		return "", err
	}
	log := cfg.LogPath
	if log == "" {
		log = filepath.Join(cfg.DataDir, "logs", "reeve.log")
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	writeKey := func(k, v string) { fmt.Fprintf(&b, "  <key>%s</key>\n  <string>%s</string>\n", k, xmlEscape(v)) }
	writeKey("Label", "dev.reeve."+cfg.Name)
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	fmt.Fprintf(&b, "    <string>%s</string>\n    <string>serve</string>\n", xmlEscape(cfg.ExecPath))
	b.WriteString("  </array>\n")
	writeKey("WorkingDirectory", cfg.DataDir)
	b.WriteString("  <key>RunAtLoad</key>\n  <true/>\n  <key>KeepAlive</key>\n  <dict>\n    <key>SuccessfulExit</key>\n    <false/>\n  </dict>\n")
	writeKey("StandardOutPath", log)
	writeKey("StandardErrorPath", log)
	writeKey("EnvironmentVariables", "") // 占位被下面覆盖
	b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
	writeKey("REEVE_HOME", cfg.DataDir)
	b.WriteString("  </dict>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.String(), nil
}

func install(ctx context.Context, cfg Config, r runner) (Result, error) {
	content, err := renderLaunchd(cfg)
	if err != nil {
		return Result{}, err
	}
	path, err := plistPath(cfg)
	if err != nil {
		return Result{}, err
	}
	changed, err := writeIfChanged(path, content)
	if err != nil {
		return Result{}, err
	}
	uid := strconv.Itoa(os.Getuid())
	cmds := [][]string{{"launchctl", "bootout", "gui/" + uid, path}, {"launchctl", "bootstrap", "gui/" + uid, path}, {"launchctl", "enable", "gui/" + uid + "/dev.reeve." + cfg.Name}}
	var ran []string
	for _, c := range cmds {
		_, _ = r.run(ctx, c[0], c[1:]...)   // bootout 对未安装的服务会失败，忽略
		ran = append(ran, strings.Join(c, " "))
	}
	return Result{Path: path, Changed: changed, Commands: ran}, nil
}
```

> 说明：`renderLaunchd` 里那行 `writeKey("EnvironmentVariables", "")` 是笔误示例——实现时删掉，只保留后面的 `<dict>`。`xmlEscape` 用 `encoding/xml` 的 `EscapeText` 或手写替换 `<>&"`。

`systemd.go`（`//go:build linux`）生成：

```ini
[Unit]
Description=reeve assistant
After=network-online.target

[Service]
Type=simple
ExecStart=<ExecPath> serve
WorkingDirectory=<DataDir>
Environment=REEVE_HOME=<DataDir>
Environment=PATH=/usr/local/bin:/usr/bin:/bin
Restart=on-failure
RestartSec=3
StandardOutput=append:<LogPath>
StandardError=append:<LogPath>

[Install]
WantedBy=default.target
```

路径 `<XDG_CONFIG_HOME or ~/.config>/systemd/user/<Name>.service`；`systemctl --user daemon-reload` 后 `enable --now`；卸载 `disable --now` + 删文件 + `daemon-reload`。

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/service -count=1 && go vet ./internal/service && gofmt -l internal/service`
Expected: PASS（macOS 上跑 darwin 用例；`GOOS=linux go build ./internal/service` 也要过）

- [ ] **Step 5: 提交**

```bash
git add internal/service
git commit -m "feat(service): launchd 与 systemd 用户服务生成与安装"
```

---

### Task 2: `reeve service install|uninstall|status`

**Files:**
- Modify: `cmd/reeve/main.go`（子命令分发与用法文本）
- Create: `cmd/reeve/service.go`（`serviceCmd`）
- Test: `cmd/reeve/main_test.go`（补分发用例）

**Interfaces:**
- Consumes: `service.Install/Uninstall/Status`、`service.Config`
- Produces: `reeve service install [--exec <path>] [--dry-run]`、`reeve service uninstall`、`reeve service status`

- [ ] **Step 1: 写失败测试**

```go
func TestServiceCommandRequiresAction(t *testing.T) {
	out, code := runMain(t, "service")
	if code == 0 || !strings.Contains(out, "install") {
		t.Fatalf("service 无参数应报用法，得到 code=%d out=%q", code, out)
	}
}

func TestServiceDryRunPrintsPlist(t *testing.T) {
	out, code := runMain(t, "service", "install", "--dry-run", "--exec", "/usr/local/bin/reeve")
	if code != 0 {
		t.Fatalf("dry-run 应成功：%d %s", code, out)
	}
	if !strings.Contains(out, "<key>Label</key>") && !strings.Contains(out, "[Service]") {
		t.Fatalf("dry-run 应打印服务定义：%s", out)
	}
}
```

- [ ] **Step 2: 实现**

```go
func serviceCmd(args []string) {
	if len(args) == 0 {
		usageService()
		os.Exit(2)
	}
	action, rest := args[0], args[1:]
	fl := flag.NewFlagSet("service "+action, flag.ExitOnError)
	execPath := fl.String("exec", "", "reeve 可执行文件路径；默认取当前进程的可执行文件")
	dry := fl.Bool("dry-run", false, "只打印将要写入的服务定义，不落地")
	_ = fl.Parse(rest)
	data, _ := dataDirs()
	cfg := service.Config{Name: "reeve", DataDir: data, LogPath: filepath.Join(data, "logs", "reeve.log")}
	cfg.ExecPath = *execPath
	if cfg.ExecPath == "" {
		exe, err := os.Executable()
		if err != nil {
			fatal("找不到自身可执行文件：%v", err)
		}
		cfg.ExecPath = exe
	}
	switch action {
	case "install":
		if *dry {
			_, content, err := service.Render(cfg) // 新增导出：按平台返回落盘路径与内容
			if err != nil {
				fatal("%v", err)
			}
			fmt.Print(content)
			return
		}
		res, err := service.Install(context.Background(), cfg)
		if err != nil {
			fatal("安装失败：%v", err)
		}
		fmt.Printf("已安装：%s\n", res.Path)
		for _, c := range res.Commands {
			fmt.Println("  " + c)
		}
	case "uninstall":
		res, err := service.Uninstall(context.Background(), cfg)
		if err != nil {
			fatal("卸载失败：%v", err)
		}
		fmt.Printf("已卸载：%s（变化=%v）\n", res.Path, res.Changed)
	case "status":
		st, err := service.Status(context.Background(), cfg)
		if err != nil {
			fatal("查询失败：%v", err)
		}
		fmt.Printf("已安装=%v 已加载=%v 路径=%s\n%s\n", st.Installed, st.Loaded, st.Path, st.Detail)
	default:
		usageService()
		os.Exit(2)
	}
}
```

> `service.Render(cfg) (path, content string, err error)` 作为新增导出（按平台返回 plist/unit 的落盘路径与文本），dry-run 与测试都用它。

- [ ] **Step 3: 真实安装/卸载验证（macOS）**

```bash
go build -o /tmp/reeve-test ./cmd/reeve
REEVE_HOME=$(mktemp -d /tmp/reeve-svc.XXXXXX) /tmp/reeve-test service install --exec /tmp/reeve-test
REEVE_HOME=$(mktemp -d /tmp/reeve-svc.XXXXXX) /tmp/reeve-test service status
REEVE_HOME=$(mktemp -d /tmp/reeve-svc.XXXXXX) /tmp/reeve-test service uninstall
```

Expected：install 打印 plist 路径与 `launchctl bootstrap`；status 显示 `已安装=true`；uninstall 后 `launchctl print gui/$(id -u)/dev.reeve.reeve` 返回非零。**必须卸载干净**（不留登录项）。

- [ ] **Step 4: 提交**

```bash
git add cmd/reeve
git commit -m "feat(cli): reeve service install/uninstall/status"
```

---

### Task 3: 发布脚本

**Files:**
- Create: `scripts/release.sh`
- Test: 手工跑（见 Step 3）

**Interfaces:**
- Produces: `dist/reeve_<version>_<os>_<arch>`（+ `dist/SHA256SUMS`），可选 `dist/reeve_<version>_android.apk`

- [ ] **Step 1: 写脚本**

```sh
#!/bin/sh
# 发布脚本：构建网页 → 交叉编译 → 打包 APK（可选）→ 校验和。
# 用法：scripts/release.sh 0.4.0
set -eu

version="${1:-}"
case "$version" in
  ''|*[!0-9.]*) echo "用法：scripts/release.sh MAJOR.MINOR.PATCH" >&2; exit 2 ;;
esac

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
out="$root/dist"
rm -rf "$out"
mkdir -p "$out"

echo "==> 构建网页"
(cd web && npm ci --silent && npm run build)

echo "==> 构建二进制（$version）"
for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do
  os="${target%/*}"; arch="${target#*/}"
  echo "  $os/$arch"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$version" \
    -o "$out/reeve_${version}_${os}_${arch}" ./cmd/reeve
done

if [ -x "./android/gradlew" ] && [ -n "${ANDROID_HOME:-}" ]; then
  echo "==> 构建 Android APK"
  (cd android && ./gradlew --no-daemon :app:assembleRelease -PreeveVersion="$version")
  cp android/app/build/outputs/apk/release/app-release.apk "$out/reeve_${version}_android.apk"
else
  echo "==> 跳过 Android APK（缺少 android/ 或 ANDROID_HOME）"
fi

echo "==> 校验和"
(cd "$out" && shasum -a 256 ./* > SHA256SUMS)
echo "完成：$out"
ls -l "$out"
```

- [ ] **Step 2: 让 Android 版本号可注入**

`android/app/build.gradle.kts` 用 `providers.gradleProperty("reeveVersion").getOrElse("0.0.0")` 生成 `versionName/versionCode`；无 `android/` 时脚本跳过。

- [ ] **Step 3: 实际跑一次（`0.0.1`，产物放 /tmp 不污染仓库）**

```bash
cp -R . /tmp/reeve-release-test 2>/dev/null || true   # 或直接在主仓库跑，dist/ 已在 .gitignore
./scripts/release.sh 0.0.1
file dist/reeve_0.0.1_darwin_arm64
dist/reeve_0.0.1_darwin_arm64 version     # 期望输出 "reeve 0.0.1"
shasum -a 256 -c dist/SHA256SUMS
```

- [ ] **Step 4: `.gitignore` 补 `dist/`；提交**

```bash
git add scripts/release.sh .gitignore android/app/build.gradle.kts 2>/dev/null || git add scripts/release.sh .gitignore
git commit -m "feat(release): 发布脚本与版本注入"
```

---

### Task 4: GitHub Release 工作流与文档

**Files:**
- Create: `.github/workflows/release.yml`
- Create: `docs/release.md`
- Create: `docs/service.md`

- [ ] **Step 1: 工作流**

```yaml
name: release
on:
  push:
    tags: ["v*"]
permissions:
  contents: write
jobs:
  binaries:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: "1.22.x" }
      - uses: actions/setup-node@v4
        with: { node-version: "22", cache: npm, cache-dependency-path: web/package-lock.json }
      - name: Test
        run: go build ./... && go vet ./... && go test ./...
      - name: Release
        run: ./scripts/release.sh "${GITHUB_REF_NAME#v}"
      - uses: softprops/action-gh-release@v2
        with:
          files: dist/*
```

- [ ] **Step 2: 文档**

`docs/service.md`：`reeve service install` 的前置条件（绝对路径、数据目录、日志）、`status`/`uninstall`、launchd 与 systemd 的差异、开机自启与 `KeepAlive` 语义、如何查看日志。
`docs/release.md`：`scripts/release.sh <version>` 的产物清单、版本注入点（`main.version` → `/api/health`）、APK 需要 `ANDROID_HOME`、tag 发布流程、校验和验证命令。

- [ ] **Step 3: 校验工作流语法**

Run: `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/release.yml'))" && echo yaml-ok`
Expected: `yaml-ok`（Actions 本身无法本地执行，文档里写明「未在 CI 实际跑过」）

- [ ] **Step 4: 提交**

```bash
git add .github/workflows/release.yml docs/release.md docs/service.md
git commit -m "docs: 服务安装与发布流程"
```

---

## 自审记录

- **Spec 覆盖**：§2 本机常驻（B1/B2）、§21 M2 的 `reeve service install`（B1/B2）、M4 的发布脚本（B3/B4）。
- **明确不做**：Windows 服务（`sc.exe`）——spec 只要求 macOS/Linux；自动更新检查保留既有的 `/api/update`，不新增自更新。
- **类型一致性**：`service.Config{Name,ExecPath,DataDir,LogPath}`、`Result{Path,Changed,Commands}`、`State{Installed,Loaded,Path,Detail}`、`Render/Install/Uninstall/Status` 在全部任务与文档中同名。
- **风险**：B2 Step 3 会真实写一次 `~/Library/LaunchAgents` 并 bootstrap，随后必须卸载；若该机器已有同名服务，`bootout` 会先移除再装回，属于预期。
