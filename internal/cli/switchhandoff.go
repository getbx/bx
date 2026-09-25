package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/getbx/bx/internal/guardian"
)

// switchHandoffPath 记着「一次屏障下切换 Guardian(D3)还没交接完」。
//
// **为什么要落盘(2026-09-25 真机)**:第一次真切换停在第二步(旧 Core 正在退出时
// 读它的可执行路径报 EINVAL),屏障留着、网络断着 —— 这是设计上正确的一半。而用户
// 接下来做的是任何人都会做的事:`sudo bx up`。那条路不知道有一个切换没做完,新
// Guardian 在一道它不拥有、且服务器 /32 已被旧 Core 删掉的屏障后面起 Core,隧道
// 连不上服务器,报出一句指向「bx 自己的直连器」的误导诊断。
//
// 落了盘,`bx up` 就能认出这个状态,用同一份交接请求走 /v1/migrate 把切换做完;
// 重跑 app-install 也不必再去问一个已经不在的 Core(known-gaps A11)。
var switchHandoffPath = "/var/lib/bx/switch-handoff.json"

type switchHandoffRecord struct {
	Request guardian.MigrationRequest `json:"request"`
	At      time.Time                 `json:"at"`
}

func saveSwitchHandoff(request guardian.MigrationRequest) error {
	data, err := json.Marshal(switchHandoffRecord{Request: request, At: time.Now().UTC()})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(switchHandoffPath), 0o700); err != nil {
		return err
	}
	tmp := switchHandoffPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, switchHandoffPath)
}

// loadSwitchHandoff:没有记录返回 ok=false;读得到而解析不了、或内容不合法,如实报错
// (不当成「没有」—— 那会让 bx up 在一道屏障后面盲起 Core)。
func loadSwitchHandoff() (guardian.MigrationRequest, bool, error) {
	data, err := os.ReadFile(switchHandoffPath)
	if errors.Is(err, os.ErrNotExist) {
		return guardian.MigrationRequest{}, false, nil
	}
	if err != nil {
		return guardian.MigrationRequest{}, false, err
	}
	var record switchHandoffRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return guardian.MigrationRequest{}, false, fmt.Errorf("read unfinished Guardian switch record %s: %w", switchHandoffPath, err)
	}
	request, err := guardian.ValidateMigrationRequest(record.Request)
	if err != nil {
		return guardian.MigrationRequest{}, false, fmt.Errorf("unfinished Guardian switch record %s: %w", switchHandoffPath, err)
	}
	return request, true, nil
}

func clearSwitchHandoff() error {
	if err := os.Remove(switchHandoffPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
