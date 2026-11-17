//go:build !darwin && !linux

package service

import "context"

// Render 在不支持的平台上没有可生成的配置。
func Render(Config) (path, content string, err error) {
	return "", "", ErrUnsupported
}

func install(context.Context, Config, runner) (Result, error) {
	return Result{}, ErrUnsupported
}

func uninstall(context.Context, Config, runner) (Result, error) {
	return Result{}, ErrUnsupported
}

func status(context.Context, Config, runner) (State, error) {
	return State{}, ErrUnsupported
}
