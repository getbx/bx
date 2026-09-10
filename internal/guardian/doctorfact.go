package guardian

import "github.com/getbx/bx/internal/doctor"

// DoctorGuardianFact 把 Guardian 的状态折成 doctor 判据要的三样事实。纯转换、逐字段。
// 住在 guardian 而不是 doctor:doctor 不能 import guardian(会成环),而 cli 与
// guardian 自己的 /v1/doctor 采集都要这一份 —— 两份拷贝就是漂移的起点。
func DoctorGuardianFact(st Status) doctor.GuardianFact {
	return doctor.GuardianFact{
		DNS:      doctor.DNSFact{State: string(st.DNSState), Managed: st.DNSManaged, Service: st.DNSService},
		Recovery: doctor.RecoveryFact{State: st.Recovery.State, Stage: st.Recovery.Stage, Attempt: st.Recovery.Attempt, ErrorCode: st.Recovery.ErrorCode},
		// 意图必须一起带过去:同一份「DNS 不归 bx」的事实,在用户要保护时是故障,
		// 在他刚把保护关掉时恰恰是对的(见 doctor.DNSCheck)。
		Desired: string(st.Desired),
	}
}
