# winres — Windows exe 资源

`bx.exe` 的 manifest(UAC / DPI / long-path)、应用图标、版本信息。

- **真相源**:`winres.json`(文本,可 review)。
- **图标源**:`icon.png`(256²)+ `icon16.png`(32²),go-winres 合成 icon group。
- **产物**:`rsrc_windows_amd64.syso` / `rsrc_windows_arm64.syso`(提交进仓库根),
  Go 链接器按 GOOS/GOARCH 自动链入 windows 构建。

## 重生成

改了 winres.json 或图标后:

    go generate ./...        # 等价 go-winres make --in winres/winres.json --arch amd64,arm64 --out rsrc

然后提交新的 `.syso`。

## 不变量

- manifest `execution-level` 恒 `as invoker`——**绝不** requireAdministrator/highestAvailable。
  bx 托盘非提权常驻、改动系统的动作各自 per-action UAC(见子项目②);强制提权会推翻该设计。

## 版本

`winres.json` 里 version 占位 `0.0.0.0`;release 时 CI 用
`go-winres make … --product-version git-tag --file-version git-tag` 覆盖成 tag 版本
(与 `-ldflags -X internal/version.Version` 一致)。
