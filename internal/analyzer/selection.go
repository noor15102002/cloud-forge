package analyzer

import (
	"path"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func rebaseSources(result *model.AnalysisResult, app string) {
	rebase := func(source *model.SourceReference) {
		if source != nil && source.Path != "" {
			source.Path = path.Join(app, source.Path)
		}
	}
	container := func(value *model.Container) {
		rebase(&value.Source)
		for i := range value.Ports {
			rebase(&value.Ports[i].Source)
		}
	}
	for i := range result.Diagnostics {
		rebase(result.Diagnostics[i].Source)
	}
	application := &result.Application
	for i := range application.Runtimes {
		rebase(&application.Runtimes[i].Source)
	}
	for i := range application.Dependencies {
		rebase(&application.Dependencies[i].Source)
	}
	for i := range application.Containers {
		container(&application.Containers[i])
	}
	k := &application.Kubernetes
	for i := range k.Deployments {
		value := &k.Deployments[i]
		rebase(&value.Source)
		for j := range value.Containers {
			container(&value.Containers[j])
		}
		for j := range value.Endpoints {
			rebase(&value.Endpoints[j].Source)
		}
	}
	for i := range k.Services {
		rebase(&k.Services[i].Source)
	}
	for i := range k.HorizontalPodScalers {
		rebase(&k.HorizontalPodScalers[i].Source)
	}
	for i := range k.OtherResources {
		rebase(&k.OtherResources[i].Source)
	}
}
