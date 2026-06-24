package mcp

type fakeOps struct {
	caps     CapabilitiesOut
	status   StatusOut
	diagnose DiagnoseOut
	logs     LogsOut
	plan     PlanOut
	verify   VerifyOut
	calls    []string
	setupErr error
}

func (f *fakeOps) Capabilities() (CapabilitiesOut, error) { return f.caps, nil }
func (f *fakeOps) Status() (StatusOut, error)             { return f.status, nil }
func (f *fakeOps) Diagnose() (DiagnoseOut, error)         { return f.diagnose, nil }
func (f *fakeOps) Logs(LogsIn) (LogsOut, error)           { return f.logs, nil }
func (f *fakeOps) Plan(PlanIn) (PlanOut, error)           { return f.plan, nil }
func (f *fakeOps) Verify() (VerifyOut, error)             { return f.verify, nil }
func (f *fakeOps) Setup(SetupIn) error                    { f.calls = append(f.calls, "setup"); return f.setupErr }
func (f *fakeOps) SetTransport(SetTransportIn) error      { f.calls = append(f.calls, "set_transport"); return nil }
func (f *fakeOps) RestartTunnel() error                   { f.calls = append(f.calls, "restart"); return nil }
func (f *fakeOps) Rehijack() error                        { f.calls = append(f.calls, "rehijack"); return nil }
