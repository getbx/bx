package cli

import (
	"log"
	"os"
	"time"

	"github.com/getbx/bx/internal/corestartfailure"
	"github.com/getbx/bx/internal/supervisor"
)

// startFailureWriter 是「把记录落盘」这一步。**做成参数是为了让「没给
// --start-failure-file 时一个字节都不写」这句话可以被断言。**
//
// 直接对着文件系统测这件事测不出来:path 为空时 corestartfailure.Write 会往
// 当前目录建一个临时文件、rename 到 "" 失败、再由 defer 删掉 —— 磁盘上不留
// 任何痕迹,于是「去掉那道门」这个变异在一条只查目录的测试下照样全绿。
// 而那道门正是陈旧记录的第一层防线。
type startFailureWriter func(path string, record corestartfailure.Record) error

// runWithStartFailureRecord 让 Core **自报**它为什么没起来。
//
// 2026-09-12 那次事故里 Core 从第一秒就知道 `dial tcp <server>:443: i/o
// timeout`,而这句话进了 root-only 的 /var/log/bx.log 就没了出口:Guardian
// 只看得见「20 秒了 socket 还没出现」,于是用户拿到七次「系统里可能有第二个
// Core」。这个包装是那条信息唯一的结构化出口。
func runWithStartFailureRecord(path string, run func() error) error {
	return recordStartFailure(path, run(), corestartfailure.Write)
}

// recordStartFailure 是那条判定本身。
//
// **path 为空 ⇒ 一个字节都不写,而且这是构造上的第一层防线。**
// --start-failure-file 只由 Guardian 传;手敲的 `sudo bx run`(本项目自己
// 文档化的调试路径)不传,所以它不可能在那个位置留下一份看起来像「这一次」
// 的记录。第二层(PID + 本次健康窗口双重匹配)在 Guardian 那边 —— 陈旧文件
// 这个仓库栽过三次(upgrade-intent.json、core-process.json、那份四分之三
// 是假的缺口清单),两层都要。
//
// **写盘失败绝不改变 run 返回的那个错误。** 一个诊断不许把一次故障换成另一次
// 故障:用户的 VPS 不通,而 bx 因为 /var/lib/bx 写不进去就改口说别的,等于
// 既丢掉了这支修复的产出,又编了一个不存在的病因。
func recordStartFailure(path string, err error, write startFailureWriter) error {
	if err == nil || path == "" {
		return err
	}
	record := corestartfailure.Record{
		SchemaVersion: corestartfailure.SchemaVersion,
		PID:           os.Getpid(),
		At:            time.Now(),
		// 只有码。细节留在 Core 日志里(它是给人读的、root-only);这份记录
		// 跨进程递给 Guardian,而它的码会一路走到用户面前。
		Code: supervisor.StartFailureCode(err),
	}
	if writeErr := write(path, record); writeErr != nil {
		// 只记一行。**不带上 err** —— 那句话里可能有服务器链接(uuid 在里面);
		// 它已经进过 Core 日志一次,没必要为一次写盘失败再复述一遍。
		log.Printf("core_start_failure_record_write_failed code=%s err=%v", record.Code, writeErr)
	}
	return err
}
