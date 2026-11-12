package remoteworker_test

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	"bonanza.build/pkg/encryptedaction"
	encryptedaction_pb "bonanza.build/pkg/proto/encryptedaction"
	remoteworker_pb "bonanza.build/pkg/proto/remoteworker"
	"bonanza.build/pkg/remoteworker"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/jmespath"
	"github.com/buildbarn/bb-storage/pkg/random"
	"github.com/buildbarn/bb-storage/pkg/testutil"
	bb_x509 "github.com/buildbarn/bb-storage/pkg/x509"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeScheduler is an implementation of OperationQueueClient that
// records the requests sent by the worker, and replies with responses
// that are provided by the test.
type fakeScheduler struct {
	requests  []*remoteworker_pb.SynchronizeRequest
	responses []*remoteworker_pb.SynchronizeResponse
}

func (s *fakeScheduler) Synchronize(ctx context.Context, in *remoteworker_pb.SynchronizeRequest, opts ...grpc.CallOption) (*remoteworker_pb.SynchronizeResponse, error) {
	// The worker reuses the request message across calls. Record a
	// snapshot of the current state of the worker.
	s.requests = append(s.requests, &remoteworker_pb.SynchronizeRequest{
		CurrentState: &remoteworker_pb.CurrentState{
			WorkerState: in.CurrentState.WorkerState,
		},
	})
	if len(s.responses) == 0 {
		return nil, status.Error(codes.Internal, "No more responses")
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	return response, nil
}

// fakeExecutor is an implementation of Executor that calls into a
// function that is provided by the test, depending on the action.
type fakeExecutor struct {
	execute map[string]func(ctx context.Context) ([]byte, remoteworker_pb.CurrentState_Completed_Result, error)
}

func (fakeExecutor) CheckReadiness(ctx context.Context) error {
	return nil
}

func (e fakeExecutor) Execute(ctx context.Context, action []byte, executionTimeout time.Duration, executionEvents chan<- []byte) ([]byte, time.Duration, remoteworker_pb.CurrentState_Completed_Result, error) {
	result, resultCode, err := e.execute[string(action)](ctx)
	return result, 0, resultCode, err
}

// clientIdentity contains the keys and certificates that a client of
// the scheduler uses to send actions to the worker.
type clientIdentity struct {
	certificate *x509.Certificate
	privateKey  *ecdh.PrivateKey
	verifier    *bb_x509.ClientCertificateVerifier
}

func newClientIdentity(t *testing.T) clientIdentity {
	caPrivateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caCertificateBytes, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caPrivateKey.PublicKey, caPrivateKey)
	require.NoError(t, err)
	caCertificate, err := x509.ParseCertificate(caCertificateBytes)
	require.NoError(t, err)

	clientPrivateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	clientCertificateBytes, err := x509.CreateCertificate(
		rand.Reader,
		&x509.Certificate{
			SerialNumber: big.NewInt(2),
			Subject:      pkix.Name{CommonName: "Client"},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		},
		caCertificate,
		&clientPrivateKey.PublicKey,
		caPrivateKey,
	)
	require.NoError(t, err)
	clientCertificate, err := x509.ParseCertificate(clientCertificateBytes)
	require.NoError(t, err)
	clientECDHPrivateKey, err := clientPrivateKey.ECDH()
	require.NoError(t, err)

	clientCAs := x509.NewCertPool()
	clientCAs.AddCert(caCertificate)
	return clientIdentity{
		certificate: clientCertificate,
		privateKey:  clientECDHPrivateKey,
		verifier: bb_x509.NewClientCertificateVerifier(
			clientCAs,
			clock.SystemClock,
			jmespath.MustCompile("`true`"),
			jmespath.MustCompile("`{}`"),
		),
	}
}

// newDesiredStateExecuting creates a desired state that instructs the
// worker to execute an action with a given plaintext payload.
func newDesiredStateExecuting(t *testing.T, identity clientIdentity, workerPrivateKey *ecdh.PrivateKey, taskUUID, plaintext string) *remoteworker_pb.DesiredState {
	platformPKIXPublicKey, err := x509.MarshalPKIXPublicKey(workerPrivateKey.PublicKey())
	require.NoError(t, err)
	sharedSecret, err := identity.privateKey.ECDH(workerPrivateKey.PublicKey())
	require.NoError(t, err)

	action := &encryptedaction_pb.Action{
		PlatformPkixPublicKey:  platformPKIXPublicKey,
		ClientCertificateChain: [][]byte{identity.certificate.Raw},
		AdditionalData: &encryptedaction_pb.Action_AdditionalData{
			StableFingerprint: []byte(taskUUID),
			ExecutionTimeout:  &durationpb.Duration{Seconds: 60},
		},
	}
	require.NoError(t, encryptedaction.ActionSetCiphertext(action, sharedSecret, []byte(plaintext)))
	return &remoteworker_pb.DesiredState{
		WorkerState: &remoteworker_pb.DesiredState_Executing_{
			Executing: &remoteworker_pb.DesiredState_Executing{
				TaskUuid:                  taskUUID,
				Action:                    action,
				EffectiveExecutionTimeout: &durationpb.Duration{Seconds: 60},
			},
		},
	}
}

func TestClient(t *testing.T) {
	ctx := context.Background()

	t.Run("StoppedExecutionDoesNotAffectNextTask", func(t *testing.T) {
		// If the scheduler instructs the worker to stop executing
		// a task in favor of another one, the first task is
		// cancelled. The error it returns due to the
		// cancellation must be discarded. It must not cause the
		// next task to be reported as rejected.
		identity := newClientIdentity(t)
		workerPrivateKey, err := ecdh.P256().GenerateKey(rand.Reader)
		require.NoError(t, err)

		scheduler := &fakeScheduler{
			responses: []*remoteworker_pb.SynchronizeResponse{
				{
					NextSynchronizationAt: timestamppb.Now(),
					DesiredState:          newDesiredStateExecuting(t, identity, workerPrivateKey, "task-a", "action-a"),
				},
				{
					// Use a synchronization time in the
					// future, so that the next call to
					// Run() waits for task B to complete.
					NextSynchronizationAt: timestamppb.New(time.Now().Add(time.Hour)),
					DesiredState:          newDesiredStateExecuting(t, identity, workerPrivateKey, "task-b", "action-b"),
				},
				{
					NextSynchronizationAt: timestamppb.Now(),
				},
			},
		}
		executor := fakeExecutor{
			execute: map[string]func(ctx context.Context) ([]byte, remoteworker_pb.CurrentState_Completed_Result, error){
				"action-a": func(ctx context.Context) ([]byte, remoteworker_pb.CurrentState_Completed_Result, error) {
					// Block until the worker cancels us,
					// similar to an evaluation waiting for
					// its result to be written to storage.
					<-ctx.Done()
					return nil, 0, status.Error(codes.Canceled, "Failed to export result: context canceled")
				},
				"action-b": func(ctx context.Context) ([]byte, remoteworker_pb.CurrentState_Completed_Result, error) {
					return []byte("result-b"), remoteworker_pb.CurrentState_Completed_SUCCEEDED, nil
				},
			},
		}
		client, err := remoteworker.NewClient(
			scheduler,
			executor,
			clock.SystemClock,
			random.CryptoThreadSafeGenerator,
			[]*ecdh.PrivateKey{workerPrivateKey},
			identity.verifier,
			map[string]string{"hostname": "worker"},
			/* sizeClass = */ 0,
			/* isLargestSizeClass = */ true,
		)
		require.NoError(t, err)

		// Pick up task A, then switch over to task B. The third
		// call reports the outcome of task B.
		for range 3 {
			_, err := client.Run(ctx)
			require.NoError(t, err)
		}

		require.Len(t, scheduler.requests, 3)
		completed, ok := scheduler.requests[2].CurrentState.WorkerState.(*remoteworker_pb.CurrentState_Completed_)
		if !ok {
			if rejected, ok := scheduler.requests[2].CurrentState.WorkerState.(*remoteworker_pb.CurrentState_Rejected_); ok {
				t.Fatalf("Task %#v was rejected: %s", rejected.Rejected.TaskUuid, status.ErrorProto(rejected.Rejected.Reason))
			}
			t.Fatalf("Worker did not report completion, but %T", scheduler.requests[2].CurrentState.WorkerState)
		}
		require.Equal(t, "task-b", completed.Completed.TaskUuid)
		require.Equal(t, remoteworker_pb.CurrentState_Completed_SUCCEEDED, completed.Completed.Result)
		require.NotNil(t, completed.Completed.Event)
	})

	t.Run("RejectionIsReportedForCorrectTask", func(t *testing.T) {
		// Errors returned by the executor should still cause
		// the task to be rejected.
		identity := newClientIdentity(t)
		workerPrivateKey, err := ecdh.P256().GenerateKey(rand.Reader)
		require.NoError(t, err)

		scheduler := &fakeScheduler{
			responses: []*remoteworker_pb.SynchronizeResponse{
				{
					NextSynchronizationAt: timestamppb.New(time.Now().Add(time.Hour)),
					DesiredState:          newDesiredStateExecuting(t, identity, workerPrivateKey, "task-a", "action-a"),
				},
				{
					NextSynchronizationAt: timestamppb.Now(),
				},
			},
		}
		executor := fakeExecutor{
			execute: map[string]func(ctx context.Context) ([]byte, remoteworker_pb.CurrentState_Completed_Result, error){
				"action-a": func(ctx context.Context) ([]byte, remoteworker_pb.CurrentState_Completed_Result, error) {
					return nil, 0, status.Error(codes.Internal, "Storage is offline")
				},
			},
		}
		client, err := remoteworker.NewClient(
			scheduler,
			executor,
			clock.SystemClock,
			random.CryptoThreadSafeGenerator,
			[]*ecdh.PrivateKey{workerPrivateKey},
			identity.verifier,
			map[string]string{"hostname": "worker"},
			/* sizeClass = */ 0,
			/* isLargestSizeClass = */ true,
		)
		require.NoError(t, err)

		for range 2 {
			_, err := client.Run(ctx)
			require.NoError(t, err)
		}

		require.Len(t, scheduler.requests, 2)
		rejected, ok := scheduler.requests[1].CurrentState.WorkerState.(*remoteworker_pb.CurrentState_Rejected_)
		require.True(t, ok, "Worker did not report rejection, but %T", scheduler.requests[1].CurrentState.WorkerState)
		require.Equal(t, "task-a", rejected.Rejected.TaskUuid)
		testutil.RequireEqualStatus(t, status.Error(codes.Internal, "Storage is offline"), status.ErrorProto(rejected.Rejected.Reason))
	})
}
