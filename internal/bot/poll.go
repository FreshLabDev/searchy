package bot

import (
	"context"
	"sync"
	"time"

	"github.com/FreshLabDev/tg"
)

const (
	// pollSeconds is Telegram's own long-poll duration. The client derives its
	// HTTP deadline from it, so a slow network cannot cut a poll short.
	pollSeconds = 25
	// defaultWorkers bounds how many updates are handled at once.
	defaultWorkers = 32
	// pollBackoffStep is added per consecutive failure, and maxPollBackoff caps
	// the result: a sustained outage should not hammer the network, or the
	// logs, every second.
	pollBackoffStep = time.Second
	maxPollBackoff  = 30 * time.Second
)

func updateWorkers(n int) int {
	if n < 1 {
		return defaultWorkers
	}
	return n
}

// Run long-polls Telegram and dispatches updates until ctx is cancelled.
//
// Updates are handled concurrently rather than in arrival order, because the
// inline path deliberately blocks: the debouncer holds a keystroke for its
// window and then abandons it if a newer one arrived, which only works when
// both are in flight at once. Handling one update at a time would turn every
// keystroke into a full search and stall the poll for the debounce delay.
//
// The offset therefore advances as soon as an update is dispatched, not once
// it is finished. Confirming work that is still running loses an update if the
// process dies mid-flight; the alternative is a poll loop held hostage by a
// query the debouncer is deliberately doing nothing about. Searchy's work is a
// stateless search with no bookkeeping to reconcile, so a lost update costs a
// user one retyped query -- which is what the previous dispatcher did too.
func (h *Handlers) Run(ctx context.Context) error {
	backoffStep := h.pollBackoffStep
	if backoffStep <= 0 {
		backoffStep = pollBackoffStep
	}
	slots := make(chan struct{}, updateWorkers(h.workers))
	var inFlight sync.WaitGroup
	// Handlers write to Telegram and to the analytics store; let them finish
	// before the caller tears those down.
	defer inFlight.Wait()

	var offset int64
	var pollFailures int
	for ctx.Err() == nil {
		updates, err := h.api.GetUpdates(ctx, offset, pollSeconds)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			pollFailures++
			delay := min(time.Duration(pollFailures)*backoffStep, maxPollBackoff)
			// The same error repeats for as long as the outage lasts: log the
			// first few, then sample.
			if pollFailures <= 3 || pollFailures%10 == 0 {
				h.log.Error("telegram polling failed", "err", err, "consecutive_failures", pollFailures, "retry_in", delay)
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(delay):
			}
			continue
		}
		if pollFailures > 0 {
			h.log.Info("telegram polling recovered", "after_failures", pollFailures)
			pollFailures = 0
		}
		for _, update := range updates {
			offset = update.UpdateID + 1
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return nil
			}
			inFlight.Add(1)
			go func(update tg.Update) {
				defer inFlight.Done()
				defer func() { <-slots }()
				h.Route(ctx, &update)
			}(update)
		}
	}
	return nil
}
