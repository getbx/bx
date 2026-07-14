package mcp

type fakeOps struct {
	caps                 CapabilitiesOut
	status               StatusOut
	diagnose             DiagnoseOut
	inspect              JSONCommandOut
	leakCheck            JSONCommandOut
	observe              JSONCommandOut
	check                CheckOut
	checkIn              CheckIn
	logs                 LogsOut
	calls                []string
	commitErr            error
	rollbackErr          error
	lastSetTransportLink string
	setTransportErr      error
	rehijackCalled       bool
	policyApply          PolicyApplyIn
	policyApplyOut       PolicyApplyOut
	policyApplyErr       error
}

func (f *fakeOps) Capabilities() (CapabilitiesOut, error) { return f.caps, nil }
func (f *fakeOps) Status() (StatusOut, error)             { return f.status, nil }
func (f *fakeOps) Diagnose() (DiagnoseOut, error)         { return f.diagnose, nil }
func (f *fakeOps) Inspect(InspectIn) (JSONCommandOut, error) {
	return f.inspect, nil
}

func (f *fakeOps) LeakCheck(LeakCheckIn) (JSONCommandOut, error) {
	return f.leakCheck, nil
}

func (f *fakeOps) Observe(ObserveIn) (JSONCommandOut, error) {
	return f.observe, nil
}
func (f *fakeOps) Check(in CheckIn) (CheckOut, error) {
	f.calls = append(f.calls, "check")
	f.checkIn = in
	return f.check, nil
}
func (f *fakeOps) Logs(LogsIn) (LogsOut, error) { return f.logs, nil }
func (f *fakeOps) ApplyPolicy(in PolicyApplyIn) (PolicyApplyOut, error) {
	f.calls = append(f.calls, "policy_apply")
	f.policyApply = in
	return f.policyApplyOut, f.policyApplyErr
}
func (f *fakeOps) SetTransport(in SetTransportIn) error {
	f.calls = append(f.calls, "set_transport")
	f.lastSetTransportLink = in.Link
	return f.setTransportErr
}

func (f *fakeOps) Reconnect() error {
	f.calls = append(f.calls, "reconnect")
	return nil
}

func (f *fakeOps) Rehijack() error {
	f.calls = append(f.calls, "rehijack")
	f.rehijackCalled = true
	return nil
}
func (f *fakeOps) Commit() error   { f.calls = append(f.calls, "commit"); return f.commitErr }
func (f *fakeOps) Rollback() error { f.calls = append(f.calls, "rollback"); return f.rollbackErr }
