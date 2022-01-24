package workflow

import (
	"log"
	"time"

	a "github.com/cschleiden/go-dt/internal/args"
	"github.com/cschleiden/go-dt/internal/command"
	"github.com/cschleiden/go-dt/internal/converter"
	"github.com/cschleiden/go-dt/internal/fn"
	"github.com/cschleiden/go-dt/internal/payload"
	"github.com/cschleiden/go-dt/internal/sync"
	"github.com/pkg/errors"
)

type Activity interface{}

type ActivityOptions struct {
	RetryOptions RetryOptions
}

var DefaultActivityOptions = ActivityOptions{
	RetryOptions: DefaultRetryOptions,
}

// ExecuteActivity schedules the given activity to be executed
func ExecuteActivity(ctx sync.Context, options ActivityOptions, activity Activity, args ...interface{}) sync.Future {
	r := sync.NewFuture()

	sync.Go(ctx, func(ctx sync.Context) {
		for attempt := 0; attempt < options.RetryOptions.MaxAttempts; attempt++ {
			var result payload.Payload
			f := executeActivity(ctx, options, activity, args...)

			err := f.Get(ctx, &result)
			if err != nil {
				log.Println("Activity error", err)

				backoffDuration := time.Second * 2 // TODO
				Sleep(ctx, backoffDuration)

				continue
			}

			// TODO: Set raw value?
			r.Set(result, err)
			return
		}
	})

	return r
}

func executeActivity(ctx sync.Context, options ActivityOptions, activity Activity, args ...interface{}) sync.Future {
	f := sync.NewFuture()

	inputs, err := a.ArgsToInputs(converter.DefaultConverter, args...)
	if err != nil {
		f.Set(nil, errors.Wrap(err, "failed to convert activity input"))
		return f
	}

	wfState := getWfState(ctx)
	eventID := wfState.eventID
	wfState.eventID++

	name := fn.Name(activity)
	cmd := command.NewScheduleActivityTaskCommand(eventID, name, inputs)
	wfState.addCommand(&cmd)

	wfState.pendingFutures[eventID] = f

	// Handle cancellation
	if d := ctx.Done(); d != nil {
		if c, ok := d.(sync.ChannelInternal); ok {
			c.ReceiveNonBlocking(ctx, func(_ interface{}) {
				// Workflow has been canceled, check if the activity has already been scheduled
				if cmd.State == command.CommandState_Committed {
					// Command has already been committed, that means the activity has already been scheduled. Wait
					// until the activity is done.
					return
				}

				wfState.removeCommand(cmd)
				delete(wfState.pendingFutures, eventID)
				f.Set(nil, sync.Canceled)
			})
		}
	}

	return f
}
