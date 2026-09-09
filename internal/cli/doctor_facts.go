package cli

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"runtime"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/doctor"
	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/rulereview"
	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/getbx/bx/internal/version"
)

// guardianFactFrom 把 Guardian 的状态折成判据要的两个事实。纯转换,逐字段。
func guardianFactFrom(st guardian.Status) doctor.GuardianFact {
	return doctor.GuardianFact{
		DNS:      doctor.DNSFact{State: string(st.DNSState), Managed: st.DNSManaged, Service: st.DNSService},
		Recovery: doctor.RecoveryFact{State: st.Recovery.State, Stage: st.Recovery.Stage, Attempt: st.Recovery.Attempt, ErrorCode: st.Recovery.ErrorCode},
	}
}

// collectDoctorFacts 是 CLI 这一侧的事实采集:读文件、拨 Core / Guardian、探测、平台检查。
// **这里没有一句判断** —— 判断全在 doctor.Judge。每一步「没采到」都带原因进 Facts。
func collectDoctorFacts(configPath, target string, timeout time.Duration, skipProbe, includePlatformChecks bool) doctor.Facts {
	cfgPath := resolveConfigPath(configPath)
	f := doctor.Facts{Version: version.String(), ConfigPath: cfgPath, Darwin: runtime.GOOS == "darwin"}
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		f.Config.ReadErr = err.Error()
		f.Config.PermissionDenied = errors.Is(err, fs.ErrPermission)
		review, guardianPath, rulesErr := guardianRulesForDoctor()
		f.GuardianRules = doctor.GuardianRulesFact{Review: review, ConfigPath: guardianPath}
		if rulesErr != nil {
			f.GuardianRules.Err = rulesErr.Error()
		}
	} else {
		f.Config.Mode0600 = modeCheck(cfgPath, 0o600)
		cfg, perr := config.Parse(b)
		if perr != nil {
			f.ParseErr = perr.Error()
		} else {
			f.Parsed = cfg
			if cfg.Server != "" {
				if !skipProbe {
					probe := probeCheck(cfg.Server, target, timeout)
					f.Probe = &probe
				}
				review := rulereview.Review(buildRuleReviewInput(cfg, embedded.ChinaDomain(), func() (stats.Report, error) {
					return supervisor.FetchStatusReport(statusSocketPath())
				}))
				f.RuleReview = &review
			}
		}
	}
	f.Service = serviceDoctorChecks(runtime.GOOS, guardianServiceChecks, systemdServiceChecks)
	if err := checkStatusSocket(); err != nil {
		f.StatusSocketErr = err.Error()
	}
	if runtime.GOOS == "darwin" {
		if st, err := readGuardianStatus(); err == nil {
			g := guardianFactFrom(st)
			f.Guardian = &g
		}
	}
	if includePlatformChecks {
		f.Platform = collectPlatformChecks(context.Background())
	}
	return f
}
