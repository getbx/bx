package cli

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"strings"
	"time"

	"github.com/getbx/bx/internal/corestartfailure"
	"github.com/getbx/bx/internal/install"
)

// linuxStartFailureNote 在 linux 上说出 Core 为什么起不来(known-gaps A9)。
//
// darwin 上这件事由 Guardian 读 Core 自报的记录来说;linux 走 systemd、不经
// Guardian,于是由 `bx status` 读同一份记录、用同一套措辞(coreStartFailureAdvice)。
//
// **只在服务「要跑却起不来」时开口**:`activating`(systemd 在重启它)或 `failed`
// (重启次数用完了)。用户自己停掉的(`inactive`)不说 —— 那时上一次失败留下的
// 记录是历史,不是现状。读不到记录、认不出码一律不说,不猜。
func linuxStartFailureNote(unitState string, readRecord func() (corestartfailure.Record, error), facts startFailureServers) string {
	switch strings.TrimSpace(unitState) {
	case "activating", "failed":
	default:
		return ""
	}
	record, err := readRecord()
	if errors.Is(err, fs.ErrPermission) {
		// /var/lib/bx 是 drwx------,非 root 读不到。**如实说「在起、起不来、原因要
		// sudo 看」**,不退回「bx is not running / Start it」—— 那句在服务正反复重启
		// 时是假话,而且叫人去做一件已经在发生的事。
		return "bx is trying to start but keeps failing. To see why: " + elevateStatusCommand() + "\n"
	}
	if err != nil || strings.TrimSpace(record.Code) == "" {
		return ""
	}
	return coreStartFailureAdvice(coreStartFailureCodePrefix+record.Code, facts)
}

// liveLinuxStartFailureNote 接上真的 systemd 与真的记录。
func liveLinuxStartFailureNote(configPath string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// `is-active` 对不在跑的服务退出码非 0,但 stdout 照样给出状态词 —— 只看输出。
	out, _ := exec.CommandContext(ctx, "systemctl", "is-active", install.ServiceName).Output()
	return linuxStartFailureNote(string(out), func() (corestartfailure.Record, error) {
		return corestartfailure.Read(corestartfailure.DefaultPath)
	}, readStartFailureServers(configPath))
}

// elevateStatusCommand 是那句提示里要敲的命令。
func elevateStatusCommand() string { return "sudo bx status" }
