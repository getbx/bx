package guardian

import "github.com/getbx/bx/internal/doctor"

// DoctorGuardianFact 把 Guardian 的状态折成 doctor 判据要的两个事实。纯转换、逐字段。
// 住在 guardian 而不是 doctor:doctor 不能 import guardian(会成环),而 cli 与
// guardian 自己的 /v1/doctor 采集都要这一份 —— 两份拷贝就是漂移的起点。
func DoctorGuardianFact(st Status) doctor.GuardianFact {
	return doctor.GuardianFact{
		DNS:      doctor.DNSFact{State: string(st.DNSState), Managed: st.DNSManaged, Service: st.DNSService},
		Recovery: doctor.RecoveryFact{State: st.Recovery.State, Stage: st.Recovery.Stage, Attempt: st.Recovery.Attempt, ErrorCode: st.Recovery.ErrorCode},
	}
}
