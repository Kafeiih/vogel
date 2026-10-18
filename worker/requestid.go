package worker

import (
	"context"
	"encoding/json"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/kafeiih/vogel/reqctx"
)

// requestIDMetadataKey is the job metadata key the request ID travels under.
// Prefixed so it cannot collide with metadata a consumer sets through
// river.InsertOpts.Metadata.
const requestIDMetadataKey = "vogel_request_id"

// requestIDMiddleware carries the request ID from the process that enqueues a
// job to the one that works it, through River's job metadata rather than the
// job args, so no JobArgs type has to declare it.
//
// On insert it stamps the reqctx request ID of the enqueuing context onto
// every job. On work it puts that ID back into the job's context with
// reqctx.WithRequestID, so logger's *Context calls and audit rows recorded by
// the worker carry the request_id of the request that caused the job.
//
// It is best effort on both sides: with no request ID in the context (a
// periodic job, a CLI) or metadata it cannot decode, the job is inserted and
// worked unchanged.
type requestIDMiddleware struct {
	river.MiddlewareDefaults
}

var (
	_ rivertype.JobInsertMiddleware = (*requestIDMiddleware)(nil)
	_ rivertype.WorkerMiddleware    = (*requestIDMiddleware)(nil)
)

func (m *requestIDMiddleware) InsertMany(ctx context.Context, manyParams []*rivertype.JobInsertParams, doInner func(context.Context) ([]*rivertype.JobInsertResult, error)) ([]*rivertype.JobInsertResult, error) {
	if id := reqctx.RequestIDFromContext(ctx); id != "" {
		for _, p := range manyParams {
			p.Metadata = withRequestID(p.Metadata, id)
		}
	}
	return doInner(ctx)
}

func (m *requestIDMiddleware) Work(ctx context.Context, job *rivertype.JobRow, doInner func(context.Context) error) error {
	if id := requestIDFromMetadata(job.Metadata); id != "" {
		ctx = reqctx.WithRequestID(ctx, id)
	}
	return doInner(ctx)
}

// withRequestID returns metadata with the request ID added. Metadata that is
// not a JSON object, or that already carries the key (set explicitly through
// river.InsertOpts.Metadata), is returned unchanged: an explicit value wins,
// as it does in logger.
func withRequestID(metadata []byte, id string) []byte {
	fields := map[string]json.RawMessage{}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &fields); err != nil || fields == nil {
			return metadata
		}
	}

	if _, ok := fields[requestIDMetadataKey]; ok {
		return metadata
	}

	encodedID, err := json.Marshal(id)
	if err != nil {
		return metadata
	}
	fields[requestIDMetadataKey] = encodedID

	out, err := json.Marshal(fields)
	if err != nil {
		return metadata
	}
	return out
}

// requestIDFromMetadata returns the request ID stored by withRequestID, or ""
// when there is none.
func requestIDFromMetadata(metadata []byte) string {
	if len(metadata) == 0 {
		return ""
	}
	var fields struct {
		RequestID string `json:"vogel_request_id"`
	}
	if err := json.Unmarshal(metadata, &fields); err != nil {
		return ""
	}
	return fields.RequestID
}
