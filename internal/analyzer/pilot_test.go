package analyzer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

func TestRootUserWithGroup(t *testing.T) {
	for _, user := range []string{"0:1000", "root:users", "00:users", "1000:0"} {
		t.Run(user, func(t *testing.T) {
			root := pilotRepository(t, map[string]string{"package.json": `{}`, "Dockerfile": "FROM node:22\nUSER " + user + "\nEXPOSE 8080\n"})
			result, err := New().Analyze(root)
			if err != nil {
				t.Fatal(err)
			}
			want := model.StatusFail
			if user == "1000:0" {
				want = model.StatusPass
			}
			if got := findFinding(result.Findings, "container.dockerfile.non-root"); got == nil || got.Status != want {
				t.Fatalf("wrong root classification: %#v", got)
			}
		})
	}
}

func TestAllProbeTypesRemainPresentWithoutSecretCommands(t *testing.T) {
	for _, handler := range []string{`{"tcpSocket":{"port":8080}}`, `{"exec":{"command":["secret-command-value"]}}`, `{"grpc":{"port":8080}}`} {
		root := pilotRepository(t, map[string]string{"package.json": `{}`, "deployment.yaml": `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api"},"spec":{"template":{"spec":{"containers":[{"name":"api","readinessProbe":` + handler + `,"livenessProbe":` + handler + `}]}}}}`})
		result, err := New().Analyze(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, suffix := range []string{"readiness-probe", "health-probe"} {
			if got := findFinding(result.Findings, "kubernetes.deployment.default-api."+suffix); got == nil || got.Status != model.StatusPass {
				t.Fatalf("valid probe reported missing: %#v", got)
			}
		}
		data, _ := json.Marshal(result)
		if strings.Contains(string(data), "secret-command-value") {
			t.Fatal("exec content leaked")
		}
	}
}

func TestORMDoesNotAssertPostgreSQL(t *testing.T) {
	for name, content := range map[string]string{"package.json": `{"dependencies":{"sequelize":"1","typeorm":"1"}}`, "requirements.txt": "sqlalchemy==2.0\n"} {
		result, err := New().Analyze(pilotRepository(t, map[string]string{name: content}))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Application.Dependencies) == 0 || !hasDiagnostic(result, "database_backend_unknown") {
			t.Fatal("missing unresolved backend")
		}
		for _, dependency := range result.Application.Dependencies {
			if dependency.Name == "postgresql" {
				t.Fatal("generic ORM asserted PostgreSQL")
			}
		}
	}
}

func TestUnresolvedDockerPortHasDiagnostic(t *testing.T) {
	result, err := New().Analyze(pilotRepository(t, map[string]string{"package.json": `{}`, "Dockerfile": "FROM node:22\nUSER node\nEXPOSE $PORT\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if !hasDiagnostic(result, "docker_port_unknown") || !result.Application.Containers[0].UnresolvedPorts {
		t.Fatal("unresolved port disappeared")
	}
	if got := findFinding(result.Findings, "container.dockerfile.port"); got == nil || got.Status != model.StatusWarn {
		t.Fatalf("unknown port was claimed absent: %#v", got)
	}
}

func TestFIFOMetadataDoesNotBlock(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "package.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := readBounded(root, "package.json"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO blocked analysis")
	}
}

func pilotRepository(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
