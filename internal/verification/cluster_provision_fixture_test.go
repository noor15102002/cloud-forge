package verification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"text/template"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// clusterProvisionFixture models the newly explicit Docker network/volume
// lifecycle for service tests. Custom runner failures and nonempty observations
// remain authoritative; no fault is replaced by a successful default.
func clusterProvisionFixture(inner command.Runner) command.Runner {
	var lock sync.Mutex
	objects := map[string]map[string]any{}
	return runnerFunc(func(ctx context.Context, request command.Request) model.CommandResult {
		lock.Lock()
		defer lock.Unlock()
		result := inner.Run(ctx, request)
		args := request.Args
		if request.Name != "docker" || len(args) < 2 || (args[0] != "network" && args[0] != "volume") || failed(result) || result.Truncated {
			return result
		}
		kind := args[0]
		switch args[1] {
		case "create":
			name := args[len(args)-1]
			if !strings.HasPrefix(name, "k3d-cloudforge-") {
				return result
			}
			labels := map[string]string{}
			for i, arg := range args {
				if arg == "--label" && i+1 < len(args) {
					key, value, _ := strings.Cut(args[i+1], "=")
					labels[key] = value
				}
			}
			id := name
			if kind == "network" {
				id = strings.Repeat("d", 64)
			}
			objects[kind+":"+name] = map[string]any{"Id": id, "ID": id, "Name": name, "Labels": labels, "CreatedAt": "2026-09-22T00:00:00Z"}
			if result.Stdout == "" {
				result.Stdout = id + "\n"
			}
		case "ls":
			if result.Stdout != "" {
				return result
			}
			var lines []string
			for key, object := range objects {
				if !strings.HasPrefix(key, kind+":") {
					continue
				}
				matches := true
				for i, arg := range args {
					if arg != "--filter" || i+1 >= len(args) {
						continue
					}
					filter := args[i+1]
					if name, ok := strings.CutPrefix(filter, "name="); ok && !strings.Contains(object["Name"].(string), name) {
						matches = false
					}
					if label, ok := strings.CutPrefix(filter, "label="); ok {
						key, value, _ := strings.Cut(label, "=")
						if object["Labels"].(map[string]string)[key] != value {
							matches = false
						}
					}
				}
				if matches {
					line := object["Name"].(string)
					if kind == "network" {
						line = object["Id"].(string) + " " + line
					}
					lines = append(lines, line)
				}
			}
			sort.Strings(lines)
			if len(lines) > 0 {
				result.Stdout = strings.Join(lines, "\n") + "\n"
			}
		case "inspect":
			if result.Stdout != "" {
				return result
			}
			ref := args[len(args)-1]
			for key, object := range objects {
				if !strings.HasPrefix(key, kind+":") || object["Id"] != ref && object["Name"] != ref {
					continue
				}
				for i, arg := range args {
					if arg == "--format" && i+1 < len(args) {
						format, err := template.New("docker-inspect").Funcs(template.FuncMap{"json": func(value any) (string, error) { data, err := json.Marshal(value); return string(data), err }}).Parse(args[i+1])
						if err != nil {
							panic(fmt.Sprintf("invalid inspect template: %v", err))
						}
						var buffer bytes.Buffer
						if err := format.Execute(&buffer, object); err != nil {
							panic(fmt.Sprintf("invalid inspect fixture: %v", err))
						}
						result.Stdout = buffer.String() + "\n"
						return result
					}
				}
			}
			if strings.HasPrefix(ref, "k3d-cloudforge-") || ref == strings.Repeat("d", 64) {
				result.ExitCode, result.FailureType = 1, model.FailureExit
				result.Stderr = "Error: No such object: " + ref
			}
		case "rm":
			ref := args[len(args)-1]
			for key, object := range objects {
				if strings.HasPrefix(key, kind+":") && (object["Id"] == ref || object["Name"] == ref) {
					delete(objects, key)
				}
			}
		}
		return result
	})
}
