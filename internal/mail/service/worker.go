package service

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/mail/repository"
)

type Sender interface {
	SendVerification(context.Context, string, string, time.Time) error
}

type WorkerConfig struct {
	PollInterval time.Duration
	Lease        time.Duration
	MaxAttempts  int
	Retention    time.Duration
}

type Worker struct {
	outbox repository.Outbox
	queue  *Queue
	sender Sender
	config WorkerConfig
	logger *slog.Logger
}

func NewWorker(outbox repository.Outbox, queue *Queue, sender Sender, config WorkerConfig, logger *slog.Logger) *Worker {
	return &Worker{outbox: outbox, queue: queue, sender: sender, config: config, logger: logger}
}

func (w *Worker) Run(ctx context.Context) {
	cleanup := time.NewTicker(24 * time.Hour)
	defer cleanup.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-cleanup.C:
			if err := w.outbox.Cleanup(ctx, w.config.Retention); err != nil {
				w.logger.ErrorContext(ctx, "очистка очереди писем", "error", err)
			}
		default:
		}
		processed, err := w.RunOnce(ctx)
		if err != nil {
			w.logger.ErrorContext(ctx, "обработка очереди писем", "error", err)
			processed = false
		}
		if processed {
			continue
		}
		timer := time.NewTimer(w.config.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	job, err := w.outbox.Claim(ctx, w.config.Lease)
	if err != nil || job == nil {
		return false, err
	}
	token, err := w.queue.decrypt(job.IdempotencyKey, job.Recipient, job.Ciphertext, job.Nonce)
	if err == nil {
		err = w.sender.SendVerification(ctx, job.Recipient, token, job.ExpiresAt)
	}
	if err != nil {
		delay := retryDelay(job.Attempts)
		markErr := w.outbox.MarkFailed(ctx, job.ID, job.Attempts, delay, err.Error())
		w.logger.ErrorContext(ctx, "не удалось доставить письмо", "job_id", job.ID, "attempt", job.Attempts, "error", err)
		if markErr != nil {
			return true, markErr
		}
		return true, nil
	}
	if err := w.outbox.MarkSent(ctx, job.ID); err != nil {
		return true, err
	}
	return true, nil
}

func retryDelay(attempt int) time.Duration {
	seconds := math.Pow(2, float64(attempt))
	if seconds > 900 {
		seconds = 900
	}
	return time.Duration(seconds * float64(time.Second))
}
