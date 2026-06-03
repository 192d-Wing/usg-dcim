// arq job enqueuer. Ports the wire format Python's arq.connections.ArqRedis
// emits — a msgpack-encoded `{t, f, a, k, et}` dict at key
// `arq:job:<job_id>` with TTL, then a ZADD onto the `arq:queue` sorted
// set keyed by the same job_id with score=enqueue_time_ms. The Python
// worker reads the same shape from the same keys.
//
// Why msgpack: arq's default serializer is pickle, which has no clean
// cross-language port. We override `job_serializer` + `job_deserializer`
// on the Python WorkerSettings to use msgpack (see
// packages/otter/src/dcim/worker.py:WorkerSettings) so the Go enqueuer
// and the Python worker share a single wire on both sides.
package regiondeploy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/vmihailenco/msgpack/v5"
)

// ArqEnqueuer is the slim subset of *redis.Client the /start handler
// uses to push an arq job. Tests inject a fake that captures the
// SETEX + ZADD calls; production wires the real Redis client.
type ArqEnqueuer interface {
	EnqueueArqJob(ctx context.Context, queueName, functionName string, args []any) (jobID string, err error)
}

// arqRedisEnqueuer is the production implementation. Holds a Redis
// client and the TTL Python's arq pool defaults to (24 hours plus the
// score delta, but we always enqueue with score=now so it collapses
// to the 24h "expires_extra_ms" constant).
type arqRedisEnqueuer struct {
	c *redis.Client
}

// NewArqRedisEnqueuer wraps a *redis.Client to satisfy ArqEnqueuer.
// Exposed so main.go can wire production without exporting the inner
// type.
func NewArqRedisEnqueuer(c *redis.Client) ArqEnqueuer { return &arqRedisEnqueuer{c: c} }

// arq's expires_extra_ms default: 24 hours. Without _expires set on
// the enqueue side, arq computes expires = score - enqueue_time +
// extra_ms; with score == enqueue_time that collapses to extra_ms.
const arqExpiresExtraMs int64 = 24 * 60 * 60 * 1000

// EnqueueArqJob pushes one arq job. Returns the generated job_id so
// callers can include it in audit + log fields. Matches arq's
// `_enqueue_job` semantics: SETEX the job-record key, ZADD it onto the
// queue, all inside a Redis transaction so a partial failure doesn't
// leave a phantom record or queue entry.
func (e *arqRedisEnqueuer) EnqueueArqJob(ctx context.Context, queueName, functionName string, args []any) (string, error) {
	jobID, err := newArqJobID()
	if err != nil {
		return "", err
	}
	now := time.Now().UnixMilli()
	payload, err := msgpack.Marshal(arqJobPayload{
		Try:         nil,
		Function:    functionName,
		Args:        args,
		Kwargs:      map[string]any{},
		EnqueueTime: now,
	})
	if err != nil {
		return "", fmt.Errorf("marshal arq job: %w", err)
	}
	expiresMs := arqExpiresExtraMs
	jobKey := "arq:job:" + jobID
	pipe := e.c.TxPipeline()
	pipe.Set(ctx, jobKey, payload, time.Duration(expiresMs)*time.Millisecond)
	pipe.ZAdd(ctx, queueName, redis.Z{Score: float64(now), Member: jobID})
	if _, err := pipe.Exec(ctx); err != nil {
		return "", fmt.Errorf("redis exec: %w", err)
	}
	return jobID, nil
}

// arqJobPayload is the dict shape arq's serialize_job produces:
// `{'t': job_try, 'f': function, 'a': args, 'k': kwargs, 'et':
// enqueue_time_ms}`. msgpack maps Go struct field tags to map keys so
// the on-wire bytes match Python's msgpack.packb of the same dict.
type arqJobPayload struct {
	Try         *int           `msgpack:"t"`
	Function    string         `msgpack:"f"`
	Args        []any          `msgpack:"a"`
	Kwargs      map[string]any `msgpack:"k"`
	EnqueueTime int64          `msgpack:"et"`
}

// newArqJobID returns a hex-encoded 16-byte random id, matching arq's
// `uuid4().hex` (32 lowercase hex chars). uuid.New().String() would
// produce the dashed form which arq's job_key lookups would miss.
func newArqJobID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
