// bx-acceptance 回答「这台机器上,这一版真的在工作吗?」
//
// **它只读**:三个 GET + 一次读文件,一个字节都不改。要改动的步骤由它打印出来
// 给人自己敲 —— 项目所有者的机器是生产环境,这条是硬纪律。
//
// 用法(在仓库目录里):
//
//	go run ./cmd/bx-acceptance
//
// 退出码:**只有 Fail 才非零**。Unknown 不是失败。
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/getbx/bx/internal/acceptance"
)

func main() {
	checks := acceptance.Run(acceptance.Collect(context.Background()))
	fmt.Print(acceptance.Render(checks, acceptance.NotCheckableReadOnly()))
	os.Exit(acceptance.ExitCode(checks))
}
