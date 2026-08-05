package guardian

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

const guardianMutationTimeout = time.Minute

type Controller interface {
	Status() Status
	Up(context.Context) error
	Down(context.Context) error
}

type MigrationController interface {
	Migrate(context.Context, MigrationRequest) error
}

type UpdateController interface {
	Update(context.Context, UpdateRequest) (UpdateResult, error)
}

type PathRecoveryController interface {
	RequestPathRecovery(RecoveryRequest) (RecoverySnapshot, error)
	CurrentPathRecovery() RecoverySnapshot
}

type pathRecoveryStatusController interface {
	currentPathRecoveryStatus() (RecoverySnapshot, string)
}

type LocalAPIOptions struct {
	OwnerUID        uint32
	GuardianVersion string
	RuntimeVersion  func() string
}

type peerCredentialsKey struct{}

type peerCredentials struct {
	uid uint32
	got bool
}

type localAPI struct {
	handler        http.Handler
	mutations      *acceptedMutations
	recoveries     recoveryLifecycle
	pathRecoveries pathRecoveryLifecycle
}

type recoveryLifecycle interface {
	beginRecoveryShutdown()
	waitForRecoveries(context.Context) error
}

type acceptedMutations struct {
	mu        sync.Mutex
	accepting bool
	active    int
	drained   chan struct{}
	closed    bool
}

func withPeerCredentials(ctx context.Context, uid uint32, got bool) context.Context {
	return context.WithValue(ctx, peerCredentialsKey{}, peerCredentials{uid: uid, got: got})
}

func NewLocalAPI(controller Controller, provided ...LocalAPIOptions) http.Handler {
	var options LocalAPIOptions
	if len(provided) != 0 {
		options = provided[0]
	}
	mutations := &acceptedMutations{accepting: true, drained: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeGuardianJSON(w, http.StatusOK, observableStatus(controller, pathRecoveryControllerFor(controller), options))
	})
	mux.HandleFunc("/v1/up", mutationHandler(controller, controller.Up, mutations))
	mux.HandleFunc("/v1/down", mutationHandler(controller, controller.Down, mutations))
	migrationController, _ := controller.(MigrationController)
	mux.HandleFunc("/v1/migrate", migrationHandler(controller, migrationController, mutations))
	updateController, _ := controller.(UpdateController)
	mux.HandleFunc("/v1/update", updateHandler(controller, updateController, mutations))
	pathRecoveryController, _ := controller.(PathRecoveryController)
	mux.HandleFunc("/v1/recoveries", recoveryRequestHandler(controller, pathRecoveryController, options.OwnerUID))
	mux.HandleFunc("/v1/recoveries/current", recoveryCurrentHandler(pathRecoveryController, options.OwnerUID))
	recoveries, _ := controller.(recoveryLifecycle)
	pathRecoveries, _ := controller.(pathRecoveryLifecycle)
	return &localAPI{handler: mux, mutations: mutations, recoveries: recoveries, pathRecoveries: pathRecoveries}
}

func pathRecoveryControllerFor(controller Controller) PathRecoveryController {
	pathRecoveryController, _ := controller.(PathRecoveryController)
	return pathRecoveryController
}

func applyVersionFields(status *Status, options LocalAPIOptions) {
	status.GuardianVersion = options.GuardianVersion
	if options.RuntimeVersion != nil {
		status.RuntimeVersion = options.RuntimeVersion()
	}
}

func observableStatus(controller Controller, recoveries PathRecoveryController, options LocalAPIOptions) Status {
	status := controller.Status()
	status.Recovery = RecoverySnapshot{State: "idle", Stage: "idle"}
	if recoveries == nil {
		applyVersionFields(&status, options)
		return status
	}
	if current, ok := recoveries.(pathRecoveryStatusController); ok {
		status.Recovery, status.NetworkGeneration = current.currentPathRecoveryStatus()
	} else {
		status.Recovery = recoveries.CurrentPathRecovery()
		status.NetworkGeneration = status.Recovery.Generation
	}
	status.Recovery = redactRecoverySnapshot(status.Recovery)
	switch status.Recovery.State {
	case "accepted", "running":
		if status.Desired == DesiredOn && status.Protection != ProtectionNeedsAttention {
			status.Protection = ProtectionRecovering
		}
	case "failed":
		if status.Protection != ProtectionNeedsAttention {
			status.Protection = ProtectionBlocked
		}
	}
	applyVersionFields(&status, options)
	return status
}

func recoveryRequestHandler(controller Controller, recovery PathRecoveryController, ownerUID uint32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if !authorizeRecoveryPeer(r.Context(), ownerUID) {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "recovery requires owner or root peer"})
			return
		}
		if recovery == nil {
			writeGuardianJSON(w, http.StatusNotImplemented, map[string]string{"error": "recovery unavailable"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var request RecoveryRequest
		if err := decoder.Decode(&request); err != nil {
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid recovery metadata"})
			return
		}
		if err := ensureJSONEOF(decoder); err != nil {
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid recovery metadata"})
			return
		}
		normalized, err := ValidateRecoveryRequest(request)
		if err != nil {
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid recovery metadata"})
			return
		}
		before := lastErrorOf(controller)
		snapshot, err := recovery.RequestPathRecovery(normalized)
		if errors.Is(err, errPathRecoveryShuttingDown) {
			writeGuardianJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "guardian is shutting down"})
			return
		}
		if err != nil {
			// 完整错误只进 root-only 的 Guardian 日志;响应只带失败码,
			// 避免把路径/链接/凭据经 socket 外传。
			log.Printf("guardian_recovery_request_failed err=%v", err)
			writeGuardianJSON(w, http.StatusInternalServerError, failureResponseBody(before, lastErrorOf(controller)))
			return
		}
		writeGuardianJSON(w, http.StatusAccepted, redactRecoverySnapshot(snapshot))
	}
}

// lastErrorOf reads controller.Status().LastError, tolerating a nil
// Controller (recoveryRequestHandler may be wired without one in tests that
// only exercise the PathRecoveryController side).
func lastErrorOf(controller Controller) string {
	if controller == nil {
		return ""
	}
	return controller.Status().LastError
}

// failureResponseBody builds the body for a mutation-failure 500 response.
// It only includes "code" when the controller's LastError actually changed
// as a result of this failure (i.e. something on this call path invoked
// needsAttention). Several real failure paths return an error without ever
// touching LastError — acquireMutation timing out on a busy mutation lock,
// recoveryBlocked short-circuiting with errRecoveryIncomplete, or Down's DNS
// restore-failure branch which explicitly clears LastError back to "" before
// returning. In those cases "after" is unchanged (or empty) and the code is
// omitted rather than replaying a stale/unrelated value: a wrong code is
// worse than no code, because it points troubleshooting in the wrong
// direction.
func failureResponseBody(before, after string) map[string]string {
	body := map[string]string{"error": "guardian operation failed"}
	if after != "" && after != before {
		body["code"] = after
	}
	return body
}

func recoveryCurrentHandler(controller PathRecoveryController, ownerUID uint32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if !authorizeRecoveryPeer(r.Context(), ownerUID) {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "recovery requires owner or root peer"})
			return
		}
		if controller == nil {
			writeGuardianJSON(w, http.StatusNotImplemented, map[string]string{"error": "recovery unavailable"})
			return
		}
		writeGuardianJSON(w, http.StatusOK, redactRecoverySnapshot(controller.CurrentPathRecovery()))
	}
}

func authorizeRecoveryPeer(ctx context.Context, ownerUID uint32) bool {
	credentials, _ := ctx.Value(peerCredentialsKey{}).(peerCredentials)
	if !credentials.got {
		return false
	}
	return credentials.uid == 0 || (ownerUID != 0 && credentials.uid == ownerUID)
}

func updateHandler(controller Controller, updater UpdateController, mutations *acceptedMutations) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		credentials, _ := r.Context().Value(peerCredentialsKey{}).(peerCredentials)
		if !credentials.got || credentials.uid != 0 {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "mutation requires root peer"})
			return
		}
		if updater == nil {
			writeGuardianJSON(w, http.StatusNotImplemented, map[string]string{"error": "update unavailable"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var request UpdateRequest
		if err := decoder.Decode(&request); err != nil {
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid update metadata"})
			return
		}
		if err := ensureJSONEOF(decoder); err != nil {
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid update metadata"})
			return
		}
		normalized, err := ValidateUpdateRequest(request)
		if err != nil {
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid update metadata"})
			return
		}
		if !mutations.accept() {
			writeGuardianJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "guardian is shutting down"})
			return
		}
		defer mutations.done()
		mutationCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), guardianMutationTimeout)
		defer cancel()
		before := controller.Status().LastError
		result, err := updater.Update(mutationCtx, normalized)
		if err != nil {
			// 完整错误只进 root-only 的 Guardian 日志;响应只带失败码,
			// 避免把路径/链接/凭据经 socket 外传。
			log.Printf("guardian_mutation_failed err=%v", err)
			writeGuardianJSON(w, http.StatusInternalServerError, failureResponseBody(before, controller.Status().LastError))
			return
		}
		writeGuardianJSON(w, http.StatusOK, result)
	}
}

func migrationHandler(controller Controller, migration MigrationController, mutations *acceptedMutations) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		credentials, _ := r.Context().Value(peerCredentialsKey{}).(peerCredentials)
		if !credentials.got || credentials.uid != 0 {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "mutation requires root peer"})
			return
		}
		if migration == nil {
			writeGuardianJSON(w, http.StatusNotImplemented, map[string]string{"error": "migration unavailable"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var request MigrationRequest
		if err := decoder.Decode(&request); err != nil {
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid migration metadata"})
			return
		}
		if err := ensureJSONEOF(decoder); err != nil {
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid migration metadata"})
			return
		}
		normalized, err := ValidateMigrationRequest(request)
		if err != nil {
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid migration metadata"})
			return
		}
		if !mutations.accept() {
			writeGuardianJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "guardian is shutting down"})
			return
		}
		defer mutations.done()
		mutationCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), guardianMutationTimeout)
		defer cancel()
		if err := migration.Migrate(mutationCtx, normalized); err != nil {
			writeGuardianJSON(w, http.StatusInternalServerError, map[string]string{"error": "guardian operation failed"})
			return
		}
		writeGuardianJSON(w, http.StatusOK, controller.Status())
	}
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return io.ErrUnexpectedEOF
	}
	return err
}

func (a *localAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.handler.ServeHTTP(w, r)
}

func (a *localAPI) beginShutdown() {
	a.mutations.stopAccepting()
}

func (a *localAPI) waitForMutations(ctx context.Context) error {
	return a.mutations.wait(ctx)
}

func (a *localAPI) beginRecoveryShutdown() {
	if a.recoveries != nil {
		a.recoveries.beginRecoveryShutdown()
	}
	if a.pathRecoveries != nil {
		a.pathRecoveries.beginPathRecoveryShutdown()
	}
}

func (a *localAPI) waitForRecoveries(ctx context.Context) error {
	var recoveryErr error
	if a.recoveries != nil {
		recoveryErr = a.recoveries.waitForRecoveries(ctx)
	}
	var pathRecoveryErr error
	if a.pathRecoveries != nil {
		pathRecoveryErr = a.pathRecoveries.waitForPathRecoveries(ctx)
	}
	return errors.Join(recoveryErr, pathRecoveryErr)
}

func mutationHandler(controller Controller, mutate func(context.Context) error, mutations *acceptedMutations) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		credentials, _ := r.Context().Value(peerCredentialsKey{}).(peerCredentials)
		if !credentials.got || credentials.uid != 0 {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "mutation requires root peer"})
			return
		}
		if !mutations.accept() {
			writeGuardianJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "guardian is shutting down"})
			return
		}
		defer mutations.done()
		mutationCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), guardianMutationTimeout)
		defer cancel()
		before := controller.Status().LastError
		if err := mutate(mutationCtx); err != nil {
			// 完整错误只进 root-only 的 Guardian 日志;响应只带失败码,
			// 避免把路径/链接/凭据经 socket 外传。
			log.Printf("guardian_mutation_failed err=%v", err)
			writeGuardianJSON(w, http.StatusInternalServerError, failureResponseBody(before, controller.Status().LastError))
			return
		}
		writeGuardianJSON(w, http.StatusOK, controller.Status())
	}
}

func (m *acceptedMutations) accept() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.accepting {
		return false
	}
	m.active++
	return true
}

func (m *acceptedMutations) done() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active--
	m.closeDrainedLocked()
}

func (m *acceptedMutations) stopAccepting() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accepting = false
	m.closeDrainedLocked()
}

func (m *acceptedMutations) closeDrainedLocked() {
	if !m.accepting && m.active == 0 && !m.closed {
		close(m.drained)
		m.closed = true
	}
}

func (m *acceptedMutations) wait(ctx context.Context) error {
	select {
	case <-m.drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func writeGuardianJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
