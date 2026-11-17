#!/bin/sh
# 发布脚本：构建网页 → 交叉编译 → 打包 APK（可选）→ 生成校验和。
# 用法：scripts/release.sh MAJOR.MINOR.PATCH
# 例：scripts/release.sh 0.4.0
# 只接受稳定的三段数字版本号，不接受 v0.4.0-rc1 之类的预发布后缀。
set -eu

usage() {
	if [ "$#" -gt 0 ]; then
		echo "错误：版本号必须是三段数字 MAJOR.MINOR.PATCH（收到 '$*'）" >&2
	fi
	echo "用法：scripts/release.sh MAJOR.MINOR.PATCH（例：0.4.0）" >&2
	exit 2
}

case "${1:-}" in
-h | --help)
	echo "用法：scripts/release.sh MAJOR.MINOR.PATCH（例：0.4.0）"
	exit 0
	;;
esac

version="${1:-}"
[ "$#" -eq 1 ] || usage "$@"

# 校验：恰好三段、每段纯数字且无前导零。subshell 里 set -f，
# 避免 $version 被 pathname expansion 展开成当前目录里碰巧匹配的文件名。
valid_version() {
	(
		set -f
		old_ifs="$IFS"
		IFS=.
		# shellcheck disable=SC2086 # 故意按点拆分
		set -- $1
		IFS="$old_ifs"
		[ "$#" -eq 3 ] || exit 1
		for part in "$@"; do
			case "$part" in
			'' | *[!0-9]*) exit 1 ;;
			esac
			case "$part" in
			0) ;;
			0*) exit 1 ;;
			esac
		done
	)
}
valid_version "$version" || usage "$version"

# 校验和工具：macOS 自带 shasum，Linux 常见 sha256sum。必须在任何构建之前确认，
# 否则会在全部编译完之后才失败并留下半成品。
if command -v shasum >/dev/null 2>&1; then
	SHA_TOOL="shasum -a 256"
elif command -v sha256sum >/dev/null 2>&1; then
	SHA_TOOL="sha256sum"
else
	echo "错误：找不到校验和工具，需要 shasum 或 sha256sum。" >&2
	exit 3
fi

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

# 先确认确实是 reeve 仓库根目录，再清理输出目录（防止经符号链接调用时误删）。
if [ ! -f go.mod ]; then
	echo "错误：$root 不像 reeve 仓库根目录（缺少 go.mod），拒绝清理 dist。" >&2
	exit 1
fi

out="$root/dist"
rm -rf "$out"
mkdir -p "$out"

if [ ! -f web/package-lock.json ]; then
	echo "错误：找不到 web/package-lock.json，前端构建需要它。" >&2
	exit 1
fi

echo "==> 构建网页"
(cd web && npm ci --silent && npm run build)
# Vite 会清空 dist；把占位文件补回来，避免发布后工作区出现脏文件。
touch web/dist/.gitkeep

echo "==> 构建二进制（${version}）"
for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do
	os="${target%/*}"
	arch="${target#*/}"
	echo "  $os/$arch"
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
		-ldflags "-s -w -X main.version=$version" \
		-o "$out/reeve_${version}_${os}_${arch}" ./cmd/reeve
done

if [ ! -d android ]; then
	if [ "${REEVE_REQUIRE_APK:-}" = "1" ]; then
		echo "错误：REEVE_REQUIRE_APK=1 但仓库里没有 android/ 目录。" >&2
		exit 1
	fi
	echo "==> 跳过 Android APK（没有 android/ 目录）"
else
	apk_ready=1
	if [ ! -x ./android/gradlew ]; then
		echo "警告：android/ 存在但 android/gradlew 不存在或不可执行，跳过 APK。" >&2
		apk_ready=0
	fi
	if [ -z "${ANDROID_HOME:-}" ]; then
		echo "警告：android/ 存在但未设置 ANDROID_HOME，跳过 APK。" >&2
		apk_ready=0
	fi
	# release 变体不再回退 debug 密钥：没有签名四件套时产物是未签名的
	# app-release-unsigned.apk，不能发布（每个干净构建的 debug 密钥都不同，用户无法就地升级）。
	if [ "$apk_ready" -eq 1 ]; then
		if [ -z "${REEVE_ANDROID_KEYSTORE:-}" ] ||
			[ -z "${REEVE_ANDROID_KEYSTORE_PASSWORD:-}" ] ||
			[ -z "${REEVE_ANDROID_KEY_ALIAS:-}" ] ||
			[ -z "${REEVE_ANDROID_KEY_PASSWORD:-}" ]; then
			echo "==> 未设置签名环境变量，跳过 APK（不会发布 debug 签名包）"
			apk_ready=0
		fi
	fi
	if [ "$apk_ready" -eq 0 ]; then
		if [ "${REEVE_REQUIRE_APK:-}" = "1" ]; then
			echo "错误：REEVE_REQUIRE_APK=1 但 Android 构建或签名条件不满足。" >&2
			exit 1
		fi
	else
		echo "==> 构建 Android APK"
		(cd android && ./gradlew --no-daemon :app:assembleRelease -PreeveVersion="$version")
		apk=android/app/build/outputs/apk/release/app-release.apk
		if [ ! -f "$apk" ]; then
			echo "错误：找不到已签名的 APK 产物 $apk（未签名的构建不会发布）。" >&2
			exit 1
		fi
		apksigner=""
		for candidate in "${ANDROID_HOME:-}/build-tools"/*/apksigner; do
			[ -x "$candidate" ] && apksigner="$candidate"
		done
		# apksigner 是 shell 脚本，需要 java 才能跑；没有 Java 时不能把「跑不起来」当成签名失败。
		java_ok=0
		if [ -n "${JAVA_HOME:-}" ] && [ -x "$JAVA_HOME/bin/java" ]; then
			java_ok=1
		elif command -v java >/dev/null 2>&1; then
			java_ok=1
		fi
		if [ -n "$apksigner" ] && [ "$java_ok" -eq 1 ]; then
			if ! "$apksigner" verify "$apk" >/dev/null 2>&1; then
				echo "错误：APK 签名校验失败：$apk" >&2
				exit 1
			fi
		else
			echo "警告：找不到 apksigner 或 Java，跳过 APK 签名校验。" >&2
		fi
		cp "$apk" "$out/reeve_${version}_android.apk"
	fi
fi

echo "==> 生成校验和"
set -- "$out"/*
if [ ! -e "$1" ]; then
	echo "错误：$out 里没有任何产物。" >&2
	exit 1
fi
# 在 dist 里用不带 ./ 前缀的相对文件名，先写临时文件再原子替换。
# shellcheck disable=SC2086 # SHA_TOOL 故意按空格拆成两个词
(cd "$out" && $SHA_TOOL * >SHA256SUMS.tmp && mv SHA256SUMS.tmp SHA256SUMS)

echo "完成：$out"
ls -l "$out"
