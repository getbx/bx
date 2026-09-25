//go:build darwin

package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/install"
)

// barrierSwitch 是「保护开着时在屏障下换 Guardian」那四步的 darwin 实现(D3,
// docs/superpowers/specs/2026-09-25-fail-closed-guardian-switch-design.md)。
//
// 它不另写任何一份交接逻辑:网关与服务器地址来自 legacyMigrationRequest(从正在跑
// 的 Core 问),屏障计划是 Guardian 迁移用的同一份(guardian.InstallHandoffBarrier),
// 交接走 /v1/migrate —— 新 Guardian 用同一份计划接过屏障、在屏障下起 Core、健康后
// 释放。**这里没有任何一步会把屏障拆掉**:成功时拆它的是新 Guardian,失败时拆不拆
// 是用户的决定(sudo bx down)。
type barrierSwitch struct {
	configPath string
	handoff    guardian.MigrationRequest
	ready      bool
}

const (
	barrierSwitchStepTimeout = 60 * time.Second
	coreGoneTimeout          = 45 * time.Second
)

func (s *barrierSwitch) up(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, barrierSwitchStepTimeout)
	defer cancel()
	request, err := legacyMigrationRequest(ctx, s.configPath, migrationMetadataDeps{})
	if err != nil {
		return fmt.Errorf("could not work out the gateway and server addresses the barrier needs: %w", err)
	}
	store := guardian.OpenDefaultStore()
	// 挂起先于屏障:旧 Guardian 看到 Core 退出时不重启它,新 Guardian 起来时不自己起一个
	// 没有屏障 handoff 的 Core。
	if err := store.ArmMaintenanceHold(guardian.HoldReasonUpgrade, time.Now()); err != nil {
		return fmt.Errorf("arming the maintenance hold: %w", err)
	}
	if err := guardian.InstallHandoffBarrier(ctx, request); err != nil {
		// 装到一半:撤回已经装上的 reject(Core 还在跑、它的路由还在,撤回之后就是
		// 开始之前的样子 —— 不是泄漏),再销挂起。这一步失败时的承诺是「什么都没改」。
		undoErr := guardian.RemoveBlockingBarrierRoutes(context.WithoutCancel(ctx), nil)
		_, clearErr := store.ClearMaintenanceHold()
		return errors.Join(fmt.Errorf("installing the barrier: %w", err), undoErr, clearErr)
	}
	s.handoff = request
	s.ready = true
	return nil
}

func (s *barrierSwitch) stopGuardian(parent context.Context) error {
	if !s.ready {
		return errors.New("the barrier is not up")
	}
	ctx, cancel := context.WithTimeout(parent, barrierSwitchStepTimeout+coreGoneTimeout)
	defer cancel()
	if err := install.BootoutGuardian(ctx); err != nil {
		return fmt.Errorf("stopping the old Guardian: %w", err)
	}
	// 旧 plist:launchd 刚把 Core 连同 Guardian 一起收掉(它正在还原路由,屏障在)。
	// 新 plist(AbandonProcessGroup):Core 还活着,显式停。两种都等到系统里数不到 Core。
	if err := stopOrphanedCore(ctx); err != nil {
		return fmt.Errorf("stopping the old Core: %w", err)
	}
	if err := waitNoCoreRunning(ctx); err != nil {
		return err
	}
	// 旧 Core 退出时删掉了它自己装的那条服务器 /32;新 Core 的隧道要经它出去。
	if err := guardian.ReassertHandoffBypass(ctx, s.handoff); err != nil {
		return fmt.Errorf("restoring the route to the server: %w", err)
	}
	return nil
}

func waitNoCoreRunning(ctx context.Context) error {
	runner := guardian.NewExecCoreRunner(install.GuardianExecutable(), defaultConfigPath, darwinDNSListen)
	waitCtx, cancel := context.WithTimeout(ctx, coreGoneTimeout)
	defer cancel()
	for {
		cores, err := runner.ScanRunning()
		if err == nil && len(cores) == 0 {
			return nil
		}
		select {
		case <-waitCtx.Done():
			if err != nil {
				return fmt.Errorf("could not confirm the old Core has exited: %w", err)
			}
			return fmt.Errorf("a bx Core process (PID %d) is still running after the old Guardian stopped", cores[0].PID)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (s *barrierSwitch) startGuardian(parent context.Context) error {
	if err := install.EnableGuardian(); err != nil {
		return fmt.Errorf("starting the new Guardian: %w", err)
	}
	if !waitGuardianSocket(parent, guardian.SocketPath, guardianReadyTimeout, guardianReadyPollInterval) {
		return errors.New("the new Guardian did not start answering on its socket")
	}
	return nil
}

func (s *barrierSwitch) handOver(parent context.Context) error {
	if !s.ready {
		return errors.New("the barrier is not up")
	}
	client := guardian.NewClientWithTimeout(guardian.SocketPath, guardianMutationClientTimeout)
	status, err := client.Migrate(parent, s.handoff)
	if err != nil {
		return fmt.Errorf("handing the barrier over to the new Guardian: %w", err)
	}
	if status.Protection != guardian.ProtectionProtected {
		return fmt.Errorf("the new Guardian took over but reports protection %q", status.Protection)
	}
	if !slices.Contains(status.Capabilities, guardian.CapabilityCoreOutlivesGuardian) {
		// 不是失败(保护在),但下一次升级又得走屏障这条路 —— 说出来。
		fmt.Println("! the new Guardian does not report that Core outlives it; the next upgrade will switch behind the barrier again")
	}
	return nil
}
