package mcp

type fakeOps struct {
	caps                 CapabilitiesOut
	status               StatusOut
	diagnose             DiagnoseOut
	inspect              JSONCommandOut
	leakCheck            JSONCommandOut
	logs                 LogsOut
	plan                 PlanOut
	verify               VerifyOut
	calls                []string
	setupErr             error
	commitErr            error
	rollbackErr          error
	lastSetTransportLink string
	setTransportErr      error
	rehijackCalled       bool
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
func (f *fakeOps) Logs(LogsIn) (LogsOut, error) { return f.logs, nil }
func (f *fakeOps) Plan(PlanIn) (PlanOut, error) { return f.plan, nil }
func (f *fakeOps) Verify() (VerifyOut, error)   { return f.verify, nil }
func (f *fakeOps) Setup(SetupIn) error          { f.calls = append(f.calls, "setup"); return f.setupErr }
func (f *fakeOps) SetTransport(in SetTransportIn) error {
	f.calls = append(f.calls, "set_transport")
	f.lastSetTransportLink = in.Link
	return f.setTransportErr
}
func (f *fakeOps) RestartTunnel() error {
	f.calls = append(f.calls, "restart")
	return nil
}
func (f *fakeOps) Rehijack() error {
	f.calls = append(f.calls, "rehijack")
	f.rehijackCalled = true
	return nil
}
func (f *fakeOps) Commit() error   { f.calls = append(f.calls, "commit"); return f.commitErr }
func (f *fakeOps) Rollback() error { f.calls = append(f.calls, "rollback"); return f.rollbackErr }
