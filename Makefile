# reeve — 构建与开发入口
#
# 所有目标只调用仓库里已有的工具链：Go、Node.js（web/）、Gradle（android/）。
# Android 目标需要 JAVA_HOME 与 ANDROID_HOME 已按 docs/android.md 配置好。

SHELL := /bin/bash

GO      ?= go
NPM     ?= npm
BIN     := bin/reeve
FAKEAI  := bin/fakeai
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS ?= -X main.version=$(VERSION)
GOFLAGS ?= -trimpath

.DEFAULT_GOAL := help
.PHONY: help build build-go web fakeai serve test test-go test-web test-android \
        lint fmt vet apk release clean _check-android

# Android 目标的前置检查：给出可执行的错误信息，而不是让 gradlew 自己报「找不到 Java」。
_check-android:
	@test -n "$(JAVA_HOME)" && test -x "$(JAVA_HOME)/bin/java" || { \
		echo "Android 目标需要 JAVA_HOME 指向 JDK 17（配置方法见 docs/android.md）"; exit 1; }
	@test -n "$(ANDROID_HOME)$(ANDROID_SDK_ROOT)" || { \
		echo "Android 目标需要 ANDROID_HOME 或 ANDROID_SDK_ROOT（配置方法见 docs/android.md）"; exit 1; }

help: ## 列出所有目标
	@awk 'BEGIN {FS = ":.*## "; printf "\n用法：make <目标>\n\n"} \
		/^[a-zA-Z0-9_-]+:.*## / {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@printf "\n"

build: web build-go ## 构建前端并打包成 bin/reeve（单二进制，前端已嵌入）

build-go: ## 只编译 Go（使用当前 web/dist，未构建前端时为占位页面）
	$(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/reeve

web: ## 构建控制台前端（npm ci + typecheck + vite build）
	cd web && $(NPM) ci --no-audit --no-fund
	cd web && $(NPM) run build
	@touch web/dist/.gitkeep

fakeai: ## 构建冒烟测试用的假模型服务 bin/fakeai
	$(GO) build $(GOFLAGS) -o $(FAKEAI) ./cmd/fakeai

serve: build ## 构建并在前台启动服务（默认 127.0.0.1:18800）
	./$(BIN) serve

test: test-go test-web test-android ## 跑全部测试（Go + 前端 + Android 单测）

test-go: ## 跑 Go 测试
	$(GO) test -count=1 ./...

test-web: ## 跑前端测试（node --test）
	cd web && $(NPM) ci --no-audit --no-fund && $(NPM) test

test-android: _check-android ## 跑 Android 单元测试
	cd android && ./gradlew --no-daemon :app:testDebugUnitTest

lint: vet ## 静态检查：gofmt 一致性、go vet、前端 typecheck
	@out=$$(gofmt -l cmd internal web); \
	if [ -n "$$out" ]; then echo "以下文件未格式化（make fmt）："; echo "$$out"; exit 1; fi
	cd web && $(NPM) ci --no-audit --no-fund && $(NPM) run typecheck

vet: ## go vet
	$(GO) vet ./...

fmt: ## 格式化 Go 代码
	$(GO) fmt ./...

apk: _check-android ## 构建 debug APK（android/app/build/outputs/apk/debug/app-debug.apk）
	cd android && ./gradlew --no-daemon :app:assembleDebug

release: ## 走正式发布流程（需 VERSION=X.Y.Z，产物在 dist/）
	@case "$(VERSION)" in [0-9]*.[0-9]*.[0-9]*) ;; \
	*) echo "用法：make release VERSION=X.Y.Z（scripts/release.sh 只接受纯数字三段版本，不接受 v 前缀）"; exit 2;; esac
	./scripts/release.sh $(VERSION)

clean: ## 清理构建产物（保留 web/dist 的占位文件）
	rm -rf bin dist android/app/build
	rm -rf web/dist/*
	touch web/dist/.gitkeep
