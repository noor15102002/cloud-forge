package verification

import (
	"strings"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func annotateMaturity(result *model.VerificationPlan, config model.RuntimeConfiguration) {
	experimentalWorkload := backendProfile(config) || isWorker(config) || config.Preparation != nil || config.Network != nil
	for _, name := range []string{"postgresql", "clamav"} {
		experimentalWorkload = experimentalWorkload || config.Dependencies[name].Enabled
	}
	result.Limitations = append(result.Limitations, "CORE and EXPERIMENTAL describe product maturity; SUPPORTED means schedulable, not qualified or passed.")
	if experimentalWorkload {
		result.Limitations = append(result.Limitations, "This workload requires experimental runtime capabilities and is outside the bounded HTTP/Redis release contract.")
	}
	for i := range result.Capabilities {
		c := &result.Capabilities[i]
		maturity := model.CoreMaturity
		if experimentalWorkload || strings.HasPrefix(c.Name, "worker-") {
			maturity = model.ExperimentalMaturity
		}
		switch c.Name {
		case "horizontal-autoscaling", "readiness-gating", "inflight-shutdown", "dependency.postgresql", "dependency.clamav", "application-preparation", "network-isolation":
			maturity = model.ExperimentalMaturity
		case "dependency-loss":
			maturity = model.UnsupportedMaturity
		default:
			if strings.HasPrefix(c.Name, "dependency.") && c.Name != "dependency.redis" {
				maturity = model.UnsupportedMaturity
			}
		}
		c.Limitations = append(c.Limitations, maturity)
	}
}
