package verification

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"text/template"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/executor/k3d"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// Model only the import target. The separate cleanup fixtures retain control
// over their inventories and injected errors.
func importNodeFixture(inner command.Runner) command.Runner {
	var lock sync.Mutex
	nodes := map[string]map[string]any{}
	return runnerFunc(func(ctx context.Context, req command.Request) model.CommandResult {
		lock.Lock()
		defer lock.Unlock()
		result := inner.Run(ctx, req)
		if failed(result) || result.Truncated {
			return result
		}
		if req.Name == "k3d" && len(req.Args) > 2 && req.Args[0] == "cluster" && req.Args[1] == "create" {
			cluster := req.Args[2]
			labels := map[string]string{"k3d.cluster": cluster, "k3d.role": "server"}
			for i, arg := range req.Args[:len(req.Args)-1] {
				if arg == "--runtime-label" {
					label, _, _ := strings.Cut(req.Args[i+1], "@")
					key, value, _ := strings.Cut(label, "=")
					labels[key] = value
				}
			}
			name := "k3d-" + cluster + "-server-0"
			nodes[name] = map[string]any{"Id": strings.Repeat("c", 64), "Name": "/" + name, "Config": map[string]any{"Labels": labels}, "State": map[string]bool{"Running": true}}
		}
		if result.Stdout == "" && isImportNodeInspection(req) {
			if node := nodes[req.Args[len(req.Args)-1]]; node != nil {
				format, err := template.New("node").Funcs(template.FuncMap{"json": func(value any) (string, error) { b, e := json.Marshal(value); return string(b), e }}).Parse(req.Args[3])
				var out bytes.Buffer
				if err == nil {
					err = format.Execute(&out, node)
				}
				if err != nil {
					panic(err)
				}
				result.Stdout = out.String()
			}
		}
		return result
	})
}

func isImportNodeInspection(req command.Request) bool {
	return req.Name == "docker" && len(req.Args) == 5 && req.Args[0] == "container" && req.Args[1] == "inspect" && req.Args[2] == "--format" && strings.Contains(req.Args[3], `"owner":`)
}

func testImportClient(t *testing.T, inner command.Runner) *k3d.Client {
	t.Helper()
	runner := runnerFunc(func(ctx context.Context, req command.Request) model.CommandResult {
		result := inner.Run(ctx, req)
		if !failed(result) && !result.Truncated && result.Stdout == "" && isImportNodeInspection(req) {
			result.Stdout = `{"id":"` + strings.Repeat("c", 64) + `","name":true,"owner":true,"run":true,"cluster":true,"role":true,"running":true}`
		}
		return result
	})
	client := k3d.New(runner)
	client.SetOwnership(strings.Repeat("f", 32))
	workspace := t.TempDir()
	// #nosec G302 -- private directory requires owner traversal permission.
	if err := os.Chmod(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	client.SetWorkspace(workspace)
	return client
}
