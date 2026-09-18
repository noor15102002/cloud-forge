package analyzer

import (
	"encoding/json"
	"sort"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

// Field names suffice to reject unsupported semantics; no environment, command,
// annotation or other arbitrary source values are retained in the public model.
func unsupportedDeploymentFields(raw []byte, deployment *model.Deployment) {
	var document map[string]json.RawMessage
	if json.Unmarshal(raw, &document) != nil {
		return
	}
	var spec map[string]json.RawMessage
	_ = json.Unmarshal(document["spec"], &spec)
	checkFields(spec, "spec", []string{"replicas", "selector", "template", "strategy", "minReadySeconds", "progressDeadlineSeconds"}, &deployment.Unsupported)
	var template map[string]json.RawMessage
	_ = json.Unmarshal(spec["template"], &template)
	var metadata map[string]json.RawMessage
	_ = json.Unmarshal(template["metadata"], &metadata)
	checkFields(metadata, "spec.template.metadata", []string{"labels", "creationTimestamp"}, &deployment.Unsupported)
	var pod map[string]json.RawMessage
	_ = json.Unmarshal(template["spec"], &pod)
	checkFields(pod, "spec.template.spec", []string{"containers", "terminationGracePeriodSeconds"}, &deployment.Unsupported)
	var containers []map[string]json.RawMessage
	_ = json.Unmarshal(pod["containers"], &containers)
	for _, container := range containers {
		checkFields(container, "spec.template.spec.containers[]", []string{"name", "image", "imagePullPolicy", "ports", "resources", "readinessProbe", "livenessProbe", "startupProbe"}, &deployment.Unsupported)
		var ports []map[string]json.RawMessage
		_ = json.Unmarshal(container["ports"], &ports)
		for _, port := range ports {
			checkFields(port, "spec.template.spec.containers[].ports[]", []string{"name", "containerPort", "protocol"}, &deployment.Unsupported)
		}
		var resources map[string]json.RawMessage
		_ = json.Unmarshal(container["resources"], &resources)
		checkFields(resources, "spec.template.spec.containers[].resources", []string{"requests", "limits"}, &deployment.Unsupported)
	}
	sort.Strings(deployment.Unsupported)
	unique := deployment.Unsupported[:0]
	for _, name := range deployment.Unsupported {
		if len(unique) == 0 || unique[len(unique)-1] != name {
			unique = append(unique, name)
		}
	}
	deployment.Unsupported = unique
}

func checkFields(object map[string]json.RawMessage, prefix string, allowed []string, unsupported *[]string) {
	for name := range object {
		known := false
		for _, field := range allowed {
			if name == field {
				known = true
				break
			}
		}
		if !known {
			*unsupported = append(*unsupported, prefix+"."+name)
		}
	}
}
