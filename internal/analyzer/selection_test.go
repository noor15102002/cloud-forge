package analyzer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noor15102002/cloud-forge/pkg/model"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestSelectedAnalysisUsesOnlyIntendedMetadataAndDockerfile(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"package.json":             `{"name":"workspace","dependencies":{"pg":"1"}}`,
		"apps/api/package.json":    `{"name":"chosen","dependencies":{"express":"1"}}`,
		"apps/api/Dockerfile.dev":  "this is not a Dockerfile\n",
		"apps/other/package.json":  `{"name":"other","dependencies":{"redis":"1"}}`,
		"apps/other/workload.yaml": "kind: Deployment\napiVersion: apps/v1\nmetadata: {name: unrelated}\n",
		"apps/api/.env":            "SECRET=must-not-appear\n",
		"docker/Production":        "FROM node:24-alpine\nUSER node\nEXPOSE 8000\n",
		"apps/api/k8s.yaml":        "kind: Deployment\napiVersion: apps/v1\nmetadata: {name: chosen}\nspec:\n  template:\n    spec:\n      containers:\n      - name: chosen\n        image: test\n        ports: [{containerPort: 8000}]\n        readinessProbe:\n          httpGet: {path: /ready, port: 8000}\n",
	}
	for name, content := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	selection := &model.BuildSelection{App: "apps/api", Dockerfile: "docker/Production", Context: "."}
	first, err := New().AnalyzeSelected(root, selection)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New().AnalyzeSelected(root, selection)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Fatal("analysis was not repeatable")
	}
	if first.SchemaVersion != "v1alpha2" || first.Application.Name != "chosen" || first.Application.Path != "apps/api" || len(first.Application.Dependencies) != 0 || len(first.Application.Containers) != 1 || len(first.Application.Kubernetes.Deployments) != 1 {
		t.Fatalf("wrong selection: %s", a)
	}
	if first.Application.Containers[0].Source.Path != "docker/Production" || first.Application.Runtimes[0].Source.Path != "apps/api/package.json" || first.Application.Kubernetes.Deployments[0].Endpoints[0].Source.Path != "apps/api/k8s.yaml" {
		t.Fatalf("wrong provenance: %s", a)
	}
	for _, forbidden := range []string{"must-not-appear", "unrelated", "Dockerfile.dev", root} {
		if strings.Contains(string(a), forbidden) {
			t.Fatalf("unexpected content %q", forbidden)
		}
	}
}

func TestSelectedAnalysisSatisfiesPublishedSchema(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "monorepo")
	result, err := New().AnalyzeSelected(root, &model.BuildSelection{App: "apps/http", Dockerfile: "apps/http/Containerfile.release", Context: "."})
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	contract, err := compiler.Compile(filepath.Join("..", "..", "schemas", "analysis.v1alpha2.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if err := contract.Validate(document); err != nil {
		t.Fatal(err)
	}
}
