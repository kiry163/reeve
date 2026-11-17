# 发布流程

发布由一个脚本 `scripts/release.sh` 和一个 GitHub Actions 工作流
`.github/workflows/release.yml` 组成。本地能完整跑通脚本；工作流本身**没有在真实的 GitHub
Actions 上执行过**，目前只做过 YAML 语法校验（见文末「已知边界」）。

## 版本规则

- 只接受**稳定版本**：`MAJOR.MINOR.PATCH`，恰好三段、每段纯数字、无前导零
  （`0.4.0` 合法，`00.4.0`、`0.4`、`0.4.0.1` 都非法）。
- **不接受预发布**：`0.4.0-rc1`、`0.4.0+build`、`v0.4.0` 一律在脚本开头报错退出（退出码 2），
  不会开始构建。
- Git tag 用 `vX.Y.Z`（如 `v0.4.0`），工作流把它去掉 `v` 后传给脚本：
  `./scripts/release.sh "${GITHUB_REF_NAME#v}"`。工作流只在推送 `v*` tag 时触发，但
  `v0.4.0-rc1` 这类 tag 会在脚本的版本校验处失败，不会产出 Release。

## 本地构建

```bash
./scripts/release.sh 0.4.0
```

前置条件：

- Go 1.22+（`go.mod` 声明 `go 1.22`）。
- Node.js 与 npm，且 `web/package-lock.json` 存在（脚本用 `npm ci` 构建网页）。
- 校验和工具：macOS 自带 `shasum`，Linux 一般是 `sha256sum`；两者都没有会直接失败。
- 脚本自身会 `cd` 到它所在目录的上一级（仓库根目录），不依赖调用时的当前目录；那里必须有
  `go.mod`，否则拒绝清理 `dist/`。

脚本会先清空 `dist/`，再依次：构建网页 → 交叉编译四个平台 → 尝试打包 APK（可选）→ 生成
`SHA256SUMS`。产物（`dist/` 已被 `.gitignore` 忽略）：

| 文件 | 内容 |
|---|---|
| `reeve_<version>_darwin_arm64` | macOS Apple Silicon |
| `reeve_<version>_darwin_amd64` | macOS Intel |
| `reeve_<version>_linux_arm64` | Linux ARM64 |
| `reeve_<version>_linux_amd64` | Linux x86-64 |
| `reeve_<version>_android.apk` | Android APK，仅在满足 Android 构建条件时出现 |
| `SHA256SUMS` | 上面所有文件（生成时刻已存在的）的 SHA-256，形如 `<hash>  <文件名>` |

二进制用 `CGO_ENABLED=0 GOOS/GOARCH go build -trimpath -ldflags "-s -w -X
main.version=<version>"` 生成，是静态链接的单文件。

`SHA256SUMS` 里用的是**不带 `./` 前缀的相对文件名**，在 `dist/` 目录内校验即可；它本身不会被
列进校验和文件。

## 校验和验证

下载后进入放产物的目录：

```bash
# macOS
shasum -a 256 -c SHA256SUMS

# Linux
sha256sum -c SHA256SUMS
```

每个文件应输出 `OK`；任一文件被改动或截断会输出 `FAILED` 并以非零码退出。`shasum -a 256`
与 `sha256sum` 的 `-c` 都接受同样的 `<hash>  <name>` 格式，两边生成的 `SHA256SUMS` 可互认。

## 版本注入点

脚本通过 `-ldflags "-X main.version=<version>"` 写入 `cmd/reeve/main.go` 的 `var version =
"dev"`。`main` 在启动时把它赋给 `bot.Version`，因此同一个版本号出现在：

- `reeve version` 输出（`reeve 0.4.0`）；
- `GET /api/health`：`{"api_version":1,"ok":true,"version":"0.4.0"}`（map 序列化按 key 排序；
  `/api/capabilities` 同样带 `version`）；
- 更新检查：与 GitHub 最新 Release 的版本比较。

本地直接 `go build` 时 `main.version` 仍是 `dev`，不会覆盖 `bot.Version`，健康检查显示
「开发版」。手工验证注入是否生效：

```bash
go build -ldflags "-X main.version=9.9.9" -o /tmp/reeve-inject ./cmd/reeve
/tmp/reeve-inject version
```

## Tag 发布流程

1. 确认要发布的提交已经合入目标分支，工作区干净。
2. 打 tag 并推送（tag 是唯一触发条件，普通 push 不会触发）：

   ```bash
   git tag v0.4.0
   git push origin v0.4.0
   ```

3. 工作流 `release` 在 `ubuntu-latest` 上分两个 job 执行：
   - `build`（`permissions: contents: read`）：`actions/checkout@v4` 检出 tag 对应的提交；
     `actions/setup-go@v5`（`1.22.x`）、`actions/setup-node@v4`（Node 22，npm 缓存，缓存键
     依赖 `web/package-lock.json`）；`go build ./... && go vet ./... && go test ./...`，任何
     一步失败都不发布；`./scripts/release.sh "${GITHUB_REF_NAME#v}"`；最后把 `dist/` 用
     `actions/upload-artifact@v4` 交给下一个 job（构建 job 没有写权限）。
   - `publish`（`needs: build`，`permissions: contents: write`，`environment: release`）：
     `actions/download-artifact@v4` 取回 `dist/`，再用 `softprops/action-gh-release@v2` 把
     `dist/*`（四个平台二进制 + `SHA256SUMS`；配置了 Android 签名 secret 时还会多一个 APK）
     挂到以 tag `v0.4.0` 命名的 GitHub Release 上。

   `permissions` 按 job 拆分：只有 `publish` 有 `contents: write`。`environment: release` 是
   人工批准闸门——在仓库 Settings → Environments 里给 `release` 配置 Required reviewers 后，
   `publish` 会停在等待批准；不配置这个环境时 GitHub 会以同名环境直接放行，等于没有闸门。
   工作流使用仓库自带的 `GITHUB_TOKEN`，不需要额外配置 secret。
4. Release 创建后补一段正文（工作流没传 `body`，正文默认为空），或者先写 `CHANGELOG` 再人工
   粘贴。

失败处理：如果测试或构建失败，Release 不会创建；修复后删除本地与远端 tag 再重新打同一个 tag：

```bash
git tag -d v0.4.0
git push origin :refs/tags/v0.4.0
```

已经创建了 Release 才发现问题，优先发下一个 patch 版本，而不是替换已发布的产物——下载方的
校验和可能已经记下了旧文件。

## Android APK

`android/` 已落地（Kotlin + Jetpack Compose，包名 `dev.reeve.app`）。`release.sh` 同时满足
以下条件才构建 APK：

- 存在 `android/` 目录，且 `android/gradlew` 可执行；
- `ANDROID_HOME` 非空；
- 签名四件套齐全：`REEVE_ANDROID_KEYSTORE`（PKCS#12 文件路径；CI 由 base64 secret 落盘生成）、
  `REEVE_ANDROID_KEYSTORE_PASSWORD`、`REEVE_ANDROID_KEY_ALIAS`、
  `REEVE_ANDROID_KEY_PASSWORD`。

此时脚本执行 `./gradlew --no-daemon :app:assembleRelease -PreeveVersion=<version>`，确认
`android/app/build/outputs/apk/release/app-release.apk` 存在并用 `apksigner verify` 校验签名
（找不到 `apksigner` 时只提示并跳过校验），然后复制成 `dist/reeve_<version>_android.apk`。
APK **在生成校验和之前**放进 `dist/`，所以 `SHA256SUMS` 会包含这一行。

签名是必需的：`release` 构建类型在没有 keystore 时**保持未签名**（AGP 产物名为
`app-release-unsigned.apk`），不会回退到 debug 密钥。debug 密钥每台机器/每次干净构建都不同，
用它发布会导致每个版本签名不一致、用户无法就地升级。

缺签名变量时脚本打印「未设置签名环境变量，跳过 APK（不会发布 debug 签名包）」并继续生成其余
产物；`REEVE_REQUIRE_APK=1` 会把任何「跳过」升级为失败（exit 1），但不会替你安装 SDK 或生成
密钥。

CI 侧：GitHub 托管的 `ubuntu-latest` runner 预装 Android SDK，并设置
`ANDROID_HOME=/usr/local/lib/android/sdk`。`.github/workflows/release.yml` 的
`Write Android keystore` step 把 base64 secret 解码到 `$RUNNER_TEMP/reeve-release.p12`；
紧接着的 `Release` step 发现该文件就导出 `REEVE_ANDROID_KEYSTORE`，三个口令/别名只作为这一个
step 的 env 注入（不写 `GITHUB_ENV`，也就不会出现在 `npm ci` 或其它步骤的环境里）。step 结束
（成功或失败）由 `Remove Android keystore` step 删除临时文件。仓库需要配置：

| Secret | 内容 |
| --- | --- |
| `REEVE_ANDROID_KEYSTORE_BASE64` | keystore 文件的 base64（生成：`base64 < release.p12`，macOS 也可 `base64 -i release.p12`） |
| `REEVE_ANDROID_KEYSTORE_PASSWORD` | keystore 口令 |
| `REEVE_ANDROID_KEY_ALIAS` | key alias |
| `REEVE_ANDROID_KEY_PASSWORD` | key 口令 |

**不配置任何 Android secret 时自动跳过 APK**（不会发布 debug 签名包），Go/网页产物照常发布。
想让缺 APK 直接失败，可给 Release step 加 `REEVE_REQUIRE_APK: "1"`。

### 手工补 APK

只有当 Release 里缺少 APK 时才需要这一步（例如发布时 CI 没拿到 Android 构建条件）。
**不要**为了补 APK 在本地重跑 `release.sh`：它会 `rm -rf dist/` 并重新编译四个二进制，
本地新产物的校验和与 CI 已发布的二进制不一定一致，容易和已上传的文件错配。

正确做法是保留 Release 上已有的产物，只构建 APK、追加它的一行校验和再一并上传：

```bash
# 1. 在 tag 对应的提交上构建并签名 APK（需要 Android SDK、可执行的 android/gradlew 和 keystore）
cd /path/to/reeve
git checkout vX.Y.Z
export ANDROID_HOME=/path/to/Android/sdk
export REEVE_ANDROID_KEYSTORE=/path/to/keystore.p12
export REEVE_ANDROID_KEYSTORE_PASSWORD=...
export REEVE_ANDROID_KEY_ALIAS=...
export REEVE_ANDROID_KEY_PASSWORD=...
(cd android && ./gradlew --no-daemon :app:assembleRelease -PreeveVersion=X.Y.Z)
# 产物必须是已签名的 app-release.apk；未签名时是 app-release-unsigned.apk，不能发布
"$ANDROID_HOME"/build-tools/*/apksigner verify android/app/build/outputs/apk/release/app-release.apk

# 2. 下载 Release 里已有的全部产物（含 CI 生成的 SHA256SUMS）
mkdir -p /tmp/reeve-release && cd /tmp/reeve-release
gh release download vX.Y.Z

# 3. 放入 APK，用文件名（不带路径）计算并追加校验和
cp /path/to/reeve/android/app/build/outputs/apk/release/app-release.apk \
   reeve_X.Y.Z_android.apk
shasum -a 256 reeve_X.Y.Z_android.apk >> SHA256SUMS   # Linux 用 sha256sum

# 4. 上传 APK 与更新后的 SHA256SUMS
gh release upload vX.Y.Z reeve_X.Y.Z_android.apk SHA256SUMS --clobber
```

`gh` 需要已登录且有该 Release 的写权限。上传后重新下载全部文件验证：在产物目录执行
`shasum -a 256 -c SHA256SUMS`（Linux `sha256sum -c`），每一个文件都应输出 `OK`。不要在没
上传 APK 的情况下单独替换 `SHA256SUMS`；`--clobber` 会覆盖 Release 上的旧校验和文件。

## 已知边界

- **工作流未在真实 Actions 上跑过**。本仓库只做过本地 YAML 解析校验：

  ```bash
  python3 -c "import yaml; yaml.safe_load(open('.github/workflows/release.yml'))" && echo yaml-ok
  ```

  （本机没装 PyYAML 时先 `pip install pyyaml`，或在临时 venv 里执行同一条命令。）

  各 action 的版本、缓存行为、`GITHUB_TOKEN` 权限、`environment: release` 的人工批准、Release
  上传结果都未经实际运行验证；第一次 tag 发布后应人工核对 Release 里的文件清单与
  `SHA256SUMS` 一致（四个平台二进制，配置了 Android 签名 secret 时还有一个 APK），并跑一次
  `sha256sum -c SHA256SUMS`。
- **不支持预发布 tag**：没有 `-rc`/`beta` 之类的通道，也无法用 tag 触发「草稿」Release。
- **APK 取决于签名 secret**：`android/` 已落地，`android/gradlew` 可执行且 `ANDROID_HOME`
  非空时，CI 与本地都会尝试构建 APK；只有签名四件套齐全才会产出并发布，未配置
  `REEVE_ANDROID_KEYSTORE_BASE64` 等 secret 时 CI 会跳过 APK（其余产物照常发布）。Release 里
  缺 APK 时按上一节手工补，不要重跑 `release.sh`。
- **action 未按 commit SHA 固定**：工作流里的 `actions/*` 与 `softprops/action-gh-release@v2`
  都按主版本 tag 引用，没有写成完整 SHA。tag 可以被上游改写，追求可复现/可审计构建时应改为
  `uses: owner/action@<40 位 SHA>`；本仓库目前只做语法校验，因此这是已知限制。
- `release.sh` 会 `rm -rf dist/`，不要把它当成增量构建目录。
