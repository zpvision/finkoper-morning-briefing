package briefing

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"
)

// Run is called exactly once by the process owner. Database ownership is held for its entire lifetime.
func Run(ctx context.Context, s *Store, o *Ollama) error {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		j, in, err := s.claim(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
				continue
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return errors.New("worker_claim_failed")
		}
		start := time.Now()
		wait := start.Sub(j.Created).Milliseconds()
		slog.Info("job_running", "job_id", j.ID, "queue_wait_ms", wait)
		var result *Result
		var metrics Metrics
		if j.PromptVersion != PromptVersion || j.SchemaVersion != SchemaVersion {
			err = errors.New("unsupported_job_version")
		} else {
			result, metrics, err = o.Generate(ctx, in)
		}
		elapsed := time.Since(start).Milliseconds()
		metrics.QueueWait = &wait
		metrics.Processing = &elapsed
		j.Metrics = metrics
		// Shutdown cancellation leaves running persisted; startup requeues it. No retry in this process.
		if ctx.Err() != nil {
			return nil
		}
		j.Status = "succeeded"
		j.Result = result
		if err != nil {
			j.Status = "failed"
			msg := err.Error()
			j.Error = &msg
			j.Result = nil
		}
		saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		saveErr := s.finish(saveCtx, j)
		cancel()
		if saveErr != nil {
			return errors.New("worker_persist_failed")
		}
		attrs := []any{"job_id", j.ID, "status", j.Status, "processing_ms", elapsed}
		if metrics.BudgetHelper != nil {
			attrs = append(attrs, "budget_helper_ms", *metrics.BudgetHelper)
		}
		if metrics.CompatibilityCheck != nil {
			attrs = append(attrs, "compatibility_check_ms", *metrics.CompatibilityCheck)
		}
		slog.Info("job_finished", attrs...)
	}
}
