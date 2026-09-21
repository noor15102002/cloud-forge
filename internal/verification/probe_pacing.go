package verification

import (
	"context"
	"errors"
	"time"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

var errProbePacingInterrupted = errors.New("HTTP observation was interrupted before the paced request started")

// probePacer spaces actual HTTP request starts across the run's readiness and
// availability observers. One gate is shared between phases, so starting a new
// observer does not grant a new burst. Control requests and k6 are independent.
type probePacer struct {
	interval time.Duration
	gate     chan struct{}
	next     time.Time
}

func newProbePacer(interval time.Duration) *probePacer {
	p := &probePacer{interval: interval, gate: make(chan struct{}, 1)}
	p.gate <- struct{}{}
	return p
}

func (p *probePacer) run(ctx context.Context, request func(time.Time) (int, error)) (int, error) {
	select {
	case <-ctx.Done():
		return 0, errors.Join(errProbePacingInterrupted, ctx.Err())
	case <-p.gate:
	}
	defer func() { p.gate <- struct{}{} }()
	if delay := time.Until(p.next); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return 0, errors.Join(errProbePacingInterrupted, ctx.Err())
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, errors.Join(errProbePacingInterrupted, err)
	}
	started := time.Now()
	p.next = started.Add(p.interval)
	// Hold the gate through the request: a slow response cannot leave queued
	// interval tokens that would later be released as a burst.
	return request(started)
}

func (s *Service) performProbe(ctx context.Context, url string, onStart func(time.Time)) (int, error) {
	request := func(started time.Time) (int, error) {
		if onStart != nil {
			onStart(started)
		}
		return s.probe(ctx, url)
	}
	if s.probePacer != nil {
		return s.probePacer.run(ctx, request)
	}
	return request(time.Now())
}

func (s *Service) pacedProbe(ctx context.Context, url string) (int, error) {
	return s.performProbe(ctx, url, nil)
}

// Configuration is validated before runtime. A fresh gate is installed for
// each run and removed afterwards, including cancellation and startup failure.
func (s *Service) configureProbePacing(config model.RuntimeConfiguration) func() {
	originalPacer, originalPoll := s.probePacer, s.trafficPoll
	if config.Probes != nil {
		interval, _ := time.ParseDuration(config.Probes.Interval)
		s.probePacer = newProbePacer(interval)
		s.trafficPoll = interval
	}
	return func() { s.probePacer, s.trafficPoll = originalPacer, originalPoll }
}

func lifecycleFinalHealthUnobserved(id, title, code string, duration int64, traffic trafficObservation, measurements []model.Measurement) recoveryOutcome {
	result := lifecycleExecutionError(id, title, code,
		"The final HTTP health request could not start within its observation deadline; no HTTP failure is inferred from the pacing wait.",
		"Inspect the configured probe interval and the completed observations; collected measurements remain evidence.", traffic)
	result.Evidence.DurationMS = duration
	result.Evidence.Measurements = measurements
	return result
}
