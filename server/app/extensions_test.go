package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithRoutesCollectsInRegistrationOrder(t *testing.T) {
	var o options
	WithRoutes(func(RouteContext) {})(&o)
	WithRoutes(func(RouteContext) {})(&o)
	WithBackgroundJob(func(JobContext) {})(&o)
	assert.Len(t, o.routes, 2)
	assert.Len(t, o.jobs, 1)
}

// fakeLoginInterceptor is a no-op LoginInterceptor for the option-wiring test.
type fakeLoginInterceptor struct{}

func (fakeLoginInterceptor) Handle(PostAuthContext, http.ResponseWriter, *http.Request, string, string) bool {
	return false
}

// fakeMemberAdmin is a no-op MemberAdmin for the option-wiring test.
type fakeMemberAdmin struct{}

func (fakeMemberAdmin) ListMembers(context.Context, string) ([]MemberInfo, error) { return nil, nil }
func (fakeMemberAdmin) ListInvites(context.Context, string) ([]InviteInfo, error) { return nil, nil }
func (fakeMemberAdmin) CreateInvite(context.Context, string, string, string, string) (string, error) {
	return "", nil
}
func (fakeMemberAdmin) RevokeInvite(context.Context, string, string) error  { return nil }
func (fakeMemberAdmin) RemoveMember(context.Context, string, string) error  { return nil }
func (fakeMemberAdmin) SeatUsage(context.Context, string) (int, int, error) { return 0, 0, nil }

func TestWithLoginInterceptorAndMemberAdminSetOptions(t *testing.T) {
	var o options
	WithLoginInterceptor(func(*pgxpool.Pool) LoginInterceptor { return fakeLoginInterceptor{} })(&o)
	WithMemberAdmin(func(*pgxpool.Pool) MemberAdmin { return fakeMemberAdmin{} })(&o)
	assert.NotNil(t, o.loginInterceptor, "WithLoginInterceptor must set the option field")
	assert.NotNil(t, o.memberAdmin, "WithMemberAdmin must set the option field")
}

func TestMountExtensionsMountsExtraRoutes(t *testing.T) {
	mux := http.NewServeMux()
	called := false
	mountExtensions(mux, []RouteRegistrar{
		func(rc RouteContext) {
			called = true
			require.NotNil(t, rc.Mux)
			rc.Mux.HandleFunc("GET /ext/ping", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("pong"))
			})
		},
	}, nil, nil, nil, testLogger())

	require.True(t, called, "registrar must be invoked")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ext/ping", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "pong", rec.Body.String())
}

func TestStartBackgroundJobsRunsAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	stopped := make(chan struct{})

	startBackgroundJobs(ctx, []BackgroundJob{
		func(jc JobContext) {
			close(started)
			<-jc.Ctx.Done() // exits only when the shutdown context is cancelled
			close(stopped)
		},
	}, nil, testLogger())

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background job did not start")
	}

	cancel()

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("background job did not observe shutdown cancellation")
	}
}
