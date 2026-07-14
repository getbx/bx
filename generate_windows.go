package main

// Windows exe 资源(manifest / 图标 / 版本信息)由 go-winres 从 winres/winres.json
// 生成为 rsrc_windows_{amd64,arm64}.syso,Go 链接器按 GOOS/GOARCH 自动链入 windows 构建。
// 换图标/版本后:`go generate ./...` 重生成 .syso 并提交。真相源是文本 winres/winres.json。
//
//go:generate go run github.com/tc-hib/go-winres@v0.3.3 make --in winres/winres.json --arch amd64,arm64 --out rsrc
