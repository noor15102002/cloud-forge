package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	appsv1 "k8s.io/api/apps/v1"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// DeploymentState retains only the identity and rollout state needed for baseline validation.
type DeploymentState struct {
	UID                string
	Image              string
	Replicas           int32
	Generation         int64
	ObservedGeneration int64
	Revision           string
}

// ObserveDeployment reads the controller's desired and observed baseline.
func (c *Client) ObserveDeployment(ctx context.Context, cluster, namespace, name string) (DeploymentState, model.CommandResult, error) {
	result := c.runner.Run(ctx, command.Request{Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "--namespace", namespace, "get", "deployment", name, "--output", "json"}, Timeout: 15 * time.Second, OutputLimit: 128 * 1024})
	var state DeploymentState
	if result.FailureType != model.FailureNone || result.ExitCode != 0 {
		return state, result, nil
	}
	if result.Truncated {
		return state, result, errors.New("deployment observation exceeded its bound")
	}
	var deployment appsv1.Deployment
	if err := json.Unmarshal([]byte(result.Stdout), &deployment); err != nil {
		return state, result, errors.New("invalid deployment observation")
	}
	state = DeploymentState{UID: string(deployment.UID), Generation: deployment.Generation, ObservedGeneration: deployment.Status.ObservedGeneration, Revision: deployment.Annotations["deployment.kubernetes.io/revision"]}
	if deployment.Spec.Replicas != nil {
		state.Replicas = *deployment.Spec.Replicas
	}
	if len(deployment.Spec.Template.Spec.Containers) == 1 {
		state.Image = deployment.Spec.Template.Spec.Containers[0].Image
	}
	return state, result, nil
}

// RevisionReplicaSets returns controller-owned ReplicaSets at the observed revision.
func (c *Client) RevisionReplicaSets(ctx context.Context, cluster, namespace, selector string, deployment DeploymentState) (map[string]bool, model.CommandResult, error) {
	result := c.runner.Run(ctx, command.Request{Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "--namespace", namespace, "get", "replicasets", "--selector", selector, "--output", "json"}, Timeout: 15 * time.Second, OutputLimit: 256 * 1024})
	if result.FailureType != model.FailureNone || result.ExitCode != 0 {
		return nil, result, nil
	}
	if result.Truncated {
		return nil, result, errors.New("replica set observation exceeded its bound")
	}
	var list appsv1.ReplicaSetList
	if json.Unmarshal([]byte(result.Stdout), &list) != nil {
		return nil, result, errors.New("invalid replica set observation")
	}
	uids := map[string]bool{}
	for _, rs := range list.Items {
		owned := false
		for _, ref := range rs.OwnerReferences {
			if ref.Controller != nil && *ref.Controller && string(ref.UID) == deployment.UID && ref.Kind == "Deployment" {
				owned = true
			}
		}
		if owned && deployment.UID != "" && deployment.Revision != "" && rs.Annotations["deployment.kubernetes.io/revision"] == deployment.Revision && len(rs.Spec.Template.Spec.Containers) == 1 && rs.Spec.Template.Spec.Containers[0].Image == deployment.Image {
			uids[string(rs.UID)] = true
		}
	}
	return uids, result, nil
}

// DeleteHPA restores fixed test replicas by removing only this run's named autoscaler.
func (c *Client) DeleteHPA(ctx context.Context, cluster, namespace, name string) model.CommandResult {
	return c.runner.Run(ctx, command.Request{Name: "kubectl", Args: []string{"--context", "k3d-" + cluster, "--namespace", namespace, "delete", "horizontalpodautoscaler", name, "--ignore-not-found=true", "--wait=true", "--timeout=30s"}, Timeout: 35 * time.Second, OutputLimit: 16 * 1024})
}
