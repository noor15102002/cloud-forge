package render

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestProbePolicyRenderingExplainsScopeAndSamplingLimits(t *testing.T) {
	for name, render := range map[string]func(io.Writer, model.VerificationPlan) error{"text": PlanText, "markdown": PlanMarkdown} {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			plan := model.VerificationPlan{Status: model.StatusPass, Probes: &model.ProbeSettings{Interval: "2s"}}
			if err := render(&output, plan); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"minimum request-start interval 2s", "explicit test configuration", "readiness and availability", "Kubernetes probes, explicit control requests and k6 are unchanged", "Slower sampling can miss shorter outages"} {
				if !strings.Contains(output.String(), want) {
					t.Fatalf("missing policy explanation %q: %s", want, &output)
				}
			}
			output.Reset()
			plan.Probes = nil
			if err := render(&output, plan); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), "CloudForge HTTP probes") {
				t.Fatal("omitted policy was presented as explicit configuration")
			}
		})
	}
}
