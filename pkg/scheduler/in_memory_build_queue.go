package scheduler

import (
	"bytes"
	"container/heap"
	"context"
	"crypto/aes"
	"crypto/ecdh"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"bonanza.build/pkg/crypto"
	buildqueuestate_pb "bonanza.build/pkg/proto/buildqueuestate"
	encryptedaction_pb "bonanza.build/pkg/proto/encryptedaction"
	remoteexecution_pb "bonanza.build/pkg/proto/remoteexecution"
	remoteworker_pb "bonanza.build/pkg/proto/remoteworker"
	"bonanza.build/pkg/scheduler/initialsizeclass"
	scheduler_invocation "bonanza.build/pkg/scheduler/invocation"
	"bonanza.build/pkg/scheduler/routing"

	"github.com/buildbarn/bb-storage/pkg/auth"
	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/otel"
	"github.com/buildbarn/bb-storage/pkg/random"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/prometheus/client_golang/prometheus"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	inMemoryBuildQueuePrometheusMetrics sync.Once

	inMemoryBuildQueueInFlightDeduplicationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "bonanza",
			Subsystem: "scheduler",
			Name:      "in_memory_build_queue_in_flight_deduplications_total",
			Help:      "Number of times an Execute() request of a cacheable action was performed, and whether it was in-flight deduplicated against an existing task.",
		},
		[]string{"pkix_public_key", "size_class", "outcome"})

	inMemoryBuildQueueInvocationsCreatedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "bonanza",
			Subsystem: "scheduler",
			Name:      "in_memory_build_queue_invocations_created_total",
			Help:      "Number of times an invocation object was created by creating a size class queue or scheduling a task through Execute().",
		},
		[]string{"pkix_public_key", "size_class", "depth"})
	inMemoryBuildQueueInvocationsActivatedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "bonanza",
			Subsystem: "scheduler",
			Name:      "in_memory_build_queue_invocations_activated_total",
			Help:      "Number of times an invocation object transitioned from being idle to having queued or executing operations.",
		},
		[]string{"pkix_public_key", "size_class", "depth"})
	inMemoryBuildQueueInvocationsDeactivatedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "bonanza",
			Subsystem: "scheduler",
			Name:      "in_memory_build_queue_invocations_deactivated_total",
			Help:      "Number of times an invocation object transitioned from having queued or executing operations to being idle.",
		},
		[]string{"pkix_public_key", "size_class", "depth"})
	inMemoryBuildQueueInvocationsRemovedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "bonanza",
			Subsystem: "scheduler",
			Name:      "in_memory_build_queue_invocations_removed_total",
			Help:      "Number of times an invocation object was removed.",
		},
		[]string{"pkix_public_key", "size_class", "depth"})

	inMemoryBuildQueueTasksScheduledTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "bonanza",
			Subsystem: "scheduler",
			Name:      "in_memory_build_queue_tasks_scheduled_total",
			Help:      "Number of times tasks were scheduled, either by calling Execute() or through initial size class selection retries.",
		},
		[]string{"pkix_public_key", "size_class", "assignment"})
	inMemoryBuildQueueTasksQueuedDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "bonanza",
			Subsystem: "scheduler",
			Name:      "in_memory_build_queue_tasks_queued_duration_seconds",
			Help:      "Time in seconds that tasks were queued before executing.",
			Buckets:   util.DecimalExponentialBuckets(-3, 6, 2),
		},
		[]string{"pkix_public_key", "size_class"})
	inMemoryBuildQueueTasksExecutingDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "bonanza",
			Subsystem: "scheduler",
			Name:      "in_memory_build_queue_tasks_executing_duration_seconds",
			Help:      "Time in seconds that tasks were executing before completing.",
			Buckets:   util.DecimalExponentialBuckets(-3, 6, 2),
		},
		[]string{"pkix_public_key", "size_class", "result", "grpc_code"})
	inMemoryBuildQueueTasksExecutingRetries = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "bonanza",
			Subsystem: "scheduler",
			Name:      "in_memory_build_queue_tasks_executing_retries",
			Help:      "Number of times that tasks were retried before completing.",
			Buckets:   prometheus.LinearBuckets(0, 1, 11),
		},
		[]string{"pkix_public_key", "size_class", "result", "grpc_code"})
	inMemoryBuildQueueTasksCompletedDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "bonanza",
			Subsystem: "scheduler",
			Name:      "in_memory_build_queue_tasks_completed_duration_seconds",
			Help:      "Time in seconds that tasks were completed before being removed.",
			Buckets:   util.DecimalExponentialBuckets(-3, 6, 2),
		},
		[]string{"pkix_public_key", "size_class"})

	inMemoryBuildQueueWorkersCreatedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "bonanza",
			Subsystem: "scheduler",
			Name:      "in_memory_build_queue_workers_created_total",
			Help:      "Number of workers created by Synchronize().",
		},
		[]string{"pkix_public_key", "size_class"})
	inMemoryBuildQueueWorkersTerminatingTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "bonanza",
			Subsystem: "scheduler",
			Name:      "in_memory_build_queue_workers_terminating_total",
			Help:      "Number of workers that have entered the terminating state.",
		},
		[]string{"pkix_public_key", "size_class"})
	inMemoryBuildQueueWorkersRemovedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "bonanza",
			Subsystem: "scheduler",
			Name:      "in_memory_build_queue_workers_removed_total",
			Help:      "Number of workers removed due to expiration.",
		},
		[]string{"pkix_public_key", "size_class", "state"})

	inMemoryBuildQueueWorkerInvocationStickinessRetained = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "bonanza",
			Subsystem: "scheduler",
			Name:      "in_memory_build_queue_worker_invocation_stickiness_retained",
			Help:      "How many levels of worker invocation stickiness were respected, as configured through worker_invocation_stickiness_limits.",
			Buckets:   prometheus.LinearBuckets(0, 1, 11),
		},
		[]string{"pkix_public_key", "size_class"})
)

// InMemoryBuildQueueConfiguration contains all the tunable settings of
// the InMemoryBuildQueue.
type InMemoryBuildQueueConfiguration struct {
	// ExecutionUpdateInterval specifies how frequently Execute()
	// and WaitExecution() should stream updates for a task to the
	// client.
	ExecutionUpdateInterval time.Duration

	// OperationWithNoWaitersTimeout specifies how long an operation
	// may remain registered without having a single client calling
	// Execute() or WaitExecution() on it.
	OperationWithNoWaitersTimeout time.Duration

	// PlatformQueueWithNoWorkersTimeout specifies how long a
	// platform may remain registered by InMemoryBuildQueue when no
	// Synchronize() calls are received for any workers.
	PlatformQueueWithNoWorkersTimeout time.Duration

	// BusyWorkerSynchronizationInterval specifies how frequently
	// busy workers should be instructed to report their state, even
	// if no changes to their running state occurred.
	BusyWorkerSynchronizationInterval time.Duration

	// GetIdleWorkerSynchronizationInterval returns the maximum
	// amount of time a synchronization performed by a worker
	// against the scheduler may block. Once this amount of time is
	// passed, the worker is instructed to resynchronize, as a form
	// of health checking.
	//
	// Implementations may add jitter to this value to ensure
	// synchronization requests get smeared out over time.
	GetIdleWorkerSynchronizationInterval func() time.Duration

	// WorkerTaskRetryCount specifies how many times a worker may
	// redundantly request that a single task is started. By
	// limiting this, we can prevent a single task from
	// crash-looping a worker indefinitely.
	WorkerTaskRetryCount int

	// WorkerWithNoSynchronizationsTimeout specifies how long a
	// worker may remain registered by InMemoryBuildQueue when no
	// Synchronize() calls are received.
	WorkerWithNoSynchronizationsTimeout time.Duration

	// VerificationPrivateKeyRefreshInterval specifies the interval
	// at which the private key used to obtain the shared secret for
	// computing verification_zeros is refreshed. This ensures that
	// accidental disclosure of verification_zeros does not permit a
	// malicious worker to associate itself with a platform
	// indefinitely.
	VerificationPrivateKeyRefreshInterval time.Duration
}

// InMemoryBuildQueue implements a BuildQueue that can distribute
// requests through the Remote Worker protocol to worker processes. All
// of the state of the build queue (i.e., list of queued execution
// requests and list of workers) is kept in memory.
type InMemoryBuildQueue struct {
	clock                               clock.Clock
	uuidGenerator                       util.UUIDGenerator
	randomNumberGenerator               random.SingleThreadedGenerator
	configuration                       *InMemoryBuildQueueConfiguration
	platformQueueAbsenceHardFailureTime time.Time
	actionRouter                        routing.ActionRouter

	lock           sync.Mutex
	platformQueues map[string]*platformQueue

	// Private key that is used to verify that workers provide
	// public keys for which they actually own the private key.
	verificationPrivateKeyExpiration time.Time
	verificationCurveIndices         map[ecdh.Curve]int
	verificationPrivateKeys          []verificationPrivateKey

	// Bookkeeping for WaitExecution(). This call permits us to
	// re-attach to operations by name. It also allows us to obtain
	// results for historical actions, up to a certain degree.
	operationsNameMap map[string]*operation

	// Map of each task by action. This map is used to deduplicate
	// concurrent requests for the same action.
	inFlightDeduplicationMap map[[sha256.Size]byte]*task

	// Time value that is updated during every mutation of build
	// queue state. This reduces the number of clock accesses, while
	// also making it easier to test this code.
	now time.Time

	// Binary heap containing closures that purge stale workers,
	// platform queues and operations.
	cleanupQueue cleanupQueue

	// TODO: Re-add authorization from Buildbarn!
}

// NewInMemoryBuildQueue creates a new InMemoryBuildQueue that is in the
// initial state. It does not have any queues, workers or queued
// execution requests. All of these are created by sending it RPCs.
func NewInMemoryBuildQueue(clock clock.Clock, uuidGenerator util.UUIDGenerator, randomNumberGenerator random.SingleThreadedGenerator, configuration *InMemoryBuildQueueConfiguration, actionRouter routing.ActionRouter) *InMemoryBuildQueue {
	inMemoryBuildQueuePrometheusMetrics.Do(func() {
		prometheus.MustRegister(inMemoryBuildQueueInFlightDeduplicationsTotal)

		prometheus.MustRegister(inMemoryBuildQueueInvocationsCreatedTotal)
		prometheus.MustRegister(inMemoryBuildQueueInvocationsActivatedTotal)
		prometheus.MustRegister(inMemoryBuildQueueInvocationsDeactivatedTotal)
		prometheus.MustRegister(inMemoryBuildQueueInvocationsRemovedTotal)

		prometheus.MustRegister(inMemoryBuildQueueTasksScheduledTotal)
		prometheus.MustRegister(inMemoryBuildQueueTasksQueuedDurationSeconds)
		prometheus.MustRegister(inMemoryBuildQueueTasksExecutingDurationSeconds)
		prometheus.MustRegister(inMemoryBuildQueueTasksExecutingRetries)
		prometheus.MustRegister(inMemoryBuildQueueTasksCompletedDurationSeconds)

		prometheus.MustRegister(inMemoryBuildQueueWorkersCreatedTotal)
		prometheus.MustRegister(inMemoryBuildQueueWorkersTerminatingTotal)
		prometheus.MustRegister(inMemoryBuildQueueWorkersRemovedTotal)

		prometheus.MustRegister(inMemoryBuildQueueWorkerInvocationStickinessRetained)
	})

	return &InMemoryBuildQueue{
		clock:                               clock,
		randomNumberGenerator:               randomNumberGenerator,
		uuidGenerator:                       uuidGenerator,
		configuration:                       configuration,
		platformQueueAbsenceHardFailureTime: clock.Now().Add(configuration.PlatformQueueWithNoWorkersTimeout),
		actionRouter:                        actionRouter,
		platformQueues:                      map[string]*platformQueue{},
		verificationCurveIndices:            map[ecdh.Curve]int{},
		operationsNameMap:                   map[string]*operation{},
		inFlightDeduplicationMap:            map[[sha256.Size]byte]*task{},
	}
}

var (
	_ remoteworker_pb.OperationQueueServer     = (*InMemoryBuildQueue)(nil)
	_ buildqueuestate_pb.BuildQueueStateServer = (*InMemoryBuildQueue)(nil)
)

// RegisterPredeclaredPlatformQueue adds a platform queue to
// InMemoryBuildQueue that remains present, regardless of whether
// workers appear.
//
// The main purpose of this method is to create platform queues that are
// capable of using multiple size classes, as a maximum size class and
// initialsizeclass.Analyzer can be provided for specifying how
// operations are assigned to size classes.
func (bq *InMemoryBuildQueue) RegisterPredeclaredPlatformQueue(pkixPublicKeys [][]byte, workerInvocationStickinessLimits []time.Duration, maximumQueuedBackgroundLearningOperations int, backgroundLearningOperationPriority int32, sizeClasses []uint32) error {
	// Perform basic validations of arguments.
	if len(pkixPublicKeys) == 0 {
		return status.Error(codes.InvalidArgument, "No public keys provided")
	}
	if !slices.IsSortedFunc(pkixPublicKeys, func(a, b []byte) int { return bytes.Compare(a, b) }) {
		return status.Error(codes.InvalidArgument, "Public keys are not sorted")
	}

	if len(sizeClasses) < 1 {
		return status.Error(codes.InvalidArgument, "No size classes provided")
	}
	if !slices.IsSorted(sizeClasses) {
		return status.Error(codes.InvalidArgument, "Size classes must be provided in sorted order")
	}
	if len(sizeClasses) > 1 && sizeClasses[0] == 0 {
		return status.Error(codes.InvalidArgument, "If multiple size classes are provided, all size classes must be positive")
	}

	bq.enter(bq.clock.Now())
	defer bq.leave()

	// Perform validation that requires looking at the current state
	// of the scheduler.
	newPublicKeys := make([]newPublicKey, 0, len(pkixPublicKeys))
	for i, pkixPublicKey := range pkixPublicKeys {
		if _, ok := bq.platformQueues[string(pkixPublicKey)]; ok {
			return status.Errorf(codes.InvalidArgument, "PKIX public key at index %d is already associated with different platform queue", i)
		}
		publicKey, err := bq.getPlatformQueuePublicKey(pkixPublicKey)
		if err != nil {
			return util.StatusWrapfWithCode(err, codes.InvalidArgument, "Invalid PKIX public key at index %d", i)
		}
		newPublicKeys = append(newPublicKeys, newPublicKey{
			publicKey: publicKey,
		})
	}

	// Create the platform queue and size class queues.
	pq := newPlatformQueue(workerInvocationStickinessLimits, maximumQueuedBackgroundLearningOperations, backgroundLearningOperationPriority)
	pq.addNewPublicKeys(bq, newPublicKeys)
	for _, sizeClass := range sizeClasses {
		pq.addSizeClassQueue(bq, sizeClass, false)
	}
	return nil
}

// Execute an action by scheduling it in the build queue. This call
// blocks until the action is completed.
func (bq *InMemoryBuildQueue) Execute(in *remoteexecution_pb.ExecuteRequest, out remoteexecution_pb.Execution_ExecuteServer) error {
	ctx := out.Context()
	action := in.Action
	if action == nil {
		return status.Error(codes.InvalidArgument, "No action provided")
	}

	// Forward the client-provided authentication and request
	// metadata, so that the worker logs it.
	auxiliaryMetadata := make([]*anypb.Any, 0, 1)
	if authenticationMetadata, shouldDisplay := auth.AuthenticationMetadataFromContext(ctx).GetPublicProto(); shouldDisplay {
		authenticationMetadataAny, err := anypb.New(authenticationMetadata)
		if err != nil {
			return util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to marshal authentication metadata")
		}
		auxiliaryMetadata = append(auxiliaryMetadata, authenticationMetadataAny)
	}
	w3cTraceContext := otel.W3CTraceContextFromContext(ctx)

	invocationKeys, initialSizeClassSelector, err := bq.actionRouter.RouteAction(ctx, action)
	if err != nil {
		return util.StatusWrap(err, "Failed to route action")
	}

	marshaledAction, err := proto.Marshal(action)
	if err != nil {
		return util.StatusWrap(err, "Failed to marshal action")
	}
	deduplicationKey := sha256.Sum256(marshaledAction)

	bq.enter(bq.clock.Now())
	defer bq.leave()

	if t, ok := bq.inFlightDeduplicationMap[deduplicationKey]; ok {
		// A task for the same action reference already exists
		// against which we may deduplicate. No need to create a
		// task.
		initialSizeClassSelector.Abandoned()
		scq := t.getCurrentSizeClassQueue()
		i := scq.getOrCreateInvocation(bq, invocationKeys)
		if o, ok := t.operations[i]; ok {
			// Task is already associated with the current
			// invocation. Simply wait on the operation that
			// already exists.
			scq.inFlightDeduplicationsSameInvocation.Inc()
			return o.waitExecution(bq, out)
		}

		// Create an additional operation for this task.
		o := t.newOperation(bq, in.Priority, i, false)
		if w := t.currentWorker; w != nil {
			// The request has been deduplicated against a
			// task that is already in the executing stage.
			i.incrementExecutingWorkersCount(bq, w)
		} else {
			// The request has been deduplicated against a
			// task that is still queued.
			o.enqueue()
		}
		scq.inFlightDeduplicationsOtherInvocation.Inc()
		return o.waitExecution(bq, out)
	}

	// We need to create a new task. For that we first need to
	// obtain the size class queue in which we're going to place it.
	platformPkixPublicKey := action.PlatformPkixPublicKey
	pq, ok := bq.platformQueues[string(platformPkixPublicKey)]
	if !ok {
		code := codes.FailedPrecondition
		if bq.now.Before(bq.platformQueueAbsenceHardFailureTime) {
			// The scheduler process started not too long
			// ago. It may be the case that clients ended up
			// connecting to the scheduler before workers
			// got a chance to synchronize.
			//
			// Prevent builds from failing unnecessarily by
			// providing a brief window of time where
			// soft errors are returned to the client,
			// giving workers time to reconnect.
			code = codes.Unavailable
		}
		initialSizeClassSelector.Abandoned()
		return status.Errorf(
			code,
			"No workers exist for PKIX public key %s",
			base64.StdEncoding.EncodeToString(platformPkixPublicKey),
		)
	}
	sizeClassIndex, expectedDuration, timeout, initialSizeClassLearner := initialSizeClassSelector.Select(pq.sizeClasses)
	scq := pq.sizeClassQueues[sizeClassIndex]

	// Create the task.
	t := &task{
		operations:       map[*invocation]*operation{},
		deduplicationKey: deduplicationKey,
		desiredState: remoteworker_pb.DesiredState_Executing{
			TaskUuid:                  util.Must(bq.uuidGenerator()).String(),
			Action:                    action,
			EffectiveExecutionTimeout: durationpb.New(timeout),
			QueuedTimestamp:           bq.getCurrentTime(),
			AuxiliaryMetadata:         auxiliaryMetadata,
			W3CTraceContext:           w3cTraceContext,
		},
		expectedDuration:        expectedDuration,
		initialSizeClassLearner: initialSizeClassLearner,
		stageChangeWakeup:       make(chan struct{}),
	}
	bq.inFlightDeduplicationMap[deduplicationKey] = t
	scq.inFlightDeduplicationsNew.Inc()
	i := scq.getOrCreateInvocation(bq, invocationKeys)
	o := t.newOperation(bq, in.Priority, i, false)
	t.schedule(bq)
	return o.waitExecution(bq, out)
}

// WaitExecution attaches to an existing operation that was created by
// Execute(). This call can be used by the client to reattach to an
// operation in case of network failure.
func (bq *InMemoryBuildQueue) WaitExecution(in *remoteexecution_pb.WaitExecutionRequest, out remoteexecution_pb.Execution_WaitExecutionServer) error {
	bq.enter(bq.clock.Now())
	for {
		o, ok := bq.operationsNameMap[in.Name]
		if !ok {
			bq.leave()
			return status.Errorf(codes.NotFound, "Operation with name %#v not found", in.Name)
		}

		bq.enter(bq.clock.Now())
		if bq.operationsNameMap[in.Name] == o {
			defer bq.leave()
			return o.waitExecution(bq, out)
		}
	}
}

type verificationPrivateKey struct {
	privateKey    *ecdh.PrivateKey
	pkixPublicKey []byte
}

// generateVerificationPrivateKey generates a new elliptic-curve private
// key for the purpose of validating that a worker is in the possession
// of a private key for which it announces a public key.
//
// This method is either called when a worker announces a public key
// having a curve is used that hasn't been observed before, or when the
// keys are periodically rotated.
func (bq *InMemoryBuildQueue) generateVerificationPrivateKey(curve ecdh.Curve) (verificationPrivateKey, error) {
	privateKey, err := curve.GenerateKey(bq.randomNumberGenerator)
	if err != nil {
		return verificationPrivateKey{}, util.StatusWrapWithCode(err, codes.Internal, "Failed to generate new verification private key")
	}
	pkixPublicKey, err := x509.MarshalPKIXPublicKey(privateKey.PublicKey())
	if err != nil {
		return verificationPrivateKey{}, util.StatusWrapWithCode(err, codes.Internal, "Failed to marshal new verification public key")
	}
	return verificationPrivateKey{
		privateKey:    privateKey,
		pkixPublicKey: pkixPublicKey,
	}, nil
}

// parsePKIXPublicKeyAndGetCurveIndex parses a PKIX public key that a
// worker has provided. If the public key uses a curve that hasn't been
// observed before, it ensures that a new verification key for this
// curve is available.
func (bq *InMemoryBuildQueue) parsePKIXPublicKeyAndGetCurveIndex(pkixPublicKey []byte) (*ecdh.PublicKey, int, error) {
	ecdhPublicKey, err := crypto.ParsePKIXECDHPublicKey(pkixPublicKey)
	if err != nil {
		return nil, 0, err
	}

	curve := ecdhPublicKey.Curve()
	curveIndex, ok := bq.verificationCurveIndices[curve]
	if !ok {
		verificationPrivateKey, err := bq.generateVerificationPrivateKey(curve)
		if err != nil {
			return nil, 0, err
		}
		curveIndex = len(bq.verificationPrivateKeys)
		bq.verificationCurveIndices[curve] = curveIndex
		bq.verificationPrivateKeys = append(bq.verificationPrivateKeys, verificationPrivateKey)
	}
	return ecdhPublicKey, curveIndex, nil
}

// platformQueuePublicKey is a public key that is associated with a
// platform queue. This public key is provided by the worker and can be
// used by clients to sign actions.
//
// In addition to storing just the public key, it holds the expected
// value of verification_zeros that a worker should send in order to be
// trusted.
type platformQueuePublicKey struct {
	pkixPublicKey     []byte
	verificationZeros [aes.BlockSize]byte
	curveIndex        int
}

// getPlatformQueuePublicKey converts a user-provided PKIX public key to an
// instance of platformQueuePublicKey.
func (bq *InMemoryBuildQueue) getPlatformQueuePublicKey(pkixPublicKey []byte) (platformQueuePublicKey, error) {
	ecdhPublicKey, curveIndex, err := bq.parsePKIXPublicKeyAndGetCurveIndex(pkixPublicKey)
	if err != nil {
		return platformQueuePublicKey{}, err
	}
	sharedSecret, err := bq.verificationPrivateKeys[curveIndex].privateKey.ECDH(ecdhPublicKey)
	if err != nil {
		return platformQueuePublicKey{}, util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to compute shared secret")
	}
	blockCipher, err := aes.NewCipher(sharedSecret)
	if err != nil {
		return platformQueuePublicKey{}, util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to create AES cipher")
	}
	result := platformQueuePublicKey{
		pkixPublicKey: pkixPublicKey,
		curveIndex:    curveIndex,
	}
	blockCipher.Encrypt(result.verificationZeros[:], result.verificationZeros[:])
	return result, nil
}

// Synchronize the state of a worker with the scheduler. This call is
// used by a worker to report the completion of an operation and to
// request more work.
func (bq *InMemoryBuildQueue) Synchronize(ctx context.Context, request *remoteworker_pb.SynchronizeRequest) (*remoteworker_pb.SynchronizeResponse, error) {
	// Ensure that the list of public keys provided by the worker is
	// non-empty and properly sorted.
	publicKeys := request.PublicKeys
	if len(publicKeys) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Worker provided no public keys")
	}
	if !slices.IsSortedFunc(publicKeys, func(a, b *remoteworker_pb.SynchronizeRequest_PublicKey) int {
		return bytes.Compare(a.PkixPublicKey, b.PkixPublicKey)
	}) {
		return nil, status.Error(codes.InvalidArgument, "Public keys provided by worker are not sorted")
	}

	workerKey := newWorkerKey(request.WorkerId)

	bq.enter(bq.clock.Now())
	defer bq.leave()

	// Rotate the keys used for computing verification zeros if
	// needed. This is done periodically, so that accidental
	// disclosure of verification_zeros does not have a lasting
	// impact.
	if !bq.verificationPrivateKeyExpiration.After(bq.now) {
		newVerificationPrivateKeys := make([]verificationPrivateKey, len(bq.verificationPrivateKeys))
		for curve, index := range bq.verificationCurveIndices {
			verificationPrivateKey, err := bq.generateVerificationPrivateKey(curve)
			if err != nil {
				return nil, err
			}
			newVerificationPrivateKeys[index] = verificationPrivateKey
		}
		bq.verificationPrivateKeys = newVerificationPrivateKeys

		// Update cached verification zeros, so that existing
		// workers are forced to recompute as well.
		for _, pq := range bq.platformQueues {
			for i := 0; i < len(pq.publicKeys); i++ {
				publicKey := &pq.publicKeys[i]
				var err error
				newPublicKey, err := bq.getPlatformQueuePublicKey(publicKey.pkixPublicKey)
				if err != nil {
					return nil, util.StatusWrapWithCode(err, codes.Internal, "Failed to update cached verification zeros")
				}
				*publicKey = newPublicKey
			}
		}

		bq.verificationPrivateKeyExpiration = bq.now.Add(bq.configuration.VerificationPrivateKeyRefreshInterval)
	}

	// Find the platform queue belonging to the provided set of
	// public keys. If multiple public keys are provided, ensure
	// that they all belong to the same platform queue.
	var pq *platformQueue
	var firstPublicKey []byte
	for _, publicKey := range publicKeys {
		if foundPQ, ok := bq.platformQueues[string(publicKey.PkixPublicKey)]; ok {
			if pq == nil {
				pq = foundPQ
				firstPublicKey = publicKey.PkixPublicKey
			} else if foundPQ != pq {
				return nil, status.Errorf(
					codes.InvalidArgument,
					"PKIX public keys %s and %s are already associated with different platform queues",
					base64.StdEncoding.EncodeToString(firstPublicKey),
					base64.StdEncoding.EncodeToString(publicKey.PkixPublicKey),
				)
			}
		}
	}
	var existingPublicKeys []platformQueuePublicKey
	if pq != nil {
		existingPublicKeys = pq.publicKeys
	}

	// Check whether at least all verification zeros are set. These
	// need to be provided, so that the scheduler can validate that
	// the worker is actually in possession of the private keys
	// belonging to the public keys that it announces.
	needsVerification := slices.ContainsFunc(
		publicKeys,
		func(publicKey *remoteworker_pb.SynchronizeRequest_PublicKey) bool {
			return len(publicKey.VerificationZeros) != aes.BlockSize
		},
	)

	// Check whether all verification zeros are correct.
	var newPublicKeys []newPublicKey
	insertionIndex := 0
	curvesSeen := make(map[int]struct{}, len(publicKeys))
	verificationPKIXPublicKeys := make([][]byte, 0, len(publicKeys))
	for index, requestPublicKey := range publicKeys {
		// Determine if this public key is already associated
		// with this platform queue. If not, determine the index
		// at which it needs to be inserted to keep the list of
		// public keys sorted.
		cmp := 1
		for insertionIndex < len(existingPublicKeys) {
			cmp = bytes.Compare(requestPublicKey.PkixPublicKey, existingPublicKeys[insertionIndex].pkixPublicKey[:])
			if cmp >= 0 {
				break
			}
			insertionIndex++
		}

		var curveIndex int
		if cmp == 0 {
			// Platform queue already uses this public key.
			// Require that the client provided verification
			// zeros equal the precomputed value.
			publicKey := &existingPublicKeys[insertionIndex]
			curveIndex = publicKey.curveIndex
			if !needsVerification && subtle.ConstantTimeCompare(requestPublicKey.VerificationZeros, publicKey.verificationZeros[:]) != 1 {
				needsVerification = true
			}
		} else if needsVerification {
			// Platform queue does not use this public key
			// yet, but we already know that this call won't
			// be adding them. At least ensure that a
			// verification key for this kind of curve
			// exists.
			var err error
			_, curveIndex, err = bq.parsePKIXPublicKeyAndGetCurveIndex(requestPublicKey.PkixPublicKey)
			if err != nil {
				return nil, util.StatusWrapfWithCode(err, codes.InvalidArgument, "Invalid PKIX public key at index %d", index)
			}
		} else {
			// Platform queue does not use this public key
			// yet. Compute the verification zeros on the
			// fly and allow the public key to be added if
			// the client provided value matches.
			publicKey, err := bq.getPlatformQueuePublicKey(requestPublicKey.PkixPublicKey)
			if err != nil {
				return nil, util.StatusWrapfWithCode(err, codes.InvalidArgument, "Invalid PKIX public key at index %d", index)
			}
			curveIndex = publicKey.curveIndex
			if subtle.ConstantTimeCompare(requestPublicKey.VerificationZeros, publicKey.verificationZeros[:]) == 1 {
				newPublicKeys = append(newPublicKeys, newPublicKey{
					publicKey:      publicKey,
					insertionIndex: insertionIndex,
				})
			} else {
				needsVerification = true
			}
		}

		if _, ok := curvesSeen[curveIndex]; !ok {
			curvesSeen[curveIndex] = struct{}{}
			verificationPKIXPublicKeys = append(verificationPKIXPublicKeys, bq.verificationPrivateKeys[curveIndex].pkixPublicKey)
		}
	}
	if needsVerification {
		// One or more worker provided verification zeros don't
		// match. Return the verification public keys for all
		// curves used by the worker, so that it can recompute
		// and retry.
		return &remoteworker_pb.SynchronizeResponse{
			NextSynchronizationAt: bq.getCurrentTime(),
			DesiredState: &remoteworker_pb.DesiredState{
				WorkerState: &remoteworker_pb.DesiredState_VerifyingPublicKeys_{
					VerifyingPublicKeys: &remoteworker_pb.DesiredState_VerifyingPublicKeys{
						VerificationPkixPublicKeys: verificationPKIXPublicKeys,
					},
				},
			},
		}, nil
	}

	var scq *sizeClassQueue
	if pq == nil {
		// Workers for this platform have not been observed
		// before. Create a new platform queue containing a
		// single size class queue.
		pq = newPlatformQueue(nil, 0, 0)
	} else if index := sort.Search(
		len(pq.sizeClasses),
		func(i int) bool { return pq.sizeClasses[i] >= request.SizeClass },
	); index < len(pq.sizeClasses) && pq.sizeClasses[index] == request.SizeClass {
		// Found an existing size class queue. Prevent the
		// platform queue from being garbage collected, as it
		// will now have an active worker.
		scq = pq.sizeClassQueues[index]
		if scq.cleanupKey.isActive() {
			bq.cleanupQueue.remove(scq.cleanupKey)
		}
	} else {
		// Worker for this type of public key has been observed
		// before, but not for this size class. Only allow
		// multiple size classes to be created if the platform
		// queue is predeclared, as the build results are
		// non-deterministic otherwise.
		if maximumSizeClassQueue := pq.sizeClassQueues[len(pq.sizeClassQueues)-1]; maximumSizeClassQueue.mayBeRemoved {
			return nil, status.Error(codes.InvalidArgument, "Cannot add multiple size classes to a platform queue that is not predeclared")
		} else if maximumSizeClass := pq.sizeClasses[len(pq.sizeClasses)-1]; request.SizeClass > maximumSizeClass {
			return nil, status.Errorf(codes.InvalidArgument, "Worker provided size class %d, which exceeds the predeclared maximum of %d", request.SizeClass, maximumSizeClass)
		} else if maximumSizeClass > 0 && request.SizeClass < 1 {
			return nil, status.Error(codes.InvalidArgument, "Worker did not provide a size class, even though this platform queue uses them")
		}
	}

	// Platform queue has been created. Attach any new public keys
	// and size classes.
	pq.addNewPublicKeys(bq, newPublicKeys)
	if scq == nil {
		scq = pq.addSizeClassQueue(bq, request.SizeClass, true)
	}

	w, ok := scq.workers[workerKey]
	if ok {
		// Prevent the worker from being garbage collected while
		// synchronization is happening.
		if !w.cleanupKey.isActive() {
			return nil, status.Error(codes.ResourceExhausted, "Worker is already synchronizing with the scheduler")
		}
		bq.cleanupQueue.remove(w.cleanupKey)
	} else {
		// First time we're seeing this worker. As this worker
		// has never run an action before (that we know about),
		// associate it with the root invocation.
		i := &scq.rootInvocation
		w = &worker{
			workerKey:               workerKey,
			lastInvocation:          i,
			listIndex:               -1,
			stickinessStartingTimes: make([]time.Time, len(pq.workerInvocationStickinessLimits)),
		}
		i.idleWorkersCount++
		scq.workers[workerKey] = w
		scq.workersCreatedTotal.Inc()
	}

	// Install cleanup handlers to ensure stale workers and queues
	// are purged after sufficient amount of time.
	defer func() {
		removalTime := bq.now.Add(bq.configuration.WorkerWithNoSynchronizationsTimeout)
		bq.cleanupQueue.add(&w.cleanupKey, removalTime, func() {
			scq.removeStaleWorker(bq, workerKey, removalTime)
		})
	}()

	// Process the current state of the worker to determine what it
	// should be doing next.
	switch workerState := request.CurrentState.GetWorkerState().(type) {
	case *remoteworker_pb.CurrentState_Idle:
		return w.getCurrentOrNextTask(ctx, bq, scq, request.WorkerId, request.PreferBeingIdle)
	case *remoteworker_pb.CurrentState_Rejected_:
		if !w.isRunningCorrectTask(workerState.Rejected.TaskUuid) {
			return w.getCurrentOrNextTask(ctx, bq, scq, request.WorkerId, request.PreferBeingIdle)
		}
		reason := status.ErrorProto(workerState.Rejected.Reason)
		if reason == nil {
			return nil, status.Error(codes.InvalidArgument, "Provided rejection reason is not an error")
		}
		w.currentTask.fail(bq, "RejectedByWorker", util.StatusWrap(reason, "Action rejected by worker"), true)
	case *remoteworker_pb.CurrentState_Executing_:
		if !w.isRunningCorrectTask(workerState.Executing.TaskUuid) {
			// Don't block when obtaining a task, so that we
			// can ask the worker to stop running the
			// current action if there's nothing else to do.
			return w.getCurrentOrNextTask(nil, bq, scq, request.WorkerId, request.PreferBeingIdle)
		}

		// If the worker provided an execution event, record it
		// so that the next ExecuteResponse can send it back to
		// the client.
		if event := workerState.Executing.Event; event != nil {
			t := w.currentTask
			t.lastExecutionEvent = event
			t.executionEventsSeen++
			t.reportNonFinalStageChange()
		}

		// Allow the worker to continue with what it's doing
		// right now.
		return &remoteworker_pb.SynchronizeResponse{
			NextSynchronizationAt: bq.getNextSynchronizationAtDelay(),
		}, nil
	case *remoteworker_pb.CurrentState_Completed_:
		if !w.isRunningCorrectTask(workerState.Completed.TaskUuid) {
			return w.getCurrentOrNextTask(ctx, bq, scq, request.WorkerId, request.PreferBeingIdle)
		}
		w.currentTask.complete(bq, workerState.Completed)
	default:
		return nil, status.Error(codes.InvalidArgument, "Worker provided an unknown current state")
	}
	return w.getNextTask(ctx, bq, scq, request.WorkerId, request.PreferBeingIdle)
}

// ListPlatformQueues returns a list of all platform queues currently
// managed by the scheduler.
func (bq *InMemoryBuildQueue) ListPlatformQueues(ctx context.Context, request *emptypb.Empty) (*buildqueuestate_pb.ListPlatformQueuesResponse, error) {
	bq.enter(bq.clock.Now())
	defer bq.leave()

	// Obtain platform queue IDs in sorted order.
	platformQueueList := platformQueueList(slices.Collect(maps.Values(bq.platformQueues)))
	sort.Sort(platformQueueList)

	// Extract status.
	platformQueues := make([]*buildqueuestate_pb.PlatformQueueState, 0, len(bq.platformQueues))
	for _, pq := range slices.Compact(platformQueueList) {
		pkixPublicKeys := make([][]byte, 0, len(pq.publicKeys))
		for _, publicKey := range pq.publicKeys {
			pkixPublicKeys = append(pkixPublicKeys, publicKey.pkixPublicKey)
		}
		sizeClassQueues := make([]*buildqueuestate_pb.SizeClassQueueState, 0, len(pq.sizeClassQueues))
		for i, scq := range pq.sizeClassQueues {
			sizeClassQueues = append(sizeClassQueues, &buildqueuestate_pb.SizeClassQueueState{
				SizeClass:      pq.sizeClasses[i],
				Timeout:        bq.cleanupQueue.getTimestamp(scq.cleanupKey),
				RootInvocation: scq.rootInvocation.getInvocationState(bq),
				WorkersCount:   uint32(len(scq.workers)),
				DrainsCount:    uint32(len(scq.drains)),
			})
		}
		platformQueues = append(platformQueues, &buildqueuestate_pb.PlatformQueueState{
			PkixPublicKeys:  pkixPublicKeys,
			SizeClassQueues: sizeClassQueues,
		})
	}
	return &buildqueuestate_pb.ListPlatformQueuesResponse{
		PlatformQueues: platformQueues,
	}, nil
}

// GetOperation returns detailed information about a single operation
// identified by name.
func (bq *InMemoryBuildQueue) GetOperation(ctx context.Context, request *buildqueuestate_pb.GetOperationRequest) (*buildqueuestate_pb.GetOperationResponse, error) {
	bq.enter(bq.clock.Now())
	defer bq.leave()

	o, ok := bq.operationsNameMap[request.OperationName]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Operation %#v not found", request.OperationName)
	}
	s := o.getOperationState(bq)
	s.Name = ""
	return &buildqueuestate_pb.GetOperationResponse{
		Operation: s,
	}, nil
}

// getPaginationInfo uses binary searching to determine which
// information should be returned by InMemoryBuildQueue's List*()
// operations.
func getPaginationInfo(n int, pageSize uint32, f func(int) bool) (*buildqueuestate_pb.PaginationInfo, int) {
	startIndex := uint32(sort.Search(n, f))
	endIndex := uint32(n)
	if endIndex-startIndex > pageSize {
		endIndex = startIndex + pageSize
	}
	return &buildqueuestate_pb.PaginationInfo{
		StartIndex:   startIndex,
		TotalEntries: uint32(n),
	}, int(endIndex)
}

// KillOperations requests that one or more operations that are
// currently QUEUED or EXECUTING are moved the COMPLETED stage
// immediately. The next time any worker associated with the operation
// contacts the scheduler, it is requested to stop executing the
// operation.
func (bq *InMemoryBuildQueue) KillOperations(ctx context.Context, request *buildqueuestate_pb.KillOperationsRequest) (*emptypb.Empty, error) {
	failureErr := status.ErrorProto(request.Status)
	if failureErr == nil {
		return nil, status.Error(codes.InvalidArgument, "Provided status is not an error")
	}

	switch filter := request.Filter.GetType().(type) {
	case *buildqueuestate_pb.KillOperationsRequest_Filter_OperationName:
		bq.enter(bq.clock.Now())
		o, ok := bq.operationsNameMap[filter.OperationName]
		if !ok {
			bq.leave()
			return nil, status.Errorf(codes.NotFound, "Operation %#v not found", filter.OperationName)
		}
		o.task.fail(bq, "KilledOperationName", failureErr, false)
		bq.leave()
		return &emptypb.Empty{}, nil
	case *buildqueuestate_pb.KillOperationsRequest_Filter_SizeClassQueueWithoutWorkers:
		bq.enter(bq.clock.Now())
		defer bq.leave()

		scq, err := bq.getSizeClassQueueByName(filter.SizeClassQueueWithoutWorkers)
		if err != nil {
			return nil, err
		}
		if len(scq.workers) > 0 {
			return nil, status.Error(codes.FailedPrecondition, "Cannot kill operations, as size class queue still has workers")
		}
		scq.rootInvocation.failAllQueuedOperations(bq, "KilledSizeClassQueueWithoutWorkers", failureErr)
		return &emptypb.Empty{}, nil
	default:
		return nil, status.Error(codes.InvalidArgument, "Unknown filter provided")
	}
}

// ListOperations returns detailed information about all of the
// operations tracked by the InMemoryBuildQueue.
func (bq *InMemoryBuildQueue) ListOperations(ctx context.Context, request *buildqueuestate_pb.ListOperationsRequest) (*buildqueuestate_pb.ListOperationsResponse, error) {
	var invocationKey *scheduler_invocation.Key
	if request.FilterInvocationId != nil {
		key, err := scheduler_invocation.NewKey(request.FilterInvocationId)
		if err != nil {
			return nil, util.StatusWrap(err, "Invalid invocation key")
		}
		invocationKey = &key
	}

	bq.enter(bq.clock.Now())
	defer bq.leave()

	// Obtain operation names in sorted order.
	nameList := make([]string, 0, len(bq.operationsNameMap))
	for name, o := range bq.operationsNameMap {
		if (invocationKey == nil || o.invocation.hasInvocationKey(*invocationKey)) &&
			(request.FilterStage == buildqueuestate_pb.ListOperationsRequest_ALL || request.FilterStage == o.task.getStage()) {
			nameList = append(nameList, name)
		}
	}
	sort.Strings(nameList)
	paginationInfo, endIndex := getPaginationInfo(len(nameList), request.PageSize, func(i int) bool {
		return request.StartAfter == nil || nameList[i] > request.StartAfter.OperationName
	})

	// Extract status.
	nameListRegion := nameList[paginationInfo.StartIndex:endIndex]
	operations := make([]*buildqueuestate_pb.OperationState, 0, len(nameListRegion))
	for _, name := range nameListRegion {
		o := bq.operationsNameMap[name]
		operations = append(operations, o.getOperationState(bq))
	}
	return &buildqueuestate_pb.ListOperationsResponse{
		Operations:     operations,
		PaginationInfo: paginationInfo,
	}, nil
}

func (bq *InMemoryBuildQueue) getSizeClassQueueByName(name *buildqueuestate_pb.SizeClassQueueName) (*sizeClassQueue, error) {
	pq, ok := bq.platformQueues[string(name.PlatformPkixPublicKey)]
	if !ok {
		return nil, status.Error(codes.NotFound, "Platform queue not found")
	}
	if index := sort.Search(
		len(pq.sizeClasses),
		func(i int) bool { return pq.sizeClasses[i] >= name.SizeClass },
	); index < len(pq.sizeClassQueues) && pq.sizeClasses[index] == name.SizeClass {
		return pq.sizeClassQueues[index], nil
	}
	return nil, status.Error(codes.NotFound, "Size class queue not found")
}

func (bq *InMemoryBuildQueue) getInvocationByName(name *buildqueuestate_pb.InvocationName) (*invocation, *sizeClassQueue, error) {
	if name == nil {
		return nil, nil, status.Error(codes.InvalidArgument, "No invocation name provided")
	}
	scq, err := bq.getSizeClassQueueByName(name.SizeClassQueueName)
	if err != nil {
		return nil, nil, err
	}
	i := &scq.rootInvocation
	for idx, id := range name.Ids {
		key, err := scheduler_invocation.NewKey(id)
		if err != nil {
			return nil, nil, util.StatusWrapf(err, "Invalid invocation key at index %d", idx)
		}
		var ok bool
		i, ok = i.children[key]
		if !ok {
			return nil, nil, status.Error(codes.NotFound, "Invocation not found")
		}
	}
	return i, scq, nil
}

// ListInvocationChildren returns properties of all client invocations
// for which one or more operations are either queued or executing
// within a given platform queue.
//
// When justQueuedInvocations is false, entries for invocations are
// returned even if they have no queued operations; only ones that are
// being executed right now. Entries will be sorted by invocation ID.
//
// When justQueuedInvocations is true, entries for invocations are
// returned only if they have queued operations. Entries will be sorted
// by priority at which operations are scheduled.
func (bq *InMemoryBuildQueue) ListInvocationChildren(ctx context.Context, request *buildqueuestate_pb.ListInvocationChildrenRequest) (*buildqueuestate_pb.ListInvocationChildrenResponse, error) {
	bq.enter(bq.clock.Now())
	defer bq.leave()

	i, _, err := bq.getInvocationByName(request.InvocationName)
	if err != nil {
		return nil, err
	}

	switch request.Filter {
	case buildqueuestate_pb.ListInvocationChildrenRequest_ALL, buildqueuestate_pb.ListInvocationChildrenRequest_ACTIVE:
		// Return all or active invocations in alphabetic order.
		keyList := make([]string, 0, len(i.children))
		for invocationKey, i := range i.children {
			if request.Filter == buildqueuestate_pb.ListInvocationChildrenRequest_ALL || i.isActive() {
				keyList = append(keyList, string(invocationKey))
			}
		}
		sort.Strings(keyList)

		children := make([]*buildqueuestate_pb.InvocationChildState, 0, len(i.children))
		for _, key := range keyList {
			invocationKey := scheduler_invocation.Key(key)
			i := i.children[invocationKey]
			children = append(children, &buildqueuestate_pb.InvocationChildState{
				Id:    invocationKey.GetID(),
				State: i.getInvocationState(bq),
			})
		}
		return &buildqueuestate_pb.ListInvocationChildrenResponse{
			Children: children,
		}, nil
	case buildqueuestate_pb.ListInvocationChildrenRequest_QUEUED:
		// Return invocations with one or more queued
		// operations, sorted by scheduling order.
		children := make([]*buildqueuestate_pb.InvocationChildState, 0, i.queuedChildren.Len())
		sort.Sort(&i.queuedChildren)
		for _, i := range i.queuedChildren {
			children = append(children, &buildqueuestate_pb.InvocationChildState{
				Id:    i.invocationKeys[len(i.invocationKeys)-1].GetID(),
				State: i.getInvocationState(bq),
			})
		}
		return &buildqueuestate_pb.ListInvocationChildrenResponse{
			Children: children,
		}, nil
	default:
		return nil, status.Error(codes.InvalidArgument, "Unknown filter provided")
	}
}

// ListQueuedOperations returns properties of all queued operations
// contained for a given invocation within a platform queue.
func (bq *InMemoryBuildQueue) ListQueuedOperations(ctx context.Context, request *buildqueuestate_pb.ListQueuedOperationsRequest) (*buildqueuestate_pb.ListQueuedOperationsResponse, error) {
	bq.enter(bq.clock.Now())
	defer bq.leave()

	i, _, err := bq.getInvocationByName(request.InvocationName)
	if err != nil {
		return nil, err
	}

	startAfter := request.StartAfter
	var startAfterExpectedDuration time.Duration
	var startAfterQueuedTimestamp time.Time
	if startAfter != nil {
		if err := startAfter.ExpectedDuration.CheckValid(); err != nil {
			return nil, util.StatusWrapWithCode(err, codes.InvalidArgument, "Invalid expected duration")
		}
		startAfterExpectedDuration = startAfter.ExpectedDuration.AsDuration()

		if err := startAfter.QueuedTimestamp.CheckValid(); err != nil {
			return nil, util.StatusWrapWithCode(err, codes.InvalidArgument, "Invalid queued timestamp")
		}
		startAfterQueuedTimestamp = startAfter.QueuedTimestamp.AsTime()
	}

	// As every sorted list is also a valid binary heap, simply sort
	// the queued operations list prior to emitting it.
	sort.Sort(i.queuedOperations)
	paginationInfo, endIndex := getPaginationInfo(i.queuedOperations.Len(), request.PageSize, func(idx int) bool {
		o := i.queuedOperations[idx]
		if startAfter == nil || o.priority > startAfter.Priority {
			return true
		}
		if o.priority < startAfter.Priority {
			return false
		}
		t := o.task
		if t.expectedDuration < startAfterExpectedDuration {
			return true
		}
		if t.expectedDuration > startAfterExpectedDuration {
			return false
		}
		return t.desiredState.QueuedTimestamp.AsTime().After(startAfterQueuedTimestamp)
	})

	queuedOperationsRegion := i.queuedOperations[paginationInfo.StartIndex:endIndex]
	queuedOperations := make([]*buildqueuestate_pb.OperationState, 0, queuedOperationsRegion.Len())
	for _, o := range queuedOperationsRegion {
		s := o.getOperationState(bq)
		s.InvocationName = nil
		queuedOperations = append(queuedOperations, s)
	}
	return &buildqueuestate_pb.ListQueuedOperationsResponse{
		QueuedOperations: queuedOperations,
		PaginationInfo:   paginationInfo,
	}, nil
}

// ListWorkers returns basic properties of all workers for a given
// platform queue.
func (bq *InMemoryBuildQueue) ListWorkers(ctx context.Context, request *buildqueuestate_pb.ListWorkersRequest) (*buildqueuestate_pb.ListWorkersResponse, error) {
	var startAfterWorkerKey *string
	if startAfter := request.StartAfter; startAfter != nil {
		workerKey := string(newWorkerKey(startAfter.WorkerId))
		startAfterWorkerKey = &workerKey
	}

	bq.enter(bq.clock.Now())
	defer bq.leave()

	// Obtain IDs of all workers in sorted order.
	var scq *sizeClassQueue
	var keyList []string
	switch filter := request.Filter.GetType().(type) {
	case *buildqueuestate_pb.ListWorkersRequest_Filter_All:
		var err error
		scq, err = bq.getSizeClassQueueByName(filter.All)
		if err != nil {
			return nil, err
		}
		for workerKey := range scq.workers {
			keyList = append(keyList, string(workerKey))
		}
	case *buildqueuestate_pb.ListWorkersRequest_Filter_Executing:
		var i *invocation
		var err error
		i, scq, err = bq.getInvocationByName(filter.Executing)
		if err != nil {
			return nil, err
		}
		for w := range i.executingWorkers {
			keyList = append(keyList, string(w.workerKey))
		}
	case *buildqueuestate_pb.ListWorkersRequest_Filter_IdleSynchronizing:
		var i *invocation
		var err error
		i, scq, err = bq.getInvocationByName(filter.IdleSynchronizing)
		if err != nil {
			return nil, err
		}
		for _, entry := range i.idleSynchronizingWorkers {
			keyList = append(keyList, string(entry.worker.workerKey))
		}
	default:
		return nil, status.Error(codes.InvalidArgument, "Unknown filter provided")
	}
	sort.Strings(keyList)
	paginationInfo, endIndex := getPaginationInfo(len(keyList), request.PageSize, func(i int) bool {
		return startAfterWorkerKey == nil || keyList[i] > *startAfterWorkerKey
	})

	// Extract status.
	keyListRegion := keyList[paginationInfo.StartIndex:endIndex]
	workers := make([]*buildqueuestate_pb.WorkerState, 0, len(keyListRegion))
	for _, key := range keyListRegion {
		workerKey := workerKey(key)
		w := scq.workers[workerKey]
		var currentOperation *buildqueuestate_pb.OperationState
		if t := w.currentTask; t != nil {
			// A task may have more than one operation
			// associated with it, in case deduplication of
			// in-flight requests occurred. For the time
			// being, let's not expose the concept of tasks
			// through the web UI yet. Just show one of the
			// operations.
			//
			// Do make this deterministic by picking the
			// operation with the lowest name,
			// alphabetically.
			var o *operation
			for _, oCheck := range t.operations {
				if o == nil || o.name > oCheck.name {
					o = oCheck
				}
			}
			currentOperation = o.getOperationState(bq)
			currentOperation.InvocationName = nil
			currentOperation.Stage = nil
		}
		workerID := workerKey.getWorkerID()
		workers = append(workers, &buildqueuestate_pb.WorkerState{
			Id:               workerID,
			Timeout:          bq.cleanupQueue.getTimestamp(w.cleanupKey),
			CurrentOperation: currentOperation,
			Drained:          w.isDrained(scq, workerID),
		})
	}
	return &buildqueuestate_pb.ListWorkersResponse{
		Workers:        workers,
		PaginationInfo: paginationInfo,
	}, nil
}

// ListDrains returns a list of all the drains that are present within a
// given platform queue.
func (bq *InMemoryBuildQueue) ListDrains(ctx context.Context, request *buildqueuestate_pb.ListDrainsRequest) (*buildqueuestate_pb.ListDrainsResponse, error) {
	bq.enter(bq.clock.Now())
	defer bq.leave()

	scq, err := bq.getSizeClassQueueByName(request.SizeClassQueueName)
	if err != nil {
		return nil, err
	}

	// Obtain IDs of all drains in sorted order.
	keyList := make([]string, 0, len(scq.drains))
	for drainKey := range scq.drains {
		keyList = append(keyList, drainKey)
	}
	sort.Strings(keyList)

	// Extract drains.
	drains := make([]*buildqueuestate_pb.DrainState, 0, len(keyList))
	for _, key := range keyList {
		drains = append(drains, scq.drains[key])
	}
	return &buildqueuestate_pb.ListDrainsResponse{
		Drains: drains,
	}, nil
}

func (bq *InMemoryBuildQueue) modifyDrain(ctx context.Context, request *buildqueuestate_pb.AddOrRemoveDrainRequest, modifyFunc func(scq *sizeClassQueue, drainKey string)) (*emptypb.Empty, error) {
	drainKey, err := json.Marshal(request.WorkerIdPattern)
	if err != nil {
		return nil, util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to marshal worker ID pattern")
	}

	bq.enter(bq.clock.Now())
	defer bq.leave()

	scq, err := bq.getSizeClassQueueByName(request.SizeClassQueueName)
	if err != nil {
		return nil, err
	}
	modifyFunc(scq, string(drainKey))
	return &emptypb.Empty{}, nil
}

// AddDrain inserts a new drain into the list of drains currently
// tracked by the platform queue.
func (bq *InMemoryBuildQueue) AddDrain(ctx context.Context, request *buildqueuestate_pb.AddOrRemoveDrainRequest) (*emptypb.Empty, error) {
	return bq.modifyDrain(ctx, request, func(scq *sizeClassQueue, drainKey string) {
		scq.drains[drainKey] = &buildqueuestate_pb.DrainState{
			WorkerIdPattern:  request.WorkerIdPattern,
			CreatedTimestamp: bq.getCurrentTime(),
		}

		// Wake up all synchronizing workers that are queued,
		// but are supposed to be drained. This ensures that
		// they will stop picking up work immediately.
		for workerKey, w := range scq.workers {
			if w.wakeup != nil && workerMatchesPattern(workerKey.getWorkerID(), request.WorkerIdPattern) {
				w.wakeUp(scq)
			}
		}
	})
}

// RemoveDrain removes a drain from the list of drains currently tracked
// by the platform queue.
func (bq *InMemoryBuildQueue) RemoveDrain(ctx context.Context, request *buildqueuestate_pb.AddOrRemoveDrainRequest) (*emptypb.Empty, error) {
	return bq.modifyDrain(ctx, request, func(scq *sizeClassQueue, drainKey string) {
		delete(scq.drains, drainKey)

		// Wake up all synchronizing workers that are drained.
		// This ensures that they pick up work immediately.
		close(scq.undrainWakeup)
		scq.undrainWakeup = make(chan struct{})
	})
}

// TerminateWorkers can be used to indicate that workers are going to be
// terminated in the nearby future. This function will block until any
// operations running on the workers complete, thereby allowing the
// workers to be terminated without interrupting operations.
func (bq *InMemoryBuildQueue) TerminateWorkers(ctx context.Context, request *buildqueuestate_pb.TerminateWorkersRequest) (*emptypb.Empty, error) {
	var completionWakeups []chan struct{}
	bq.enter(bq.clock.Now())
	platformQueues := map[*platformQueue]struct{}{}
	for _, pq := range bq.platformQueues {
		platformQueues[pq] = struct{}{}
	}
	for pq := range platformQueues {
		for _, scq := range pq.sizeClassQueues {
			for workerKey, w := range scq.workers {
				if workerMatchesPattern(workerKey.getWorkerID(), request.WorkerIdPattern) {
					scq.markWorkerTerminating(w)
					if t := w.currentTask; t != nil {
						// The task will be at the
						// EXECUTING stage, so it can
						// only transition to COMPLETED.
						completionWakeups = append(completionWakeups, t.stageChangeWakeup)
					} else if w.wakeup != nil {
						// Wake up the worker, so that
						// it's dequeued. This prevents
						// additional tasks to be
						// assigned to it.
						w.wakeUp(scq)
					}
				}
			}
		}
	}
	bq.leave()

	for _, completionWakeup := range completionWakeups {
		select {
		case <-completionWakeup:
			// Worker has become idle.
		case <-ctx.Done():
			// Client has canceled the request.
			return nil, util.StatusFromContext(ctx)
		}
	}
	return &emptypb.Empty{}, nil
}

// getNextSynchronizationAtDelay generates a timestamp that is attached
// to SynchronizeResponses, indicating that the worker is permitted to
// hold off sending updates for a limited amount of time.
func (bq *InMemoryBuildQueue) getNextSynchronizationAtDelay() *timestamppb.Timestamp {
	return timestamppb.New(bq.now.Add(bq.configuration.BusyWorkerSynchronizationInterval))
}

// getCurrentTime generates a timestamp that corresponds to the current
// time. It is attached to SynchronizeResponses, indicating that the
// worker should resynchronize again as soon as possible. It is also
// used to compute QueuedTimestamps.
func (bq *InMemoryBuildQueue) getCurrentTime() *timestamppb.Timestamp {
	return timestamppb.New(bq.now)
}

// enter acquires the lock on the InMemoryBuildQueue and runs any
// cleanup tasks that should be executed prior to mutating its state.
func (bq *InMemoryBuildQueue) enter(t time.Time) {
	bq.lock.Lock()
	if t.After(bq.now) {
		bq.now = t
		bq.cleanupQueue.run(bq.now)
	}
}

// leave releases the lock on the InMemoryBuildQueue.
func (bq *InMemoryBuildQueue) leave() {
	bq.lock.Unlock()
}

// getIdleSynchronizeResponse returns a synchronization response that
// explicitly instructs a worker to return to the idle state.
func (bq *InMemoryBuildQueue) getIdleSynchronizeResponse() *remoteworker_pb.SynchronizeResponse {
	return &remoteworker_pb.SynchronizeResponse{
		NextSynchronizationAt: bq.getCurrentTime(),
		DesiredState: &remoteworker_pb.DesiredState{
			WorkerState: &remoteworker_pb.DesiredState_Idle{
				Idle: &emptypb.Empty{},
			},
		},
	}
}

// platformQueueList is a list of *platformQueue objects that is
// sortable. It is used by InMemoryBuildQueue.GetBuildQueueState() to
// emit all platform queues in sorted order.
type platformQueueList []*platformQueue

func (h platformQueueList) Len() int {
	return len(h)
}

func (h platformQueueList) Less(i, j int) bool {
	return bytes.Compare(h[i].publicKeys[0].pkixPublicKey, h[j].publicKeys[0].pkixPublicKey) < 0
}

func (h platformQueueList) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

// platformQueue is an actual build operations queue that contains a
// list of associated workers and operations that are queued to be
// executed. An InMemoryBuildQueue contains a platformQueue for every
// set of public keys for which one or more workers exist.
type platformQueue struct {
	publicKeys                                []platformQueuePublicKey
	workerInvocationStickinessLimits          []time.Duration
	maximumQueuedBackgroundLearningOperations int
	backgroundLearningOperationPriority       int32

	sizeClasses     []uint32
	sizeClassQueues []*sizeClassQueue
}

// getSizeClassQueueLabels returns the set of label values to attach to
// Prometheus metrics that pertain to a size class queue.
func (pq *platformQueue) getSizeClassQueueLabels(sizeClass uint32) (string, string) {
	return base64.StdEncoding.EncodeToString(pq.publicKeys[0].pkixPublicKey),
		strconv.FormatUint(uint64(sizeClass), 10)
}

// newPlatformQueue creates a new platform queue.
func newPlatformQueue(workerInvocationStickinessLimits []time.Duration, maximumQueuedBackgroundLearningOperations int, backgroundLearningOperationPriority int32) *platformQueue {
	pq := &platformQueue{
		workerInvocationStickinessLimits:          workerInvocationStickinessLimits,
		maximumQueuedBackgroundLearningOperations: maximumQueuedBackgroundLearningOperations,
		backgroundLearningOperationPriority:       backgroundLearningOperationPriority,
	}
	return pq
}

// newPublicKey keeps track of a platform public key that has been
// observed, but has not yet been added to the scheduler's bookkeeping.
type newPublicKey struct {
	publicKey      platformQueuePublicKey
	insertionIndex int
}

// addNewPublicKeys adds new public keys to the platform. New public
// keys can be observed if a platform queue is first created, or if a
// worker starts to announce a public key that has not been observed
// before. Because we want public keys to be sorted, the caller computed
// the indices at which the public keys need to be inserted.
func (pq *platformQueue) addNewPublicKeys(bq *InMemoryBuildQueue, newPublicKeys []newPublicKey) {
	if len(newPublicKeys) > 0 {
		mergedPublicKeys := make([]platformQueuePublicKey, 0, len(pq.publicKeys)+len(newPublicKeys))
		previousInsertionIndex := 0
		for _, newPublicKey := range newPublicKeys {
			mergedPublicKeys = append(mergedPublicKeys, pq.publicKeys[previousInsertionIndex:newPublicKey.insertionIndex]...)
			previousInsertionIndex = newPublicKey.insertionIndex
			mergedPublicKeys = append(mergedPublicKeys, newPublicKey.publicKey)
			bq.platformQueues[string(newPublicKey.publicKey.pkixPublicKey)] = pq
		}
		mergedPublicKeys = append(mergedPublicKeys, pq.publicKeys[previousInsertionIndex:]...)
		pq.publicKeys = mergedPublicKeys

		// TODO: Add a per worker bitmap that stores which
		// workers announce which public keys, and grow those
		// here.
	}
}

func (pq *platformQueue) addSizeClassQueue(bq *InMemoryBuildQueue, sizeClass uint32, mayBeRemoved bool) *sizeClassQueue {
	pkixPublicKey, sizeClassStr := pq.getSizeClassQueueLabels(sizeClass)
	platformLabels := map[string]string{
		"pkix_public_key": base64.StdEncoding.EncodeToString(pq.publicKeys[0].pkixPublicKey),
		"size_class":      sizeClassStr,
	}
	scq := &sizeClassQueue{
		platformQueue: pq,
		sizeClass:     sizeClass,
		mayBeRemoved:  mayBeRemoved,

		rootInvocation: invocation{
			children:         map[scheduler_invocation.Key]*invocation{},
			executingWorkers: map[*worker]int{},
		},
		workers: map[workerKey]*worker{},

		drains:        map[string]*buildqueuestate_pb.DrainState{},
		undrainWakeup: make(chan struct{}),

		inFlightDeduplicationsSameInvocation:  inMemoryBuildQueueInFlightDeduplicationsTotal.WithLabelValues(pkixPublicKey, sizeClassStr, "SameInvocation"),
		inFlightDeduplicationsOtherInvocation: inMemoryBuildQueueInFlightDeduplicationsTotal.WithLabelValues(pkixPublicKey, sizeClassStr, "OtherInvocation"),
		inFlightDeduplicationsNew:             inMemoryBuildQueueInFlightDeduplicationsTotal.WithLabelValues(pkixPublicKey, sizeClassStr, "New"),

		tasksScheduledWorker:          inMemoryBuildQueueTasksScheduledTotal.WithLabelValues(pkixPublicKey, sizeClassStr, "Worker"),
		tasksScheduledQueue:           inMemoryBuildQueueTasksScheduledTotal.WithLabelValues(pkixPublicKey, sizeClassStr, "Queue"),
		tasksQueuedDurationSeconds:    inMemoryBuildQueueTasksQueuedDurationSeconds.WithLabelValues(pkixPublicKey, sizeClassStr),
		tasksExecutingDurationSeconds: inMemoryBuildQueueTasksExecutingDurationSeconds.MustCurryWith(platformLabels),
		tasksExecutingRetries:         inMemoryBuildQueueTasksExecutingRetries.MustCurryWith(platformLabels),
		tasksCompletedDurationSeconds: inMemoryBuildQueueTasksCompletedDurationSeconds.WithLabelValues(pkixPublicKey, sizeClassStr),

		workersCreatedTotal:          inMemoryBuildQueueWorkersCreatedTotal.WithLabelValues(pkixPublicKey, sizeClassStr),
		workersTerminatingTotal:      inMemoryBuildQueueWorkersTerminatingTotal.WithLabelValues(pkixPublicKey, sizeClassStr),
		workersRemovedIdleTotal:      inMemoryBuildQueueWorkersRemovedTotal.WithLabelValues(pkixPublicKey, sizeClassStr, "Idle"),
		workersRemovedExecutingTotal: inMemoryBuildQueueWorkersRemovedTotal.WithLabelValues(pkixPublicKey, sizeClassStr, "Executing"),

		workerInvocationStickinessRetained: inMemoryBuildQueueWorkerInvocationStickinessRetained.WithLabelValues(pkixPublicKey, sizeClassStr),
	}
	scq.rootInvocation.sizeClassQueue = scq
	scq.incrementInvocationsCreatedTotal(0)

	// Force creation of all metrics associated with this platform
	// queue to make recording rules work.
	scq.tasksExecutingDurationSeconds.WithLabelValues("Success", "")
	scq.tasksExecutingRetries.WithLabelValues("Success", "")

	// Insert the new size class queue into the platform queue.
	// Keep the size class queues sorted, so that they are provided
	// to initialsizeclass.Selector deterministically.
	i := 0
	for i < len(pq.sizeClasses) && pq.sizeClasses[i] < sizeClass {
		i++
	}

	pq.sizeClasses = append(pq.sizeClasses, 0)
	copy(pq.sizeClasses[i+1:], pq.sizeClasses[i:])
	pq.sizeClasses[i] = sizeClass

	pq.sizeClassQueues = append(pq.sizeClassQueues, nil)
	copy(pq.sizeClassQueues[i+1:], pq.sizeClassQueues[i:])
	pq.sizeClassQueues[i] = scq

	return scq
}

// invocationsMetrics contains Prometheus metrics that should be tracked
// for every depth of the tree of invocations inside a size class queue.
type invocationsMetrics struct {
	createdTotal     prometheus.Counter
	activatedTotal   prometheus.Counter
	deactivatedTotal prometheus.Counter
	removedTotal     prometheus.Counter
}

type sizeClassQueue struct {
	platformQueue *platformQueue
	sizeClass     uint32
	mayBeRemoved  bool

	// Data structure in which all queued and executing operations
	// are placed, and which keeps track of all idle workers that
	// are synchronizing against the scheduler.
	rootInvocation invocation
	workers        map[workerKey]*worker
	cleanupKey     cleanupKey

	drains        map[string]*buildqueuestate_pb.DrainState
	undrainWakeup chan struct{}

	// Prometheus metrics.
	inFlightDeduplicationsSameInvocation  prometheus.Counter
	inFlightDeduplicationsOtherInvocation prometheus.Counter
	inFlightDeduplicationsNew             prometheus.Counter

	invocationsMetrics []invocationsMetrics

	tasksScheduledWorker          prometheus.Counter
	tasksScheduledQueue           prometheus.Counter
	tasksQueuedDurationSeconds    prometheus.Observer
	tasksExecutingDurationSeconds prometheus.ObserverVec
	tasksExecutingRetries         prometheus.ObserverVec
	tasksCompletedDurationSeconds prometheus.Observer

	workersCreatedTotal          prometheus.Counter
	workersTerminatingTotal      prometheus.Counter
	workersRemovedIdleTotal      prometheus.Counter
	workersRemovedExecutingTotal prometheus.Counter

	workerInvocationStickinessRetained prometheus.Observer
}

// remove is invoked when Synchronize() isn't being invoked by any
// worker for a given platform quickly enough. It causes the platform
// queue and all associated queued operations to be removed from the
// InMemoryBuildQueue.
func (scq *sizeClassQueue) remove(bq *InMemoryBuildQueue) {
	scq.rootInvocation.failAllQueuedOperations(
		bq,
		"WorkersDisappeared",
		status.Error(codes.Unavailable, "Workers for this public key and size class disappeared while task was queued"),
	)
	scq.invocationsMetrics[0].removedTotal.Inc()

	pq := scq.platformQueue
	i := 0
	for pq.sizeClassQueues[i] != scq {
		i++
	}
	pq.sizeClasses = append(pq.sizeClasses[:i], pq.sizeClasses[i+1:]...)
	pq.sizeClassQueues = append(pq.sizeClassQueues[:i], pq.sizeClassQueues[i+1:]...)
}

// removeStaleWorker is invoked when Synchronize() isn't being invoked
// by a worker quickly enough. It causes the worker to be removed from
// the InMemoryBuildQueue.
func (scq *sizeClassQueue) removeStaleWorker(bq *InMemoryBuildQueue, workerKey workerKey, removalTime time.Time) {
	w := scq.workers[workerKey]
	scq.markWorkerTerminating(w)
	if t := w.currentTask; t == nil {
		scq.workersRemovedIdleTotal.Inc()
	} else {
		scq.workersRemovedExecutingTotal.Inc()
		t.fail(
			bq,
			"WorkerDisappeared",
			status.Errorf(codes.Unavailable, "Worker %s disappeared while task was executing", workerKey),
			/* completedByWorker = */ false,
		)
	}
	w.clearLastInvocation()
	delete(scq.workers, workerKey)

	// Trigger size class queue removal if necessary.
	if len(scq.workers) == 0 && scq.mayBeRemoved {
		bq.cleanupQueue.add(&scq.cleanupKey, removalTime.Add(bq.configuration.PlatformQueueWithNoWorkersTimeout), func() {
			scq.remove(bq)
		})
	}

	// TODO: Remove public keys!
}

// getOrCreateInvocation looks up the invocation key in the size class
// queue, returning the corresponding invocation. If no invocation under
// this key exists, a new invocation is created.
//
// As the invocation may be just created, the caller must either queue
// an operation, increase the executing operations count, place a worker
// in the invocation or call invocation.removeIfEmpty().
func (scq *sizeClassQueue) getOrCreateInvocation(bq *InMemoryBuildQueue, invocationKeys []scheduler_invocation.Key) *invocation {
	i := &scq.rootInvocation
	for depth, invocationKey := range invocationKeys {
		iChild, ok := i.children[invocationKey]
		if !ok {
			iChild = &invocation{
				sizeClassQueue:                        scq,
				invocationKeys:                        invocationKeys[:depth+1],
				parent:                                i,
				children:                              map[scheduler_invocation.Key]*invocation{},
				queuedChildrenIndex:                   -1,
				executingWorkers:                      map[*worker]int{},
				lastOperationStarted:                  bq.now,
				lastOperationCompletion:               bq.now,
				idleSynchronizingWorkersChildrenIndex: -1,
			}
			i.children[invocationKey] = iChild
			scq.incrementInvocationsCreatedTotal(len(iChild.invocationKeys))

		}
		i = iChild
	}
	return i
}

// incrementInvocationsCreatedTotal increments the
// "invocations_created_total" counter for the provided depth. If no
// counters exist for the given depth, they are created and initialized
// with zero.
func (scq *sizeClassQueue) incrementInvocationsCreatedTotal(depth int) {
	if len(scq.invocationsMetrics) == depth {
		pkixPublicKey, sizeClassStr := scq.platformQueue.getSizeClassQueueLabels(scq.sizeClass)
		depthStr := strconv.FormatInt(int64(depth), 10)

		scq.invocationsMetrics = append(
			scq.invocationsMetrics,
			invocationsMetrics{
				createdTotal:     inMemoryBuildQueueInvocationsCreatedTotal.WithLabelValues(pkixPublicKey, sizeClassStr, depthStr),
				activatedTotal:   inMemoryBuildQueueInvocationsActivatedTotal.WithLabelValues(pkixPublicKey, sizeClassStr, depthStr),
				deactivatedTotal: inMemoryBuildQueueInvocationsDeactivatedTotal.WithLabelValues(pkixPublicKey, sizeClassStr, depthStr),
				removedTotal:     inMemoryBuildQueueInvocationsRemovedTotal.WithLabelValues(pkixPublicKey, sizeClassStr, depthStr),
			})
	}

	scq.invocationsMetrics[depth].createdTotal.Inc()
}

func (scq *sizeClassQueue) markWorkerTerminating(w *worker) {
	if !w.terminating {
		scq.workersTerminatingTotal.Inc()
		w.terminating = true
	}
}

// workerKey can be used as a key for maps to uniquely identify a worker
// within the domain of a certain platform. This key is used for looking
// up the state of a worker when synchronizing.
type workerKey string

func newWorkerKey(workerID map[string]string) workerKey {
	key, err := json.Marshal(workerID)
	if err != nil {
		panic(fmt.Sprintf("Failed to marshal worker ID: %s", err))
	}
	return workerKey(key)
}

// getWorkerID reobtains the worker ID map that was used to construct
// the platformKey. As this is only used infrequently, we don't bother
// keeping the unmarshalled map around to preserve memory usage.
func (k workerKey) getWorkerID() map[string]string {
	var workerID map[string]string
	if err := json.Unmarshal([]byte(k), &workerID); err != nil {
		panic(fmt.Sprintf("Failed to unmarshal previously marshalled worker ID: %s", err))
	}
	return workerID
}

// queuedChildrenHeap is a binary heap that contains a list of all child
// invocations in an invocation that have one or more queued operations.
// It is used to determine which operation should be started in case a
// worker requests a new task.
type queuedChildrenHeap []*invocation

func (h queuedChildrenHeap) Len() int {
	return len(h)
}

func (h queuedChildrenHeap) Less(i, j int) bool {
	return h[i].isPreferred(h[j], h[i].lastOperationStarted.Before(h[j].lastOperationStarted))
}

func (h queuedChildrenHeap) Swap(i, j int) {
	if h[i].queuedChildrenIndex != i || h[j].queuedChildrenIndex != j {
		panic("Invalid queue indices")
	}
	h[i], h[j] = h[j], h[i]
	h[i].queuedChildrenIndex = i
	h[j].queuedChildrenIndex = j
}

func (h *queuedChildrenHeap) Push(x interface{}) {
	e := x.(*invocation)
	if e.queuedChildrenIndex != -1 {
		panic("Invalid queue index")
	}
	e.queuedChildrenIndex = len(*h)
	*h = append(*h, e)
}

func (h *queuedChildrenHeap) Pop() interface{} {
	old := *h
	n := len(old)
	e := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	if e.queuedChildrenIndex != n-1 {
		panic("Invalid queue index")
	}
	e.queuedChildrenIndex = -1
	return e
}

// idleSynchronizingWorkersInvocationsHeap is a binary heap that
// contains all invocations for which one or more workers exist that
// most recently ran a task associated with that invocation, and are
// currently synchronizing against the scheduler.
//
// This heap is used to determine which invocations should have their
// workers rebalanced to other invocations.
type idleSynchronizingWorkersChildrenHeap []*invocation

func (h idleSynchronizingWorkersChildrenHeap) Len() int {
	return len(h)
}

func (h idleSynchronizingWorkersChildrenHeap) Less(i, j int) bool {
	// Sort invocations by worker utilization rate, so that
	// invocations with an excessive number of workers have their
	// workers taken first. Invocations can only be part of this
	// heap if they have no queued operations, so we only need to
	// consider executing and idle workers:
	//
	//     utilization = executing / (executing + idle)
	//
	// Simplify the comparison by cross-multiplying and removing the
	// common part.
	ui := uint64(len(h[i].executingWorkers)) * uint64(len(h[j].idleSynchronizingWorkers))
	uj := uint64(len(h[j].executingWorkers)) * uint64(len(h[i].idleSynchronizingWorkers))
	if ui < uj {
		return true
	} else if ui > uj {
		return false
	}
	// Tie breaker, most likely because both invocations are no
	// longer executing anything. Remove workers from the oldest
	// invocation first, as those are the least likely to be needed.
	return h[i].lastOperationCompletion.Before(h[j].lastOperationCompletion)
}

func (h idleSynchronizingWorkersChildrenHeap) Swap(i, j int) {
	if h[i].idleSynchronizingWorkersChildrenIndex != i || h[j].idleSynchronizingWorkersChildrenIndex != j {
		panic("Invalid queue indices")
	}
	h[i], h[j] = h[j], h[i]
	h[i].idleSynchronizingWorkersChildrenIndex = i
	h[j].idleSynchronizingWorkersChildrenIndex = j
}

func (h *idleSynchronizingWorkersChildrenHeap) Push(x interface{}) {
	e := x.(*invocation)
	if e.idleSynchronizingWorkersChildrenIndex != -1 {
		panic("Invalid queue index")
	}
	e.idleSynchronizingWorkersChildrenIndex = len(*h)
	*h = append(*h, e)
}

func (h *idleSynchronizingWorkersChildrenHeap) Pop() interface{} {
	old := *h
	n := len(old)
	e := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	if e.idleSynchronizingWorkersChildrenIndex != n-1 {
		panic("Invalid queue index")
	}
	e.idleSynchronizingWorkersChildrenIndex = -1
	return e
}

// invocation keeps track of operations that all need to be scheduled on
// a single size class queue, all having the same invocation ID. These
// operations will be scheduled fairly with respect to other
// invocations.
type invocation struct {
	sizeClassQueue *sizeClassQueue
	invocationKeys []scheduler_invocation.Key
	parent         *invocation

	// Heap of operations that are part of this invocation that are
	// currently in the QUEUED stage.
	queuedOperations queuedOperationsHeap

	// All nested invocations for which one or more operations or
	// workers exist.
	children map[scheduler_invocation.Key]*invocation
	// Heap of nested invocations for which one or more operations
	// exist that are in the QUEUED stage.
	queuedChildren queuedChildrenHeap
	// All nested invocations for which one or more idle
	// synchronizing workers exist.
	idleSynchronizingWorkersChildren idleSynchronizingWorkersChildrenHeap

	// The index of this invocation inside the queuedChildren heap
	// of the parent invocation.
	queuedChildrenIndex int
	// The index of this invocation inside the
	// sizeClassQueue.idleSynchronizingWorkersChildren heap.
	idleSynchronizingWorkersChildrenIndex int

	// The priority of the operation that is either queued in this
	// invocation or one of its children that is expected to be
	// executed next. This value needs to be tracked to ensure the
	// queuedChildren heap of the parent invocation is ordered
	// correctly.
	firstQueuedOperationPriority int32

	// Number of workers that are executing operations that belong
	// to this invocation. This equals the number of operations that
	// are part of this invocation that are currently in the
	// EXECUTING stage.
	executingWorkers     map[*worker]int
	lastOperationStarted time.Time
	// The time at which the last executing operation was completed.
	// This value is used to determine which invocations are the
	// best candidates for rebalancing idle synchronizing workers.
	lastOperationCompletion time.Time
	// Number of workers that are idle and most recently completed
	// an operation belonging to this invocation.
	idleWorkersCount uint32
	// List of workers that are idle and most recently executed an
	// operation belonging to this invocation and are currently
	// synchronizing against the scheduler.
	idleSynchronizingWorkers idleSynchronizingWorkersList
}

// isQueued returns whether an invocation has one or more queued
// operations, or contains a child invocation that has one or more
// queued operations.
func (i *invocation) isQueued() bool {
	return i.queuedOperations.Len() > 0 || i.queuedChildren.Len() > 0
}

// isActive returns whether an invocation has one or more queued or
// executing operations. These are generally the ones that users of the
// BuildQueueState service want to view.
func (i *invocation) isActive() bool {
	return i.isQueued() || len(i.executingWorkers) > 0
}

// removeIfEmpty checks whether the invocation is empty (i.e., not
// containing any operations or workers). If so, it removes the
// invocation from the size class queue in which it is contained.
func (i *invocation) removeIfEmpty() bool {
	if i.parent != nil && !i.isActive() && i.idleWorkersCount == 0 {
		depth := len(i.invocationKeys)
		invocationKey := i.invocationKeys[depth-1]
		if i.parent.children[invocationKey] != i {
			panic("Attempted to remove an invocation that was already removed")
		}
		delete(i.parent.children, invocationKey)
		i.sizeClassQueue.invocationsMetrics[depth].removedTotal.Inc()
		return true
	}
	return false
}

func (i *invocation) getInvocationState(bq *InMemoryBuildQueue) *buildqueuestate_pb.InvocationState {
	activeInvocationsCount := uint32(0)
	for _, iChild := range i.children {
		if iChild.isActive() {
			activeInvocationsCount++
		}
	}
	return &buildqueuestate_pb.InvocationState{
		QueuedOperationsCount:         uint32(i.queuedOperations.Len()),
		ChildrenCount:                 uint32(len(i.children)),
		QueuedChildrenCount:           uint32(i.queuedChildren.Len()),
		ActiveChildrenCount:           uint32(activeInvocationsCount),
		ExecutingWorkersCount:         uint32(len(i.executingWorkers)),
		IdleWorkersCount:              i.idleWorkersCount,
		IdleSynchronizingWorkersCount: uint32(len(i.idleSynchronizingWorkers)),
	}
}

// decrementExecutingWorkersCount decrements the number of operations in
// the EXECUTING stage that are part of this invocation.
//
// Because the number of operations in the EXECUTING stage is used to
// prioritize tasks, this function may need to adjust the position of
// this invocation in the queued invocations heap. It may also need to
// remove the invocation entirely in case it no longer contains any
// operations.
func (i *invocation) decrementExecutingWorkersCount(bq *InMemoryBuildQueue, w *worker) {
	for {
		if i.executingWorkers[w] <= 0 {
			panic("Executing workers count invalid")
		}
		i.executingWorkers[w]--
		if i.executingWorkers[w] == 0 {
			delete(i.executingWorkers, w)
			i.maybeDeactivate()
		}
		i.lastOperationCompletion = bq.now
		if i.parent == nil {
			break
		}
		heapMaybeFix(&i.parent.queuedChildren, i.queuedChildrenIndex)
		heapMaybeFix(&i.parent.idleSynchronizingWorkersChildren, i.idleSynchronizingWorkersChildrenIndex)
		i.removeIfEmpty()
		i = i.parent
	}
}

// incrementExecutingWorkersCount increments the number of operations in
// the EXECUTING stage that are part of this invocation.
func (i *invocation) incrementExecutingWorkersCount(bq *InMemoryBuildQueue, w *worker) {
	for {
		i.maybeActivate()
		i.executingWorkers[w]++
		i.lastOperationStarted = bq.now
		if i.parent == nil {
			break
		}
		heapMaybeFix(&i.parent.queuedChildren, i.queuedChildrenIndex)
		heapMaybeFix(&i.parent.idleSynchronizingWorkersChildren, i.idleSynchronizingWorkersChildrenIndex)
		i = i.parent
	}
}

func (i *invocation) hasInvocationKey(filter scheduler_invocation.Key) bool {
	for _, key := range i.invocationKeys {
		if key == filter {
			return true
		}
	}
	return false
}

// maybeActivate should be called before an invocation is transitioning
// from being possibly idle to having one or more queued or executing
// operations.
func (i *invocation) maybeActivate() {
	if !i.isActive() {
		i.sizeClassQueue.invocationsMetrics[len(i.invocationKeys)].activatedTotal.Inc()
	}
}

// maybeDectivate should be called after an invocation is transitioning
// from having one or more queued or executing operations to being
// possibly idle.
func (i *invocation) maybeDeactivate() {
	if !i.isActive() {
		i.sizeClassQueue.invocationsMetrics[len(i.invocationKeys)].deactivatedTotal.Inc()
	}
}

var priorityExponentiationBase = math.Pow(2.0, 0.01)

// isPreferred returns whether the first queued operation of invocation
// i should be preferred over the first queued operation of invocation j.
func (i *invocation) isPreferred(j *invocation, tieBreaker bool) bool {
	// To introduce fairness, we want to prefer scheduling
	// operations belonging to invocations that have the fewest
	// running operations. In addition to that, we still want to
	// respect priorities at the global level.
	//
	// Combine these two properties into a single score value
	// according to the following expression, where the invocation
	// with the lowest score is most favourable.
	//
	// S = (executingWorkersCount + 1) * b^priority
	//
	// Note that priorities are inverted; the lower the integer
	// value, the higher the priority. The '+ 1' part has been added
	// to this expression to ensure that the priority is still taken
	// into account when the number of executing operations is zero.
	//
	// The base value for the expontentiation is chosen to be
	// 2^0.01 =~ 1.007. This means that if the difference in
	// priority between two builds is 100, one build will be allowed
	// to run twice as many operations as the other.
	ei, ej := float64(len(i.executingWorkers)+1), float64(len(j.executingWorkers)+1)
	var si, sj float64
	if pi, pj := float64(i.firstQueuedOperationPriority), float64(j.firstQueuedOperationPriority); pi < pj {
		// Invocation i has a higher priority. Give invocation j
		// a penalty based on the difference in priority.
		si, sj = ei, ej*math.Pow(priorityExponentiationBase, pj-pi)
	} else if pi > pj {
		// Invocation j has a higher priority. Give invocation i
		// a penalty based on the difference in priority.
		si, sj = ei*math.Pow(priorityExponentiationBase, pi-pj), ej
	} else {
		// Both invocations have the same priority.
		si, sj = ei, ej
	}
	return si < sj || (si == sj && tieBreaker)
}

func (i *invocation) failAllQueuedOperations(bq *InMemoryBuildQueue, result string, failureErr error) {
	// Recursively cancel operations belonging to nested invocations.
	for i.queuedChildren.Len() > 0 {
		i.queuedChildren[len(i.queuedChildren)-1].failAllQueuedOperations(bq, result, failureErr)
	}

	// Cancel operations directly belonging to this invocation.
	for i.queuedOperations.Len() > 0 {
		i.queuedOperations[i.queuedOperations.Len()-1].task.fail(
			bq,
			result,
			failureErr,
			/* completedByWorker = */ false,
		)
	}
}

func (i *invocation) updateFirstOperationPriority() {
	// worker.assignNextQueuedTask() prefers scheduling queued
	// operations stored directly underneath an invocation over ones
	// stored in child invocations, so we do the same thing here.
	if i.queuedOperations.Len() > 0 {
		i.firstQueuedOperationPriority = i.queuedOperations[0].priority
	} else if len(i.queuedChildren) > 0 {
		i.firstQueuedOperationPriority = i.queuedChildren[0].firstQueuedOperationPriority
	}
}

// queuedOperationsHeap is a binary heap that stores queued operations,
// sorted by order in which they need to be assigned to workers.
type queuedOperationsHeap []*operation

func (h queuedOperationsHeap) Len() int {
	return len(h)
}

func (h queuedOperationsHeap) Less(i, j int) bool {
	// Lexicographic order on priority, expected duration and queued
	// timestamp. By executing operations with a higher expected
	// duration first, we reduce the probability of having poor
	// concurrency at the final stages of a build.
	if h[i].priority < h[j].priority {
		return true
	}
	if h[i].priority > h[j].priority {
		return false
	}
	ti, tj := h[i].task, h[j].task
	if ti.expectedDuration > tj.expectedDuration {
		return true
	}
	if ti.expectedDuration < tj.expectedDuration {
		return false
	}
	return ti.desiredState.QueuedTimestamp.AsTime().Before(tj.desiredState.QueuedTimestamp.AsTime())
}

func (h queuedOperationsHeap) Swap(i, j int) {
	if h[i].queueIndex != i || h[j].queueIndex != j {
		panic("Invalid queue indices")
	}
	h[i], h[j] = h[j], h[i]
	h[i].queueIndex = i
	h[j].queueIndex = j
}

func (h *queuedOperationsHeap) Push(x interface{}) {
	o := x.(*operation)
	if o.queueIndex != -1 {
		panic("Invalid queue index")
	}
	o.queueIndex = len(*h)
	*h = append(*h, o)
}

func (h *queuedOperationsHeap) Pop() interface{} {
	old := *h
	n := len(old)
	o := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	if o.queueIndex != n-1 {
		panic("Invalid queue index")
	}
	o.queueIndex = -1
	return o
}

// operation that a client can use to reference a task.
//
// The difference between operations and tasks is that tasks manage the
// lifecycle of a piece of work in general, while operations manage it
// in the context of a client invocation. This means that if in-flight
// deduplication of requests occurs, a task may be associated with two
// or more operations.
//
// If a single client were to abandon an operation (e.g., by closing the
// gRPC channel), the task and other operations that task will remain
// unaffected.
type operation struct {
	name     string
	task     *task
	priority int32

	// The invocation of which this operation is a part. queueIndex
	// contains the index at which the operation is stored in the
	// invocation's queuedOperations heap. When negative, it means
	// that the operation is no longer in the queued stage (and thus
	// either in the executing or completed stage).
	//
	// Because invocations are managed per size class, an operation
	// may move from one invocation to another one it is retried as
	// part of a different size class. All invocations of which this
	// operation is a part during its lifetime will have the same
	// invocation ID.
	invocation *invocation
	queueIndex int

	// Number of clients that are calling Execute() or
	// WaitExecution() on this operation.
	waiters                uint
	mayExistWithoutWaiters bool
	cleanupKey             cleanupKey
}

// waitExecution periodically streams a series of longrunningpb.Operation
// messages back to the client, containing the state of the current
// operation. Streaming is stopped after execution of the operation is
// completed.
func (o *operation) waitExecution(bq *InMemoryBuildQueue, out remoteexecution_pb.Execution_ExecuteServer) error {
	ctx := out.Context()

	// Bookkeeping for determining whether operations are abandoned
	// by clients. Operations should be removed if there are no
	// clients calling Execute() or WaitExecution() for a certain
	// amount of time.
	if o.cleanupKey.isActive() {
		bq.cleanupQueue.remove(o.cleanupKey)
	}
	o.waiters++
	defer func() {
		if o.waiters == 0 {
			panic("Invalid waiters count on operation")
		}
		o.waiters--
		o.maybeStartCleanup(bq)
	}()

	t := o.task
	executionEventsReturned := uint(0)
	for {
		// Construct the ExecuteResponse that needs to be sent
		// back to the client.
		response := &remoteexecution_pb.ExecuteResponse{
			Name: o.name,
		}
		done := false
		if t.initialSizeClassLearner == nil {
			if err := t.failureErr; err != nil {
				return err
			}
			response.Stage = &remoteexecution_pb.ExecuteResponse_Completed_{
				Completed: &remoteexecution_pb.ExecuteResponse_Completed{
					CompletionEvent: t.lastExecutionEvent,
				},
			}
			done = true
		} else if t.currentWorker != nil {
			// Attach an execution event in case the worker
			// posted once since our last response.
			var lastEvent *encryptedaction_pb.Event
			if executionEventsReturned != t.executionEventsSeen {
				lastEvent = t.lastExecutionEvent
				executionEventsReturned = t.executionEventsSeen
			}
			response.Stage = &remoteexecution_pb.ExecuteResponse_Executing_{
				Executing: &remoteexecution_pb.ExecuteResponse_Executing{
					LastEvent: lastEvent,
				},
			}
		} else {
			response.Stage = &remoteexecution_pb.ExecuteResponse_Queued_{
				Queued: &remoteexecution_pb.ExecuteResponse_Queued{},
			}
		}

		stageChangeWakeup := t.stageChangeWakeup
		bq.leave()

		// Send the ExecuteResponse back to the client.
		if err := out.Send(response); done || err != nil {
			bq.enter(bq.clock.Now())
			return err
		}

		// Suspend until the client closes the connection, the
		// action completes or a certain amount of time has
		// passed without any updates.
		timer, timerChannel := bq.clock.NewTimer(bq.configuration.ExecutionUpdateInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			bq.enter(bq.clock.Now())
			return util.StatusFromContext(ctx)
		case <-stageChangeWakeup:
			timer.Stop()
			bq.enter(bq.clock.Now())
		case t := <-timerChannel:
			bq.enter(t)
		}
	}
}

// removeQueuedFromInvocation removes an operation that is in the queued
// state from the invocation. If the invocation no longer has any queued
// operations, it will be removed from the queued invocations heap in
// the containing platform queue.
func (o *operation) removeQueuedFromInvocation() {
	i := o.invocation
	heap.Remove(&i.queuedOperations, o.queueIndex)
	i.maybeDeactivate()
	for i.parent != nil {
		i.updateFirstOperationPriority()
		heapRemoveOrFix(&i.parent.queuedChildren, i.queuedChildrenIndex, i.queuedChildren.Len()+i.queuedOperations.Len())
		i.parent.maybeDeactivate()
		i = i.parent
	}
}

// enqueue a newly created operation in the heap of queued operations of
// an invocation. This method is called whenever an operation can't be
// assigned to a worker immediately, due to no idle synchronizing
// workers for this size class queue being available.
func (o *operation) enqueue() {
	i := o.invocation
	i.maybeActivate()
	heap.Push(&i.queuedOperations, o)
	for i.parent != nil {
		i.updateFirstOperationPriority()
		i.parent.maybeActivate()
		heapPushOrFix(&i.parent.queuedChildren, i.queuedChildrenIndex, i)
		i = i.parent
	}
}

func (o *operation) remove(bq *InMemoryBuildQueue) {
	delete(bq.operationsNameMap, o.name)

	t := o.task
	if len(t.operations) == 1 {
		// Forcefully terminate the associated task if it won't
		// have any other operations associated with it.
		t.fail(
			bq,
			"NoWaitingClients",
			status.Error(codes.Canceled, "Task no longer has any waiting clients"),
			/* completedByWorker = */ false,
		)
		t.registerCompletedStageFinished(bq)
	} else {
		// The underlying task is shared with other operations.
		// Remove the operation, while leaving the task intact.
		i := o.invocation
		if w := t.currentWorker; w != nil {
			i.decrementExecutingWorkersCount(bq, w)
		} else if t.initialSizeClassLearner != nil {
			o.removeQueuedFromInvocation()
			for i.removeIfEmpty() {
				i = i.parent
			}
		}
	}
	delete(t.operations, o.invocation)
}

func (o *operation) getOperationState(bq *InMemoryBuildQueue) *buildqueuestate_pb.OperationState {
	i := o.invocation
	t := o.task
	scq := t.getCurrentSizeClassQueue()
	invocationIDs := make([]*anypb.Any, 0, len(i.invocationKeys))
	for _, invocationKey := range i.invocationKeys {
		invocationIDs = append(invocationIDs, invocationKey.GetID())
	}
	s := &buildqueuestate_pb.OperationState{
		Name: o.name,
		InvocationName: &buildqueuestate_pb.InvocationName{
			SizeClassQueueName: &buildqueuestate_pb.SizeClassQueueName{
				PlatformPkixPublicKey: scq.platformQueue.publicKeys[0].pkixPublicKey,
				SizeClass:             scq.sizeClass,
			},
			Ids: invocationIDs,
		},
		ExpectedDuration: durationpb.New(t.expectedDuration),
		QueuedTimestamp:  t.desiredState.QueuedTimestamp,
		Action:           t.desiredState.Action,
		Timeout:          bq.cleanupQueue.getTimestamp(o.cleanupKey),
		Priority:         o.priority,
	}
	if t.initialSizeClassLearner == nil {
		s.Stage = &buildqueuestate_pb.OperationState_Completed{
			Completed: &emptypb.Empty{},
		}
	} else if t.currentWorker != nil {
		s.Stage = &buildqueuestate_pb.OperationState_Executing{
			Executing: &emptypb.Empty{},
		}
	} else {
		s.Stage = &buildqueuestate_pb.OperationState_Queued{
			Queued: &emptypb.Empty{},
		}
	}
	return s
}

func (o *operation) maybeStartCleanup(bq *InMemoryBuildQueue) {
	if o.waiters == 0 && !o.mayExistWithoutWaiters {
		bq.cleanupQueue.add(&o.cleanupKey, bq.now.Add(bq.configuration.OperationWithNoWaitersTimeout), func() {
			o.remove(bq)
		})
	}
}

// task state that is created for every piece of work that needs to be
// executed by a worker. Tasks are associated with one or more
// operations. In the general case a task has one operation, but there
// may be multiple in case multiple clients request that the same action
// is built and deduplication is performed.
type task struct {
	operations       map[*invocation]*operation
	deduplicationKey [sha256.Size]byte
	desiredState     remoteworker_pb.DesiredState_Executing

	// currentStageStartTime is used by register*StageFinished() to
	// obtain Prometheus metrics.
	currentStageStartTime time.Time

	// The worker that is currently executing the task. The
	// retryCount specifies how many additional times the operation
	// was provided to the worker. This counter may be non-zero in
	// case of network flakiness or worker crashes.
	currentWorker *worker
	retryCount    int

	expectedDuration        time.Duration
	initialSizeClassLearner initialsizeclass.Learner
	mayExistWithoutWaiters  bool

	lastExecutionEvent  *encryptedaction_pb.Event
	executionEventsSeen uint
	failureErr          error
	stageChangeWakeup   chan struct{}
}

// newOperation attaches a new operation to a task. This function must
// be called at least once after creating a task. It is most frequently
// called right after creating a task, but may also be used to attach
// additional operations to an existing task in case of in-flight
// deduplication.
func (t *task) newOperation(bq *InMemoryBuildQueue, priority int32, i *invocation, mayExistWithoutWaiters bool) *operation {
	o := &operation{
		name:                   util.Must(bq.uuidGenerator()).String(),
		task:                   t,
		priority:               priority,
		invocation:             i,
		mayExistWithoutWaiters: mayExistWithoutWaiters,
		queueIndex:             -1,
	}
	if _, ok := t.operations[i]; ok {
		panic("Task is already associated with this invocation")
	}
	t.operations[i] = o
	bq.operationsNameMap[o.name] = o
	return o
}

// reportNonFinalStageChange can be used to wake up clients that are
// calling Execute() or WaitExecution(), causing them to receive another
// non-final stage change update.
func (t *task) reportNonFinalStageChange() {
	close(t.stageChangeWakeup)
	t.stageChangeWakeup = make(chan struct{})
}

// schedule a task. This function will first attempt to directly assign
// a task to an idle worker that is synchronizing against the scheduler.
// When no such worker exists, it will queue the operation, so that a
// worker may pick it up later.
func (t *task) schedule(bq *InMemoryBuildQueue) {
	// Check whether there are idle workers that are synchronizing
	// against the scheduler on which we can schedule the operation
	// directly.
	//
	// We should prefer taking workers that are associated with
	// invocations that have some similarity with those of the task,
	// so that locality is improved. Scan the tree of invocations
	// bottom up, breadth first to find an appropriate worker.
	scq := t.getCurrentSizeClassQueue()
	invocations := make([]*invocation, 0, len(t.operations))
	for i := range t.operations {
		invocations = append(invocations, i)
	}
	for {
		for idx, i := range invocations {
			if len(i.idleSynchronizingWorkers) > 0 || i.idleSynchronizingWorkersChildren.Len() > 0 {
				// This invocation either has idle
				// workers available, or it contains an
				// invocation that has idle workers.
				//
				// Schedule the task directly on the
				// most preferable worker.
				for len(i.idleSynchronizingWorkers) == 0 {
					i = i.idleSynchronizingWorkersChildren[0]
				}
				// TODO: Do we want to provide a histogram
				// on how far the new invocation is removed
				// from the original one?
				t.registerQueuedStageStarted(bq, scq.tasksScheduledWorker)
				i.idleSynchronizingWorkers[0].worker.assignUnqueuedTaskAndWakeUp(bq, t, 0)
				return
			}
			if i.parent == nil {
				// Even the root invocation has no idle
				// workers available that are
				// synchronizing against the scheduler.
				//
				// Queue the operation, so that workers
				// can pick it up when they become
				// available.
				t.registerQueuedStageStarted(bq, scq.tasksScheduledQueue)
				for _, o := range t.operations {
					o.enqueue()
				}
				return
			}
			invocations[idx] = i.parent
		}
	}
}

// getStage returns whether the task is in the queued, executing or
// completed stage.
func (t *task) getStage() buildqueuestate_pb.ListOperationsRequest_ExecutionStage {
	if t.initialSizeClassLearner == nil {
		return buildqueuestate_pb.ListOperationsRequest_COMPLETED
	}
	if t.currentWorker != nil {
		return buildqueuestate_pb.ListOperationsRequest_EXECUTING
	}
	return buildqueuestate_pb.ListOperationsRequest_QUEUED
}

func (t *task) leaveQueuedOrExecutingStage(bq *InMemoryBuildQueue, result, grpcCode string, completedByWorker bool) {
	if t.initialSizeClassLearner == nil {
		panic("Task was already marked as being completed")
	}

	scq := t.getCurrentSizeClassQueue()
	if w := t.currentWorker; w != nil {
		// Task is executing on a worker. Make sure to preserve
		// worker.lastInvocation.
		if completedByWorker {
			// Due to in-flight deduplication, the task may
			// be associated with multiple invocations.
			// Compute the invocation that is the lowest
			// common ancestor.
			var iLowest *invocation
			for i := range t.operations {
				if iLowest == nil {
					// First iteration.
					iLowest = i
				} else {
					// Find lowest common ancestor
					// of two invocations.
					for len(iLowest.invocationKeys) > len(i.invocationKeys) {
						iLowest = iLowest.parent
					}
					for len(i.invocationKeys) > len(iLowest.invocationKeys) {
						i = i.parent
					}
					for iLowest != i {
						iLowest = iLowest.parent
						i = i.parent
					}
				}
			}
			w.setLastInvocation(iLowest)
		} else {
			// In case the worker didn't complete executing
			// the task, move the worker back to the initial
			// state where it's associated with the root
			// invocation. There is no point in offering any
			// locality/stickiness.
			w.setLastInvocation(&scq.rootInvocation)
		}
	} else {
		// The task isn't executing. Create a temporary worker
		// on which we start the task, so that we can go through
		// the regular completion code below.
		var w worker
		w.assignQueuedTask(bq, t, 0)
	}

	for i := range t.operations {
		i.decrementExecutingWorkersCount(bq, t.currentWorker)
	}
	t.currentWorker.currentTask = nil
	t.currentWorker = nil
	t.registerExecutingStageFinished(bq, result, grpcCode)
}

func (t *task) markCompleted(bq *InMemoryBuildQueue) {
	t.initialSizeClassLearner = nil
	delete(bq.inFlightDeduplicationMap, t.deduplicationKey)
	close(t.stageChangeWakeup)
	t.stageChangeWakeup = nil

	// Background learning tasks may continue to exist, even if no
	// clients wait for the results. Now that this task is
	// completed, it must go through the regular cleanup process.
	for _, o := range t.operations {
		if o.mayExistWithoutWaiters {
			o.mayExistWithoutWaiters = false
			o.maybeStartCleanup(bq)
		}
	}
}

// complete execution of the task by registering the execution response.
// This function wakes up any clients waiting on the task to complete.
func (t *task) complete(bq *InMemoryBuildQueue, completed *remoteworker_pb.CurrentState_Completed) {
	if t.initialSizeClassLearner == nil {
		return
	}

	result := "Unknown"
	switch completed.Result {
	case remoteworker_pb.CurrentState_Completed_SUCCEEDED:
		result = "Succeeded"
	case remoteworker_pb.CurrentState_Completed_TIMED_OUT:
		result = "TimedOut"
	case remoteworker_pb.CurrentState_Completed_FAILED:
		result = "Failed"
	}

	t.leaveQueuedOrExecutingStage(bq, result, "", true)

	// Communicate the results to the initial size class learner,
	// which may request that the task is re-executed.
	pq := t.getCurrentSizeClassQueue().platformQueue
	if completed.Result == remoteworker_pb.CurrentState_Completed_SUCCEEDED {
		// The task succeeded, but we may still get instructed
		// to run the task again for training purposes. If that
		// happens, create a new task that runs in the
		// background. The user does not need to be blocked on
		// this.
		if backgroundSizeClassIndex, backgroundExpectedDuration, backgroundTimeout, backgroundInitialSizeClassLearner := t.initialSizeClassLearner.Succeeded(
			completed.VirtualExecutionDuration.AsDuration(),
			pq.sizeClasses,
		); backgroundInitialSizeClassLearner != nil {
			if pq.maximumQueuedBackgroundLearningOperations == 0 {
				// No background learning permitted.
				backgroundInitialSizeClassLearner.Abandoned()
			} else {
				backgroundSCQ := pq.sizeClassQueues[backgroundSizeClassIndex]
				backgroundInvocation := backgroundSCQ.getOrCreateInvocation(bq, scheduler_invocation.BackgroundLearningKeys)
				if backgroundInvocation.queuedOperations.Len() >= pq.maximumQueuedBackgroundLearningOperations {
					// Already running too many background tasks.
					backgroundInitialSizeClassLearner.Abandoned()
				} else {
					backgroundTask := &task{
						operations: map[*invocation]*operation{},
						desiredState: remoteworker_pb.DesiredState_Executing{
							TaskUuid:                  util.Must(bq.uuidGenerator()).String(),
							Action:                    t.desiredState.Action,
							EffectiveExecutionTimeout: durationpb.New(backgroundTimeout),
							QueuedTimestamp:           t.desiredState.QueuedTimestamp,
							AuxiliaryMetadata:         t.desiredState.AuxiliaryMetadata,
							W3CTraceContext:           t.desiredState.W3CTraceContext,
						},
						expectedDuration:        backgroundExpectedDuration,
						initialSizeClassLearner: backgroundInitialSizeClassLearner,
						stageChangeWakeup:       make(chan struct{}),
					}
					backgroundTask.newOperation(bq, pq.backgroundLearningOperationPriority, backgroundInvocation, true)
					backgroundTask.schedule(bq)
				}
			}
		}
		t.lastExecutionEvent = completed.Event
		t.markCompleted(bq)
	} else if expectedDuration, timeout, initialSizeClassLearner := t.initialSizeClassLearner.Failed(completed.Result == remoteworker_pb.CurrentState_Completed_TIMED_OUT); initialSizeClassLearner != nil {
		// Re-execution against the largest size class is
		// requested, using the original timeout value.
		// Transplant all operations to the other size class
		// queue and reschedule.
		t.initialSizeClassLearner = initialSizeClassLearner
		t.expectedDuration = expectedDuration
		t.desiredState.EffectiveExecutionTimeout = durationpb.New(timeout)
		t.registerCompletedStageFinished(bq)
		largestSCQ := pq.sizeClassQueues[len(pq.sizeClassQueues)-1]
		operations := t.operations
		t.operations = make(map[*invocation]*operation, len(operations))
		for oldI, o := range operations {
			i := largestSCQ.getOrCreateInvocation(bq, oldI.invocationKeys)
			t.operations[i] = o
			o.invocation = i
		}
		t.schedule(bq)
		t.reportNonFinalStageChange()
	} else {
		t.lastExecutionEvent = completed.Event
		t.markCompleted(bq)
	}
}

func (t *task) fail(bq *InMemoryBuildQueue, result string, failureErr error, completedByWorker bool) {
	if t.initialSizeClassLearner == nil {
		return
	}

	t.leaveQueuedOrExecutingStage(bq, result, status.Code(failureErr).String(), completedByWorker)
	t.initialSizeClassLearner.Abandoned()
	t.initialSizeClassLearner = nil
	t.failureErr = failureErr
	t.markCompleted(bq)
}

// registerQueuedStageStarted updates Prometheus metrics related to the
// task entering the QUEUED stage.
func (t *task) registerQueuedStageStarted(bq *InMemoryBuildQueue, tasksScheduledCounter prometheus.Counter) {
	tasksScheduledCounter.Inc()
	t.currentStageStartTime = bq.now
}

// registerQueuedStageFinished updates Prometheus metrics related to the
// task finishing the QUEUED stage.
func (t *task) registerQueuedStageFinished(bq *InMemoryBuildQueue) {
	scq := t.getCurrentSizeClassQueue()
	scq.tasksQueuedDurationSeconds.Observe(bq.now.Sub(t.currentStageStartTime).Seconds())
	t.currentStageStartTime = bq.now
}

// registerExecutingStageFinished updates Prometheus metrics related to
// the task finishing the EXECUTING stage.
func (t *task) registerExecutingStageFinished(bq *InMemoryBuildQueue, result, grpcCode string) {
	scq := t.getCurrentSizeClassQueue()
	scq.tasksExecutingDurationSeconds.WithLabelValues(result, grpcCode).Observe(bq.now.Sub(t.currentStageStartTime).Seconds())
	scq.tasksExecutingRetries.WithLabelValues(result, grpcCode).Observe(float64(t.retryCount))
	t.currentStageStartTime = bq.now
}

// registerCompletedStageFinished updates Prometheus metrics related to
// the task finishing the COMPLETED stage, meaning the task got removed.
func (t *task) registerCompletedStageFinished(bq *InMemoryBuildQueue) {
	scq := t.getCurrentSizeClassQueue()
	scq.tasksCompletedDurationSeconds.Observe(bq.now.Sub(t.currentStageStartTime).Seconds())
	t.currentStageStartTime = bq.now
}

// getCurrentSizeClassQueue returns the size class queue that is
// currently associated with the task. The size class queue may change
// if execution fails, and execution is retried on the largest size
// class queue.
func (t *task) getCurrentSizeClassQueue() *sizeClassQueue {
	for i := range t.operations {
		return i.sizeClassQueue
	}
	panic("Task is not associated with any operations")
}

// worker state for every node capable of executing operations.
type worker struct {
	workerKey workerKey

	// The task that this worker is currently executing. This field
	// must be kept in sync with task.currentWorker.
	currentTask *task
	// Used to garbage collect workers that have disappeared.
	cleanupKey cleanupKey
	// When true, this worker is going to terminate in the nearby
	// future. This is effectively a drain that cannot be cleared
	// through the BuildQueueState interface.
	terminating bool
	// The invocation that was associated with the task that this
	// worker completed most recently. This is used to make sure
	// successive tasks belonging to this invocation is more likely
	// to end up on this worker. This improves locality.
	//
	// Because of in-flight deduplication, it may be the case that
	// the worker was executing a task that belonged to multiple
	// invocations. If this is the case, lastInvocation will point
	// to the lowest common ancestor.
	lastInvocation *invocation
	// When set, the worker is idle and currently issuing a blocking
	// Synchronize() call against the scheduler. This channel can be
	// closed to wake up the worker, either after assigning a task
	// to it or to force it becoming drained.
	wakeup chan<- struct{}
	// When 'wakeup' is set, this contains the index at which this
	// worker is placed in lastInvocation's
	// idleSynchronizingWorkers. This index is needed to efficiently
	// dequeue the worker in case of wakeups or Synchronize()
	// interruptions.
	listIndex int
	// For every level of worker invocation of stickiness, the time
	// at which we started executing operations belonging to the
	// current invocation. These values are used to determine
	// whether the stickiness limit has been reached.
	stickinessStartingTimes []time.Time
}

func workerMatchesPattern(workerID, workerIDPattern map[string]string) bool {
	for key, value := range workerIDPattern {
		if workerID[key] != value {
			return false
		}
	}
	return true
}

func (w *worker) isDrained(scq *sizeClassQueue, workerID map[string]string) bool {
	// Implicitly treat workers that are terminating as being
	// drained. This prevents tasks from getting interrupted.
	if w.terminating {
		return true
	}
	for _, drain := range scq.drains {
		if workerMatchesPattern(workerID, drain.WorkerIdPattern) {
			return true
		}
	}
	return false
}

// dequeue a worker. This method is either called by the worker itself
// at the end of Synchronize(), or when a worker needs to be woken up.
func (w *worker) dequeue(scq *sizeClassQueue) {
	i := w.lastInvocation
	i.idleSynchronizingWorkers.dequeue(w.listIndex)
	for i.parent != nil {
		heapRemoveOrFix(
			&i.parent.idleSynchronizingWorkersChildren,
			i.idleSynchronizingWorkersChildrenIndex,
			len(i.idleSynchronizingWorkers)+i.idleSynchronizingWorkersChildren.Len())
		i = i.parent
	}
	w.wakeup = nil
}

// maybeDequeue is the same as dequeue(), except that it may also be
// called if the worker is not actually queued. This is used to dequeue
// a worker as part of interrupted Synchronize() calls.
func (w *worker) maybeDequeue(scq *sizeClassQueue) {
	if w.wakeup != nil {
		w.dequeue(scq)
	}
}

// wakeUp wakes up an idle worker that is currently synchronizing
// against the scheduler.
func (w *worker) wakeUp(scq *sizeClassQueue) {
	close(w.wakeup)
	w.dequeue(scq)
}

// assignUnqueuedTask assigns a task that is not queued to a worker.
func (w *worker) assignUnqueuedTask(bq *InMemoryBuildQueue, t *task, stickinessRetained int) {
	if w.currentTask != nil {
		panic("Worker is already associated with a task")
	}
	if t.currentWorker != nil {
		panic("Task is already associated with a worker")
	}

	t.registerQueuedStageFinished(bq)
	w.currentTask = t
	t.currentWorker = w
	t.retryCount = 0
	for i := range t.operations {
		i.incrementExecutingWorkersCount(bq, w)
	}
	w.clearLastInvocation()
	for i := stickinessRetained; i < len(w.stickinessStartingTimes); i++ {
		w.stickinessStartingTimes[i] = bq.now
	}
}

// assignQueuedTask assigns a task that is queued to a worker. The task
// is unqueued in the process.
func (w *worker) assignQueuedTask(bq *InMemoryBuildQueue, t *task, stickinessRetained int) {
	w.assignUnqueuedTask(bq, t, stickinessRetained)

	for _, o := range t.operations {
		o.removeQueuedFromInvocation()
	}
	t.reportNonFinalStageChange()
}

// assignNextQueuedTask determines which queued task is the best
// candidate for execution and assigns it to the current task.
func (w *worker) assignNextQueuedTask(bq *InMemoryBuildQueue, scq *sizeClassQueue, workerID map[string]string) bool {
	lastInvocationKeys := w.lastInvocation.invocationKeys
	pq := scq.platformQueue
	workerInvocationStickinessLimits := pq.workerInvocationStickinessLimits
	stickinessStartingTimes := w.stickinessStartingTimes
	i := &scq.rootInvocation
	stickinessRetained := 0
	for {
		// Even though an invocation can both have directly
		// queued operations and queued children, it is uncommon
		// in practice. Don't bother making smart decisions
		// which to pick; always prefer directly queued
		// operations over queued children.
		if len(i.queuedOperations) > 0 {
			// One or more operations are enqueued in this
			// invocation directly. Pick the most preferable
			// operation.
			scq.workerInvocationStickinessRetained.Observe(float64(stickinessRetained))
			w.assignQueuedTask(bq, i.queuedOperations[0].task, stickinessRetained)
			return true
		} else if len(i.queuedChildren) > 0 {
			// One or more operations are enqueued in a
			// child invocation.
			//
			// Determine from which invocation we need to
			// extract an operation. We always want to pick
			// the one that has the fewest executing
			// operations (corrected for the priority). In
			// case of ties, we want to schedule the
			// invocation that is least recently used, so
			// that they are scheduled round robin.
			//
			// The exception to this rule is when worker
			// invocation stickiness is enabled. In that
			// case tie breaking gives a slight advantage to
			// any of the invocations associated with the
			// last executed task. This reduces the startup
			// overhead of actions that leave state behind
			// on the worker.
			iBest := i.queuedChildren[0]
			if len(lastInvocationKeys) > 0 && len(workerInvocationStickinessLimits) > 0 {
				iSticky := i.children[lastInvocationKeys[0]]
				if iSticky.isQueued() && iSticky.isPreferred(iBest, w.stickinessStartingTimes[0].Add(workerInvocationStickinessLimits[0]).After(bq.now)) {
					iBest = iSticky
				}
				if iBest == iSticky {
					// Continue to process stickiness
					// for child invocations.
					stickinessRetained++
					lastInvocationKeys = lastInvocationKeys[1:]
					workerInvocationStickinessLimits = workerInvocationStickinessLimits[1:]
					stickinessStartingTimes = stickinessStartingTimes[1:]
				} else {
					// Stop processing any further
					// stickiness, as we're going to
					// pick another invocation.
					lastInvocationKeys = nil
				}
			}
			i = iBest
		} else {
			// No queued operations available.
			return false
		}
	}
}

// clearLastInvocation clears the invocation of the last task to run on
// this worker. This method needs to be called when workers are removed
// to make sure that invocations don't leak.
func (w *worker) clearLastInvocation() {
	if w.wakeup != nil {
		panic("Clearing last invocations would make it impossible to dequeue the worker")
	}
	if iLast := w.lastInvocation; iLast != nil {
		for i := iLast; i != nil; i = i.parent {
			if i.idleWorkersCount == 0 {
				panic("Invalid workers count")
			}
			i.idleWorkersCount--
			i.removeIfEmpty()
		}
		w.lastInvocation = nil
	}
}

// setLastInvocation sets the invocation of the last task to run on this
// worker. This method needs to be called when execution of a task
// completes.
func (w *worker) setLastInvocation(iLast *invocation) {
	if w.lastInvocation != nil {
		panic("Executing worker cannot have a last invocation associated with it")
	}
	w.lastInvocation = iLast
	for i := iLast; i != nil; i = i.parent {
		i.idleWorkersCount++
	}
}

// assignUnqueuedTaskAndWakeUp is used to assign a task to an idle
// worker that is synchronizing against the scheduler and to wake it up.
// This method is used when clients directly assign a task to a worker,
// as opposed to letting the worker select a task itself.
func (w *worker) assignUnqueuedTaskAndWakeUp(bq *InMemoryBuildQueue, t *task, stickinessRetained int) {
	// Wake up the worker prior to assigning the task. Assigning
	// clears w.lastInvocations, which cannot be done while the
	// worker is queued.
	w.wakeUp(t.getCurrentSizeClassQueue())
	w.assignUnqueuedTask(bq, t, stickinessRetained)
}

// getExecutingSynchronizeResponse returns a synchronization response
// that instructs a worker to start executing a task.
func (w *worker) getExecutingSynchronizeResponse(bq *InMemoryBuildQueue) *remoteworker_pb.SynchronizeResponse {
	t := w.currentTask
	return &remoteworker_pb.SynchronizeResponse{
		NextSynchronizationAt: bq.getNextSynchronizationAtDelay(),
		DesiredState: &remoteworker_pb.DesiredState{
			WorkerState: &remoteworker_pb.DesiredState_Executing_{
				Executing: &t.desiredState,
			},
		},
	}
}

// getNextTask extracts the next queued task from the queue and assigns
// it to the current worker. Depending on whether a context object is
// provided, this function either blocks until work is available or
// returns immediately. When returning immediately, it instructs the
// worker to go idle.
func (w *worker) getNextTask(ctx context.Context, bq *InMemoryBuildQueue, scq *sizeClassQueue, workerID map[string]string, preferBeingIdle bool) (*remoteworker_pb.SynchronizeResponse, error) {
	if preferBeingIdle {
		// The worker wants to terminate or is experiencing some
		// issues. Explicitly instruct the worker to go idle, so
		// that it knows it can hold off synchronizing.
		return bq.getIdleSynchronizeResponse(), nil
	}

	isDrained := w.isDrained(scq, workerID)
	if !isDrained && w.assignNextQueuedTask(bq, scq, workerID) {
		return w.getExecutingSynchronizeResponse(bq), nil
	}

	if ctx == nil {
		// We shouldn't block, as the worker is currently doing
		// some work that it shouldn't be doing. Request that
		// the worker goes idle immediately. It will
		// resynchronize as soon as it's done terminating its
		// current build action.
		return bq.getIdleSynchronizeResponse(), nil
	}

	timeoutTimer, timeoutChannel := bq.clock.NewTimer(bq.configuration.GetIdleWorkerSynchronizationInterval())
	defer timeoutTimer.Stop()

	for {
		if isDrained {
			// The worker is drained. Simply wait until
			// undrain operations occur.
			undrainWakeup := scq.undrainWakeup
			bq.leave()

			select {
			case t := <-timeoutChannel:
				// Timeout has been reached.
				bq.enter(t)
				return bq.getIdleSynchronizeResponse(), nil
			case <-ctx.Done():
				// Worker has canceled the request.
				bq.enter(bq.clock.Now())
				return nil, util.StatusFromContext(ctx)
			case <-undrainWakeup:
				// Worker might have been undrained.
				bq.enter(bq.clock.Now())
			}
		} else if w.assignNextQueuedTask(bq, scq, workerID) {
			// One or more tasks were queued. We're able to
			// synchronize without blocking by starting one
			// of those tasks.
			return w.getExecutingSynchronizeResponse(bq), nil
		} else {
			// No queued tasks available. Queue the worker,
			// so that clients may assign a task to us.
			//
			// Place ourselves in the queue belonging to the
			// invocation of the last task that ran on this
			// worker, so that we're more likely to receive
			// tasks that share the same invocation keys
			// prefix.
			if w.wakeup != nil || w.listIndex != -1 {
				panic("Worker is already queued")
			}
			wakeup := make(chan struct{})
			w.wakeup = wakeup
			i := w.lastInvocation
			i.idleSynchronizingWorkers.enqueue(&idleSynchronizingWorker{
				worker:    w,
				listIndex: &w.listIndex,
			})
			for i.parent != nil {
				heapPushOrFix(&i.parent.idleSynchronizingWorkersChildren, i.idleSynchronizingWorkersChildrenIndex, i)
				i = i.parent
			}
			bq.leave()

			select {
			case t := <-timeoutChannel:
				// Timeout has been reached.
				bq.enter(t)
				w.maybeDequeue(scq)
				if w.currentTask != nil {
					return w.getExecutingSynchronizeResponse(bq), nil
				}
				return bq.getIdleSynchronizeResponse(), nil
			case <-ctx.Done():
				// Worker has canceled the request.
				bq.enter(bq.clock.Now())
				w.maybeDequeue(scq)
				return nil, util.StatusFromContext(ctx)
			case <-wakeup:
				// Worker got woken up, meaning a task
				// got assigned to it, or it got drained.
				bq.enter(bq.clock.Now())
				if w.currentTask != nil {
					return w.getExecutingSynchronizeResponse(bq), nil
				}
			}
		}
		isDrained = w.isDrained(scq, workerID)
	}
}

// getCurrentOrNextTask either returns a synchronization response that
// instructs the worker to run the task it should be running. When the
// worker has no task assigned to it, it attempts to request a task from
// the queue.
func (w *worker) getCurrentOrNextTask(ctx context.Context, bq *InMemoryBuildQueue, scq *sizeClassQueue, workerID map[string]string, preferBeingIdle bool) (*remoteworker_pb.SynchronizeResponse, error) {
	if t := w.currentTask; t != nil {
		if t.retryCount < bq.configuration.WorkerTaskRetryCount {
			t.retryCount++
			return &remoteworker_pb.SynchronizeResponse{
				NextSynchronizationAt: bq.getNextSynchronizationAtDelay(),
				DesiredState: &remoteworker_pb.DesiredState{
					WorkerState: &remoteworker_pb.DesiredState_Executing_{
						Executing: &t.desiredState,
					},
				},
			}, nil
		}
		t.fail(
			bq,
			"HitWorkerTaskRetryLimit",
			status.Errorf(
				codes.Internal,
				"Attempted to execute task %d times, but it never completed. This task may cause worker %s to crash.",
				t.retryCount+1,
				newWorkerKey(workerID),
			),
			/* completedByWorker = */ false,
		)
	}
	return w.getNextTask(ctx, bq, scq, workerID, preferBeingIdle)
}

// isRunningCorrectTask determines whether the worker is actually
// running the task the scheduler instructed it to run previously.
func (w *worker) isRunningCorrectTask(taskUUID string) bool {
	t := w.currentTask
	if t == nil {
		return false
	}
	return t.desiredState.TaskUuid == taskUUID
}

type idleSynchronizingWorker struct {
	worker    *worker
	listIndex *int
}

type idleSynchronizingWorkersList []idleSynchronizingWorker

func (l *idleSynchronizingWorkersList) enqueue(entry *idleSynchronizingWorker) {
	*entry.listIndex = len(*l)
	*l = append(*l, *entry)
}

func (l *idleSynchronizingWorkersList) dequeue(listIndex int) {
	w := (*l)[listIndex].worker
	(*l)[listIndex] = (*l)[len(*l)-1]
	*(*l)[listIndex].listIndex = listIndex
	w.listIndex = -1
	(*l)[len(*l)-1] = idleSynchronizingWorker{}
	(*l) = (*l)[:len(*l)-1]
}

// heapRemoveOrFix either calls heap.Remove() or heap.Fix(), depending
// on a provided counter value. This function may be used to correct the
// position of an object that contains a counter or list inside a heap,
// after the counter or list is mutated.
func heapRemoveOrFix(h heap.Interface, i, count int) {
	if count > 0 {
		heap.Fix(h, i)
	} else {
		heap.Remove(h, i)
	}
}

// heapPushOrFix either calls heap.Push() or heap.Fix(), depending on an
// existing heap index. This function can be used to push an object into
// a heap, only if it's not part of the heap already.
func heapPushOrFix(h heap.Interface, i int, v interface{}) {
	if i < 0 {
		heap.Push(h, v)
	} else {
		heap.Fix(h, i)
	}
}

// heapMaybeFix calls heap.Fix() only when an object is actually stored
// in the heap.
func heapMaybeFix(h heap.Interface, i int) {
	if i >= 0 {
		heap.Fix(h, i)
	}
}

// cleanupKey is a handle that is used by cleanupQueue to refer to
// scheduled cleanups. It can be used to cancel a cleanup task. The key
// refers to the index of the corresponding entry in the cleanupHeap.
// Keys are offset by one, so that a zero value indicates the key is not
// associated with any cleanup entry.
type cleanupKey int

func (k cleanupKey) isActive() bool {
	return k != 0
}

// cleanupHeap is an implementation of container.Heap for cleanupEntry
// objects. It ensures that the cleanupKeys remain in sync with the
// indices of the cleanupEntries.
type cleanupHeap []cleanupEntry

func (h cleanupHeap) Len() int {
	return len(h)
}

func (h cleanupHeap) Less(i, j int) bool {
	return h[i].timestamp.Before(h[j].timestamp)
}

func (h cleanupHeap) Swap(i, j int) {
	if *h[i].key != cleanupKey(i+1) || *h[j].key != cleanupKey(j+1) {
		panic("Invalid cleanup keys")
	}
	h[i], h[j] = h[j], h[i]
	*h[i].key = cleanupKey(i + 1)
	*h[j].key = cleanupKey(j + 1)
}

func (h *cleanupHeap) Push(x interface{}) {
	e := x.(cleanupEntry)
	if *e.key != 0 {
		panic("Cleanup key already in use")
	}
	*h = append(*h, e)
	*e.key = cleanupKey(len(*h))
}

func (h *cleanupHeap) Pop() interface{} {
	old := *h
	n := len(old)
	e := old[n-1]
	old[n-1] = cleanupEntry{}
	*h = old[:n-1]
	if *e.key != cleanupKey(n) {
		panic("Invalid cleanup key")
	}
	*e.key = 0
	return e
}

// cleanupEntry stores at what point in time a certain cleanup function
// needs to be executed.
type cleanupEntry struct {
	key       *cleanupKey
	timestamp time.Time
	callback  func()
}

// cleanupQueue is an event queue that keeps track of closures that need
// to be executed at some point in the future. This data structure is
// used by InMemoryBuildQueue to keep track of workers, platform queues,
// operations, etc. that need to be garbage collected.
//
// Every entry in cleanupQueue is associated with one cleanupKey. The
// cleanupKey can be used to cancel the execution of a cleanup function.
type cleanupQueue struct {
	heap cleanupHeap
}

func (q *cleanupQueue) add(key *cleanupKey, timestamp time.Time, callback func()) {
	if *key != 0 {
		panic("Cleanup key is already in use")
	}
	heap.Push(&q.heap, cleanupEntry{
		key:       key,
		timestamp: timestamp,
		callback:  callback,
	})
}

func (q *cleanupQueue) remove(key cleanupKey) {
	heap.Remove(&q.heap, int(key)-1)
}

func (q *cleanupQueue) run(now time.Time) {
	for len(q.heap) > 0 && !q.heap[0].timestamp.After(now) {
		heap.Pop(&q.heap).(cleanupEntry).callback()
	}
}

func (q *cleanupQueue) getTimestamp(key cleanupKey) *timestamppb.Timestamp {
	if key == 0 {
		return nil
	}
	return timestamppb.New(q.heap[key-1].timestamp)
}
