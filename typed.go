package metis

import (
	"context"
	"fmt"
)

// TypedTask is an [ExternalTask] whose variables have already been decoded into
// a struct of your own. The task itself is embedded, so ID, Topic, Retries and
// the rest are reachable as usual:
//
//	func(ctx context.Context, task *metis.TypedTask[ChargeInput]) (ChargeResult, error) {
//		log.Println("charging for task", task.ID, "amount", task.Input.Amount)
//	}
type TypedTask[In any] struct {
	*ExternalTask

	// Input is the task's variables decoded into In. Variables the process does
	// not carry leave their fields at the zero value, so a missing number is a
	// 0 rather than an error — validate what you require.
	Input In
}

// TypedHandler does the work of one external task against decoded input, and
// returns a value that becomes the variables written back.
//
// The idempotency requirement of [Handler] applies unchanged: the engine
// re-dispatches a task whose lock expired, and a handler whose work succeeded
// but whose report-back failed sees the same task again.
type TypedHandler[In, Out any] func(ctx context.Context, task *TypedTask[In]) (Out, error)

// NewTypedWorker is [NewWorker] with the variable map decoded for you. It is
// the same worker — same polling, same lock budget, same error handling — with
// the marshalling moved off your handler:
//
//	type ChargeInput struct {
//		ChargeID string  `json:"chargeID"`
//		Amount   float64 `json:"amount"`
//	}
//	type ChargeResult struct {
//		Reversed bool `json:"reversed"`
//	}
//
//	worker := metis.NewTypedWorker(client, "reverse-charge", workerID,
//		metis.WorkerOptions{},
//		func(ctx context.Context, task *metis.TypedTask[ChargeInput]) (ChargeResult, error) {
//			if err := payments.Refund(ctx, task.Input.ChargeID); err != nil {
//				return ChargeResult{}, err
//			}
//			return ChargeResult{Reversed: true}, nil
//		})
//	err := worker.Run(ctx)
//
// Both type parameters are inferred from the handler, so they are rarely worth
// writing out. Out may be [Variables] when the shape of the result is decided
// at runtime; it is passed through rather than round-tripped.
//
// It returns a plain [*Worker], so OnError and Run behave identically and a
// codebase can mix typed and untyped workers.
//
// # When the input does not fit
//
// A task whose variables cannot be decoded into In fails the task rather than
// the worker — the same path as a handler returning an error, so it retries and
// then lands for an operator. That is deliberate: variables that do not match
// what the topic's worker expects is a real incident in the process, not a blip,
// and retrying will not fix it. The error names the topic and the type, because
// the cause is usually a diagram and a worker that have drifted apart.
//
// If you would rather tolerate a loose shape, take [Variables] as In: it decodes
// from any object, and the accessors are there for reading it.
func NewTypedWorker[In, Out any](
	client *Client,
	topic, workerID string,
	opts WorkerOptions,
	handler TypedHandler[In, Out],
) *Worker {
	return NewWorker(client, topic, workerID, opts,
		func(ctx context.Context, task *ExternalTask) (Variables, error) {
			var input In
			if err := task.Variables.Decode(&input); err != nil {
				return nil, fmt.Errorf("task %s on %q carries variables that do not fit %T: %w",
					task.ID, topic, input, err)
			}

			output, err := handler(ctx, &TypedTask[In]{ExternalTask: task, Input: input})
			if err != nil {
				return nil, err
			}

			vars, err := VariablesOf(output)
			if err != nil {
				return nil, fmt.Errorf("task %s on %q: result: %w", task.ID, topic, err)
			}
			return vars, nil
		})
}
