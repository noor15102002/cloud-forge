package verification

import (
	"context"
	"errors"
	"time"

	"github.com/noor15102002/cloud-forge/internal/executor/kubernetes"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// deletePodObserved samples readiness while kubectl waits for deletion. The
// minimum is an observed sample minimum, never a continuous availability proof.
// The command is joined before returning, including observation errors/cancel.
func (s *Service) deletePodObserved(ctx context.Context, client *kubernetes.Client, current plan, selector, podName string, minimum int) (model.CommandResult, int, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan model.CommandResult, 1)
	go func() { done <- client.DeletePod(ctx, current.clusterName, namespace, podName) }()
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	for {
		pods, result, err := client.ObservePods(ctx, current.clusterName, namespace, selector)
		if err != nil || failed(result) {
			cancel()
			return <-done, minimum, errors.New("pod readiness could not be sampled reliably")
		}
		ready, _, _ := summarizePods(pods)
		minimum = min(minimum, ready)
		select {
		case result := <-done:
			return result, minimum, nil
		case <-ctx.Done():
			cancel()
			return <-done, minimum, ctx.Err()
		case <-ticker.C:
		}
	}
}
