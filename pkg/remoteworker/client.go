package remoteworker

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/ecdh"
	"crypto/x509"
	"encoding/base64"
	"log"
	"sort"
	"sync/atomic"
	"time"

	"bonanza.build/pkg/crypto"
	"bonanza.build/pkg/ds"
	"bonanza.build/pkg/encryptedaction"
	encryptedaction_pb "bonanza.build/pkg/proto/encryptedaction"
	remoteworker_pb "bonanza.build/pkg/proto/remoteworker"

	"github.com/buildbarn/bb-storage/pkg/auth"
	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/otel"
	"github.com/buildbarn/bb-storage/pkg/program"
	"github.com/buildbarn/bb-storage/pkg/random"
	"github.com/buildbarn/bb-storage/pkg/util"
	bb_x509 "github.com/buildbarn/bb-storage/pkg/x509"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Client for the Remote Worker protocol. It can send synchronization
// requests to a scheduler, informing it of the current state of the
// worker, while also obtaining requests for executing actions.
type Client struct {
	// Constant fields.
	scheduler                 remoteworker_pb.OperationQueueClient
	executor                  Executor[[]byte, []byte, []byte]
	clock                     clock.Clock
	randomNumberGenerator     random.ThreadSafeGenerator
	privateKeys               []*ecdh.PrivateKey
	clientCertificateVerifier *bb_x509.ClientCertificateVerifier
	isLargestSizeClass        bool

	// Mutable fields that are always set.
	request                         remoteworker_pb.SynchronizeRequest
	schedulerMayThinkExecutingUntil *time.Time
	nextSynchronizationAt           time.Time

	// Mutable fields that are only set when executing an action.
	executionCancellation              func()
	executionCompleted                 <-chan struct{}
	latestExecutionEvent               atomic.Pointer[[]byte]
	completionRejectionError           error
	completionEvent                    *[]byte
	completionVirtualExecutionDuration time.Duration
	completionResult                   remoteworker_pb.CurrentState_Completed_Result
	eventEncoder                       *encryptedaction.EventEncoder
}

type clientKey struct {
	privateKey    *ecdh.PrivateKey
	pkixPublicKey []byte
}

type clientKeyList struct {
	ds.Slice[clientKey]
}

func (l clientKeyList) Less(i, j int) bool {
	return bytes.Compare(l.Slice[i].pkixPublicKey, l.Slice[j].pkixPublicKey) < 0
}

// NewClient creates a new Client instance that is set to the initial
// state (i.e., being idle).
func NewClient(
	scheduler remoteworker_pb.OperationQueueClient,
	executor Executor[[]byte, []byte, []byte],
	clock clock.Clock,
	randomNumberGenerator random.ThreadSafeGenerator,
	unsortedPrivateKeys []*ecdh.PrivateKey,
	clientCertificateVerifier *bb_x509.ClientCertificateVerifier,
	workerID map[string]string,
	sizeClass uint32,
	isLargestSizeClass bool,
) (*Client, error) {
	// Public keys need to be sent to the scheduler in sorted order.
	// Also sort our private keys the same way, so that we can
	// easily look up the private key corresponding to a public key
	// in an action.
	keysToSort := clientKeyList{
		Slice: make(ds.Slice[clientKey], 0, len(unsortedPrivateKeys)),
	}
	for i, privateKey := range unsortedPrivateKeys {
		pkixPublicKey, err := x509.MarshalPKIXPublicKey(privateKey.PublicKey())
		if err != nil {
			return nil, util.StatusWrapfWithCode(err, codes.InvalidArgument, "Invalid private key at index %d", i)
		}
		keysToSort.Push(clientKey{
			privateKey:    privateKey,
			pkixPublicKey: pkixPublicKey,
		})
	}
	sort.Sort(keysToSort)

	sortedPrivateKeys := make([]*ecdh.PrivateKey, 0, len(keysToSort.Slice))
	publicKeys := make([]*remoteworker_pb.SynchronizeRequest_PublicKey, 0, len(keysToSort.Slice))
	for _, key := range keysToSort.Slice {
		sortedPrivateKeys = append(sortedPrivateKeys, key.privateKey)
		publicKeys = append(publicKeys, &remoteworker_pb.SynchronizeRequest_PublicKey{
			PkixPublicKey: key.pkixPublicKey,
		})
	}

	return &Client{
		scheduler:                 scheduler,
		executor:                  executor,
		clock:                     clock,
		randomNumberGenerator:     randomNumberGenerator,
		privateKeys:               sortedPrivateKeys,
		clientCertificateVerifier: clientCertificateVerifier,
		isLargestSizeClass:        isLargestSizeClass,

		request: remoteworker_pb.SynchronizeRequest{
			WorkerId:   workerID,
			PublicKeys: publicKeys,
			SizeClass:  sizeClass,
			CurrentState: &remoteworker_pb.CurrentState{
				WorkerState: &remoteworker_pb.CurrentState_Idle{
					Idle: &emptypb.Empty{},
				},
			},
		},
		nextSynchronizationAt: clock.Now(),
	}, nil
}

func (c *Client) startExecution(desiredState *remoteworker_pb.DesiredState_Executing) error {
	c.stopExecution()

	action := desiredState.Action
	if action == nil {
		return status.Error(codes.InvalidArgument, "No action provided")
	}

	// Determine which worker key is used.
	workerKeyIndex, ok := sort.Find(len(c.request.PublicKeys), func(i int) int {
		return bytes.Compare(action.PlatformPkixPublicKey, c.request.PublicKeys[i].PkixPublicKey)
	})
	if !ok {
		return status.Errorf(codes.InvalidArgument, "Worker received action for platform with PKIX public key %s, even though it does not announce this key", base64.StdEncoding.EncodeToString(action.PlatformPkixPublicKey))
	}

	// Validate the provided client certificate.
	clientCertificateChain := make([]*x509.Certificate, 0, len(action.ClientCertificateChain))
	for i, rawCertificate := range action.ClientCertificateChain {
		certificate, err := x509.ParseCertificate(rawCertificate)
		if err != nil {
			return util.StatusWrapfWithCode(err, codes.InvalidArgument, "Invalid certificate at index %d of client certificate chain", i)
		}
		clientCertificateChain = append(clientCertificateChain, certificate)
	}
	authenticationMetadata, err := c.clientCertificateVerifier.VerifyClientCertificate(clientCertificateChain)
	if err != nil {
		return util.StatusWrap(err, "Failed to validate client certificate")
	}

	// Obtain the shared secret used to decrypt actions and encrypt
	// execution events.
	clientECDHPublicKey, err := crypto.PublicKeyToECDHPublicKey(clientCertificateChain[0].PublicKey)
	if err != nil {
		return err
	}
	sharedSecret, err := c.privateKeys[workerKeyIndex].ECDH(clientECDHPublicKey)
	if err != nil {
		return util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to compute shared secret")
	}

	// Decrypt and unmarshal the action payload.
	plaintextAction, err := encryptedaction.ActionGetPlaintext(action, sharedSecret)
	if err != nil {
		return util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to decrypt action")
	}

	// Validate that the execution timeout provided by the scheduler.
	originalExecutionTimeoutMessage := action.AdditionalData.ExecutionTimeout
	if err := originalExecutionTimeoutMessage.CheckValid(); err != nil {
		return util.StatusWrapWithCode(err, codes.InvalidArgument, "Invalid original execution timeout")
	}
	originalExecutionTimeout := originalExecutionTimeoutMessage.AsDuration()
	if err := desiredState.EffectiveExecutionTimeout.CheckValid(); err != nil {
		return util.StatusWrapWithCode(err, codes.InvalidArgument, "Invalid effective execution timeout")
	}
	effectiveExecutionTimeout := desiredState.EffectiveExecutionTimeout.AsDuration()
	if effectiveExecutionTimeout < 0 || effectiveExecutionTimeout > originalExecutionTimeout {
		return status.Errorf(
			codes.InvalidArgument,
			"Execution timeout of %s is outside permitted range [0s, %s]",
			effectiveExecutionTimeout,
			originalExecutionTimeout,
		)
	}

	// Launch one goroutine that captures any execution events
	// generated by the executor.
	executionEvents := make(chan []byte, 1)
	storedAllExecutionEvents := make(chan struct{})
	go func() {
		for executionEvent := range executionEvents {
			c.latestExecutionEvent.Store(&executionEvent)
		}
		close(storedAllExecutionEvents)
	}()

	// Launch another goroutine that invokes the executor.
	ctx, executionCancellation := context.WithCancel(
		otel.NewContextWithW3CTraceContext(
			auth.NewContextWithAuthenticationMetadata(
				context.Background(),
				authenticationMetadata,
			),
			desiredState.W3CTraceContext,
		),
	)
	executionCompleted := make(chan struct{})
	go func() {
		event, virtualExecutionDuration, result, err := c.executor.Execute(ctx, plaintextAction, effectiveExecutionTimeout, executionEvents)

		// Wait for the goroutine above to terminate, so that we
		// can guarantee that c.latestExecutionEvent is no
		// longer updated.
		close(executionEvents)
		<-storedAllExecutionEvents
		c.latestExecutionEvent.Store(nil)

		// Overwrite all completion fields, so that no outcome of
		// a previous execution is reported for this one.
		c.completionEvent = nil
		c.completionRejectionError = err
		if err == nil {
			// Discard the completion event if execution failed and
			// the worker doesn't belong to the largest size class.
			// The scheduler MUST retry execution on the largest
			// size class.
			switch result {
			case remoteworker_pb.CurrentState_Completed_SUCCEEDED:
				c.completionEvent = &event
			case remoteworker_pb.CurrentState_Completed_TIMED_OUT:
				if c.isLargestSizeClass && effectiveExecutionTimeout >= originalExecutionTimeout {
					c.completionEvent = &event
				}
			case remoteworker_pb.CurrentState_Completed_FAILED:
				if c.isLargestSizeClass {
					c.completionEvent = &event
				}
			default:
				panic("executor returned an invalid result")
			}

			// Allow Run() to embed the completion event into the
			// Synchronize() request.
			c.completionVirtualExecutionDuration = virtualExecutionDuration
			c.completionResult = result
		}
		close(executionCompleted)
	}()

	c.executionCancellation = executionCancellation
	c.executionCompleted = executionCompleted
	c.eventEncoder = encryptedaction.NewEventEncoder(action, sharedSecret)

	// Change state to indicate the build has started.
	c.request.CurrentState.WorkerState = &remoteworker_pb.CurrentState_Executing_{
		Executing: &remoteworker_pb.CurrentState_Executing{
			TaskUuid: desiredState.TaskUuid,
		},
	}
	return nil
}

func (c *Client) stopExecution() {
	// Trigger cancellation of the existing build action and wait
	// for it to complete. Discard the results.
	if c.executionCancellation != nil {
		c.executionCancellation()
		<-c.executionCompleted
		c.executionCancellation = nil
	}

	c.request.CurrentState.WorkerState = &remoteworker_pb.CurrentState_Idle{
		Idle: &emptypb.Empty{},
	}
}

// touchSchedulerMayThinkExecuting updates state on whether the
// scheduler may think the worker is currently executing an action. This
// is used to determine whether it is safe to terminate the worker
// gracefully.
func (c *Client) touchSchedulerMayThinkExecuting() {
	// Assume that if we've missed the desired synchronization time
	// provided by the scheduler by more than a minute, the
	// scheduler has purged our state.
	until := c.nextSynchronizationAt.Add(time.Minute)
	c.schedulerMayThinkExecutingUntil = &until
}

// Run an iteration of the Remote Worker client, by performing a single
// synchronization against the scheduler.
func (c *Client) Run(ctx context.Context) (bool, error) {
	// Allow the worker to terminate if the scheduler doesn't think
	// we're executing any action, or if we haven't been able to
	// successfully synchronize for a prolonged amount of time.
	if ctx.Err() != nil && (c.schedulerMayThinkExecutingUntil == nil || c.clock.Now().After(*c.schedulerMayThinkExecutingUntil)) {
		return true, nil
	}

	// If the scheduler isn't assuming we're executing any action
	// right now, perform some readiness checks. This ensures we
	// don't dequeue actions from the scheduler while unhealthy.
	if c.schedulerMayThinkExecutingUntil == nil {
		if err := c.executor.CheckReadiness(ctx); err != nil {
			return true, util.StatusWrap(err, "Worker failed readiness check")
		}
	}

	if workerState, ok := c.request.CurrentState.WorkerState.(*remoteworker_pb.CurrentState_Executing_); ok {
		// Either wait until we've reached the next
		// synchronization time or execution has completed.
		timer, timerChannel := c.clock.NewTimer(c.nextSynchronizationAt.Sub(c.clock.Now()))
		select {
		case <-timerChannel:
		case <-c.executionCompleted:
			timer.Stop()
		}

		select {
		case <-c.executionCompleted:
			if c.completionRejectionError == nil {
				// Action is completed. Transition the current state
				// from executing to completed.
				//
				// TODO: We should omit the event if we're not the
				// largest size class!
				var event *encryptedaction_pb.Event
				if c.completionEvent != nil {
					var err error
					event, err = c.eventEncoder.EncodeEvent(*c.completionEvent, true, c.randomNumberGenerator)
					if err != nil {
						return false, util.StatusWrap(err, "Failed to marshal completion event")
					}
				}
				c.request.CurrentState.WorkerState = &remoteworker_pb.CurrentState_Completed_{
					Completed: &remoteworker_pb.CurrentState_Completed{
						TaskUuid:                 workerState.Executing.TaskUuid,
						Event:                    event,
						VirtualExecutionDuration: durationpb.New(c.completionVirtualExecutionDuration),
						Result:                   c.completionResult,
					},
				}
			} else {
				c.request.CurrentState.WorkerState = &remoteworker_pb.CurrentState_Rejected_{
					Rejected: &remoteworker_pb.CurrentState_Rejected{
						TaskUuid: workerState.Executing.TaskUuid,
						Reason:   status.Convert(c.completionRejectionError).Proto(),
					},
				}
			}
			c.completionRejectionError = nil
			c.completionEvent = nil
		default:
			// Action is still executing. See if there are
			// any execution events that we can send to the
			// scheduler.
			if latestExecutionEvent := c.latestExecutionEvent.Swap(nil); latestExecutionEvent != nil {
				event, err := c.eventEncoder.EncodeEvent(*latestExecutionEvent, false, c.randomNumberGenerator)
				if err != nil {
					return false, util.StatusWrap(err, "Failed to marshal execution event")
				}
				workerState.Executing.Event = event
			}
		}
	}

	// Determine whether we should perform call to Synchronize with
	// prefer_being_able set to false (potentially blocking) or true
	// (non-blocking).
	currentStateIsExecuting := false
	switch workerState := c.request.CurrentState.WorkerState.(type) {
	case *remoteworker_pb.CurrentState_Idle:
		// Even though we are idle, the scheduler may think we
		// are executing. This means we were not able to perform
		// readiness checks. Forcefully switch to idle, so that
		// we can still do this before picking up more work.
		c.request.PreferBeingIdle = c.schedulerMayThinkExecutingUntil != nil
	case *remoteworker_pb.CurrentState_Rejected_:
		c.request.PreferBeingIdle = false
	case *remoteworker_pb.CurrentState_Executing_:
		currentStateIsExecuting = true
		c.request.PreferBeingIdle = false
	case *remoteworker_pb.CurrentState_Completed_:
		// In case execution failed with a serious
		// error, request that the worker gets a brief
		// amount of idle time, so that we can do some
		// health checks prior to picking up more work.
		c.request.PreferBeingIdle = workerState.Completed.Result != remoteworker_pb.CurrentState_Completed_SUCCEEDED
	default:
		panic("Unknown worker state")
	}

	// If we need to shut down, we should never be performing
	// blocking Synchronize() calls. We should still let the calls
	// go through, so that we can either finish the current action,
	// or properly ensure the scheduler thinks we're in the idle
	// state.
	if ctx.Err() != nil {
		c.request.PreferBeingIdle = true
		ctx = context.Background()
	}

	// Inform scheduler of current worker state, potentially
	// requesting new work. If this fails, we might have lost an
	// execute request sent by the scheduler, so assume the
	// scheduler thinks we may be executing.
	response, err := c.scheduler.Synchronize(ctx, &c.request)
	if c.schedulerMayThinkExecutingUntil == nil {
		c.touchSchedulerMayThinkExecuting()
	}
	if err != nil {
		return false, util.StatusWrap(err, "Failed to synchronize with scheduler")
	}

	// Determine when we should contact the scheduler again in case
	// of no activity.
	nextSynchronizationAt := response.NextSynchronizationAt
	if err := nextSynchronizationAt.CheckValid(); err != nil {
		return false, util.StatusWrap(err, "Scheduler response contained invalid synchronization timestamp")
	}
	c.nextSynchronizationAt = nextSynchronizationAt.AsTime()

	// As Synchronize() succeeded, the next call does not need to
	// include data associated with the current execution event.
	if workerState, ok := c.request.CurrentState.WorkerState.(*remoteworker_pb.CurrentState_Executing_); ok {
		workerState.Executing.Event = nil
	}

	// Apply desired state changes provided by the scheduler.
	if desiredState := response.DesiredState; desiredState != nil {
		switch workerState := desiredState.WorkerState.(type) {
		case *remoteworker_pb.DesiredState_VerifyingPublicKeys_:
			// Scheduler wants us to recompute our
			// verification zeros.
			verificationECDHPublicKeys := map[ecdh.Curve]*ecdh.PublicKey{}
			for i, k := range workerState.VerifyingPublicKeys.VerificationPkixPublicKeys {
				verificationECDHPublicKey, err := crypto.ParsePKIXECDHPublicKey(k)
				if err != nil {
					return false, util.StatusWrapfWithCode(err, codes.Internal, "Scheduler provided an invalid verification PKIX public key at index %d", i)
				}
				verificationECDHPublicKeys[verificationECDHPublicKey.Curve()] = verificationECDHPublicKey
			}

			for i, privateKey := range c.privateKeys {
				verificationECDHPublicKey, ok := verificationECDHPublicKeys[privateKey.Curve()]
				if !ok {
					return false, status.Errorf(codes.Internal, "Scheduler did not provide a verification PKIX public key that has the same curve as PKIX public key %s", base64.StdEncoding.EncodeToString(c.request.PublicKeys[i].PkixPublicKey))
				}
				sharedSecret, err := privateKey.ECDH(verificationECDHPublicKey)
				if err != nil {
					return false, util.StatusWrapfWithCode(err, codes.Internal, "Failed to compute shared secret for PKIX public key %s", base64.StdEncoding.EncodeToString(c.request.PublicKeys[i].PkixPublicKey))
				}
				aesCipher, err := aes.NewCipher(sharedSecret)
				if err != nil {
					return false, util.StatusWrapfWithCode(err, codes.Internal, "Failed to create block cipher for PKIX public key %s", base64.StdEncoding.EncodeToString(c.request.PublicKeys[i].PkixPublicKey))
				}
				verificationZeros := make([]byte, aesCipher.BlockSize())
				aesCipher.Encrypt(verificationZeros, verificationZeros)
				c.request.PublicKeys[i].VerificationZeros = verificationZeros
			}
		case *remoteworker_pb.DesiredState_Idle:
			// Scheduler is forcing us to go back to idle.
			c.stopExecution()
			c.schedulerMayThinkExecutingUntil = nil
			return true, nil
		case *remoteworker_pb.DesiredState_Executing_:
			// Scheduler is requesting us to execute the
			// next action, maybe forcing us to to stop
			// execution of the current build action.
			if err := c.startExecution(workerState.Executing); err != nil {
				c.request.CurrentState.WorkerState = &remoteworker_pb.CurrentState_Rejected_{
					Rejected: &remoteworker_pb.CurrentState_Rejected{
						TaskUuid: workerState.Executing.TaskUuid,
						Reason:   status.Convert(err).Proto(),
					},
				}
			}
			c.touchSchedulerMayThinkExecuting()
			return false, nil
		default:
			return false, status.Error(codes.Internal, "Scheduler provided an unknown desired state")
		}
	}

	// Scheduler has instructed to continue as is.
	if currentStateIsExecuting {
		c.touchSchedulerMayThinkExecuting()
		return false, nil
	}
	c.schedulerMayThinkExecutingUntil = nil
	return true, nil
}

// LaunchWorkerThread launches a single routine that uses a build client
// to repeatedly synchronizes against the scheduler, requesting a task
// to execute.
func LaunchWorkerThread(group program.Group, run func(ctx context.Context) (bool, error), workerName string) {
	group.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
		generator := random.NewFastSingleThreadedGenerator()
		for {
			terminationStartedBeforeRun := ctx.Err() != nil
			if mayTerminate, err := run(ctx); mayTerminate && ctx.Err() != nil {
				log.Printf("Worker %s: terminating", workerName)
				return nil
			} else if err != nil {
				log.Printf("Worker %s: %s", workerName, err)

				// In case of errors, sleep a random amount of
				// time. Allow the sleep to be skipped once when
				// termination is initiated, so that it happens
				// quickly.
				if d := random.Duration(generator, 5*time.Second); terminationStartedBeforeRun {
					time.Sleep(d)
				} else {
					t := time.NewTimer(d)
					select {
					case <-t.C:
					case <-ctx.Done():
						t.Stop()
					}
				}
			}
		}
	})
}

// ParsePlatformPrivateKeys parses a set of ECDH private keys specified
// in a worker's configuration file, so that they can be provided to
// NewClient().
func ParsePlatformPrivateKeys(privateKeys []string) ([]*ecdh.PrivateKey, error) {
	platformPrivateKeys := make([]*ecdh.PrivateKey, 0, len(privateKeys))
	for i, privateKey := range privateKeys {
		platformPrivateKey, err := crypto.ParsePEMWithPKCS8ECDHPrivateKey([]byte(privateKey))
		if err != nil {
			return nil, util.StatusWrapf(err, "Platform private key at index %d", i)
		}
		platformPrivateKeys = append(platformPrivateKeys, platformPrivateKey)
	}
	return platformPrivateKeys, nil
}
