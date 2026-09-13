// Package corestartfailure 是 Core 与 Guardian 之间那份**启动失败记录**的唯一
// 定义处 —— 结构、schema 版本、路径、读写,全在这里。
//
// 起因是 2026-09-12 那次真机事故:VPS 连 ssh 与 ping 都不通,Core 从第一秒就
// 知道 `dial tcp <server>:443: i/o timeout`,而它把这句话写进 root-only 的
// /var/log/bx.log 就退出了;Guardian 只看得见「20 秒了 socket 还没出现」,于是
// 用户拿到的是七次「系统里可能有第二个 Core」。缺的从来不是信息,是一个**跨
// 进程的、结构化的**出口。
//
// **为什么单独一个叶子包**:写的人在 internal/cli(bx run),读的人在
// internal/guardian,而这两边唯一需要逐字对齐的东西就是这份记录。本仓库为
// 「同一份清单在两个包里各抄一份」栽过(internal/udpsource、
// internal/barriercidr 都是同一个先例),漂移在构造上不可能才是解法。
//
// **为什么不是管道**:CLAUDE.md 记着一次实打实的死锁 —— 管道的 EOF 被**孙
// 进程**(Core 自己 spawn 的 sing-box)继承,父进程永远等不到。
//
// **为什么不是读 Core 的日志**:判据长在文本匹配上是本仓库明确反对的形状,
// 而且那份日志是多次 spawn 共用的,分不清哪几行属于这一次。
package corestartfailure

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SchemaVersion 是这份记录当前的版本。**读的人认不出就当「这一次没说」**,
// 绝不将就着用一份自己读不懂的记录去下结论。
const SchemaVersion = 1

// DefaultPath 是 Guardian 传给 Core、随后自己回来读的那个位置。
// 与 core-process.json、maintenance-hold.json 同在 /var/lib/bx(0700 root-only)。
const DefaultPath = "/var/lib/bx/core-start-failure.json"

// ErrUnknownSchema:记录在,但它的 schema 版本我们不认识。
var ErrUnknownSchema = errors.New("core start failure record: unknown schema version")

// Record 是 Core 自报的那一次启动失败。
//
// **只有码,没有自由文本。** 细节照旧进 Core 日志(那是给人读的、root-only);
// 这份记录要跨进程递给 Guardian,而 Guardian 的失败码会一路走到用户面前 ——
// 一个不含自由文本的记录按构造漏不出路径、链接与凭据。
// 由 TestRecordCarriesNothingButACode 打在序列化字节上钉住。
type Record struct {
	SchemaVersion int `json:"schema_version"`
	// PID 是写这份记录的那个 Core 进程。Guardian 拿它与自己刚 fork 出来的那个
	// PID 比 —— 这是「这份记录是不是这一次的」两道判据里的第一道。
	PID int `json:"pid"`
	// At 是失败发生的时刻,第二道判据(必须落在本次健康窗口内)。
	//
	// **亚秒精度是承重的**:Guardian 在一段故障里会连着 spawn 好几个 Core
	// (事故日志里是五个),秒级截断会让相邻两次落在同一秒上,窗口判据就分不
	// 开它们了。encoding/json 对 time.Time 用 RFC3339Nano,精度自然保住。
	At time.Time `json:"at"`
	// Code 是 supervisor.StartFailureCode 给出的那个码。
	Code string `json:"code"`
}

// Write 原子地写下一份记录:临时文件 → fsync → rename。
//
// 原子性不是讲究:读的人(Guardian)与写的人(Core)是两个进程,没有任何锁,
// 而一份被读到一半的 JSON 会让 Guardian 落到「读不动 ⇒ 这一次没说」那一支 ——
// 那虽然安全,却把本来能说清的一次故障退回了沉默。
func Write(path string, record Record) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".core-start-failure-")
	if err != nil {
		return fmt.Errorf("create core start failure record: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Read 读一份记录。**认不出的 schema 报 ErrUnknownSchema,坏掉的 JSON 报解析
// 错误** —— 两者都绝不退化成一个零值 Record:PID 0 与真实 PID 比出来的「不相等」
// 只是碰巧对了,而 Code 空串会让调用方以为 Core 什么都没说。
func Read(path string) (Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, fmt.Errorf("parse core start failure record: %w", err)
	}
	if record.SchemaVersion != SchemaVersion {
		return Record{}, fmt.Errorf("%w: %d", ErrUnknownSchema, record.SchemaVersion)
	}
	return record, nil
}

// Remove 删掉记录。**本来就不在不算失败** —— 它的两个调用点(spawn 之前的
// 预删、读完之后的清理)都是诊断路径,而停止/诊断路径不许因为别的事没做成
// 而失败。
func Remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
