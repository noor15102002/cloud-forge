package verification

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestProbePacingSharesOneGateAcrossObservers(t *testing.T) {
	s := New(nil)
	s.probePacer = newProbePacer(20 * time.Millisecond)
	s.probe = func(context.Context, string) (int, error) { return http.StatusOK, nil }
	var starts []time.Time
	var mu sync.Mutex
	var group sync.WaitGroup
	for range 4 {
		group.Go(func() {
			_, err := s.performProbe(context.Background(), "http://unused/ready", func(at time.Time) {
				mu.Lock()
				starts = append(starts, at)
				mu.Unlock()
			})
			if err != nil {
				t.Errorf("probe: %v", err)
			}
		})
	}
	group.Wait()
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < 20*time.Millisecond {
			t.Fatalf("request starts only %s apart", gap)
		}
	}
}

func TestProbePacingCancellationDoesNotStartOrCountARequest(t *testing.T) {
	s := New(nil)
	s.probePacer = newProbePacer(time.Second)
	var calls atomic.Int32
	s.probe = func(context.Context, string) (int, error) { calls.Add(1); return http.StatusOK, nil }
	if _, err := s.pacedProbe(context.Background(), "http://unused/ready"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	started := make(chan time.Time, 1)
	before := time.Now()
	observed := s.collectTraffic(ctx, "http://unused/ready", make(chan trafficSample, 1), started)
	if calls.Load() != 1 || len(started) != 0 || observed.Requests != 0 || observed.Failures != 0 || len(observed.Samples) != 0 {
		t.Fatalf("a canceled wait became a request: calls=%d starts=%d observation=%+v", calls.Load(), len(started), observed)
	}
	if time.Since(before) > 500*time.Millisecond {
		t.Fatal("cancellation waited for the configured interval")
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	readiness := s.waitForHTTP(ctx2, "http://unused/ready")
	if readiness.Attempts != 0 || readiness.Failures != 0 || calls.Load() != 1 {
		t.Fatalf("canceled readiness counted an unissued request: %+v", readiness)
	}
}

func TestProbePacingSlowRequestDoesNotAccumulateBurstTokens(t *testing.T) {
	p := newProbePacer(20 * time.Millisecond)
	var starts []time.Time
	for i := range 3 {
		_, err := p.run(context.Background(), func(at time.Time) (int, error) {
			starts = append(starts, at)
			if i == 0 {
				time.Sleep(60 * time.Millisecond)
			}
			return http.StatusOK, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if starts[2].Sub(starts[1]) < 20*time.Millisecond {
		t.Fatal("a slow request accumulated a later burst")
	}
}

func TestProbePacingConfigurationRestoresServiceState(t *testing.T) {
	s := New(nil)
	original := s.trafficPoll
	config := model.RuntimeConfiguration{Probes: &model.ProbeSettings{Interval: "2s"}}
	restore := s.configureProbePacing(config)
	if s.probePacer == nil || s.trafficPoll != 2*time.Second {
		t.Fatal("configured interval was not installed")
	}
	restore()
	if s.probePacer != nil || s.trafficPoll != original {
		t.Fatal("configured interval leaked into a later run")
	}
	restore = s.configureProbePacing(model.RuntimeConfiguration{})
	defer restore()
	if s.probePacer != nil || s.trafficPoll != original {
		t.Fatal("omission changed the historical probe policy")
	}
}

func TestProbePacingPreservesRateLimitFailuresAndAllowsSlowerObservation(t *testing.T) {
	for _, paced := range []bool{false, true} {
		t.Run(map[bool]string{false: "original-policy", true: "explicit-policy"}[paced], func(t *testing.T) {
			var mu sync.Mutex
			var previous time.Time
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				now := time.Now()
				status := http.StatusOK
				if !previous.IsZero() && now.Sub(previous) < 35*time.Millisecond {
					status = http.StatusTooManyRequests
				}
				previous = now
				w.WriteHeader(status)
			}))
			defer server.Close()
			s := New(nil)
			if paced {
				s.probePacer = newProbePacer(60 * time.Millisecond)
				s.trafficPoll = 60 * time.Millisecond
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			samples := make(chan trafficSample, 1)
			done := make(chan trafficObservation, 1)
			go func() { done <- s.collectTraffic(ctx, server.URL, samples, nil) }()
			for range 4 {
				select {
				case <-samples:
				case <-time.After(3 * time.Second):
					t.Fatal("sampler failed to produce bounded observations")
				}
			}
			cancel()
			result := <-done
			if paced {
				if result.Failures != 0 || result.PollMS != 60 {
					t.Fatalf("explicit pacing did not respect the endpoint: %+v", result)
				}
			} else if result.HTTPStatuses[http.StatusTooManyRequests] == 0 || result.Failures == 0 {
				t.Fatalf("rate-limit responses were lost: %+v", result)
			}
		})
	}
}

func TestProbePacingReturnsOriginalRequestError(t *testing.T) {
	p := newProbePacer(20 * time.Millisecond)
	expected := errors.New("test request failure")
	status, err := p.run(context.Background(), func(time.Time) (int, error) { return 429, expected })
	if status != 429 || !errors.Is(err, expected) {
		t.Fatal("pacing changed the observed status or failure")
	}
}

func TestProbePacingInterruptedWaitIsAnUnissuedObservation(t *testing.T) {
	p := newProbePacer(time.Second)
	p.next = time.Now().Add(time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.run(ctx, func(time.Time) (int, error) {
		t.Fatal("canceled pacing started an HTTP request")
		return 0, nil
	})
	if !errors.Is(err, errProbePacingInterrupted) || !errors.Is(err, context.Canceled) {
		t.Fatalf("unissued observation lost its distinct cause: %v", err)
	}
}
