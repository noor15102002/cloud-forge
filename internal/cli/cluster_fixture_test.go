package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"text/template"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

// Keep private infrastructure state across commands in each CLI runner. Inspect
// executes the production template against the labels supplied during create.
type cliClusterFixture struct {
	lock    sync.Mutex
	objects map[string]map[string]any
}

func (f *cliClusterFixture) run(request command.Request) (model.CommandResult, bool) {
	result := model.CommandResult{Command: request.Name, Arguments: request.Args}
	args := request.Args
	if request.Name == "k3d" && len(args) > 2 && args[0] == "cluster" && args[1] == "create" {
		f.lock.Lock()
		defer f.lock.Unlock()
		cluster := args[2]
		labels := map[string]string{"k3d.cluster": cluster, "k3d.role": "server"}
		for i, arg := range args[:len(args)-1] {
			if arg == "--runtime-label" {
				label, _, _ := strings.Cut(args[i+1], "@")
				key, value, _ := strings.Cut(label, "=")
				labels[key] = value
			}
		}
		name := "k3d-" + cluster + "-server-0"
		f.objects["container:"+name] = map[string]any{"Id": strings.Repeat("c", 64), "Name": "/" + name, "Config": map[string]any{"Labels": labels}, "State": map[string]bool{"Running": true}}
		return result, false
	}
	if request.Name == "docker" && len(args) == 5 && args[0] == "container" && args[1] == "inspect" && strings.Contains(args[3], `"owner":`) {
		f.lock.Lock()
		defer f.lock.Unlock()
		if node := f.objects["container:"+args[4]]; node != nil {
			format, err := template.New("node").Funcs(template.FuncMap{"json": func(value any) (string, error) { b, e := json.Marshal(value); return string(b), e }}).Parse(args[3])
			var out bytes.Buffer
			if err == nil {
				err = format.Execute(&out, node)
			}
			if err != nil {
				panic(err)
			}
			result.Stdout = out.String()
			return result, true
		}
	}
	if request.Name != "docker" || len(args) < 2 || (args[0] != "network" && args[0] != "volume") {
		return result, false
	}
	kind, reference := args[0], args[len(args)-1]
	if strings.HasPrefix(reference, "buildx_buildkit_") {
		return result, false
	}
	f.lock.Lock()
	defer f.lock.Unlock()
	switch args[1] {
	case "create":
		if !strings.HasPrefix(reference, "k3d-cloudforge-") {
			return result, false
		}
		labels := map[string]string{}
		for i, arg := range args[:len(args)-1] {
			if arg == "--label" {
				key, value, _ := strings.Cut(args[i+1], "=")
				labels[key] = value
			}
		}
		id := reference
		if kind == "network" {
			sum := sha256.Sum256([]byte(reference))
			id = hex.EncodeToString(sum[:])
		}
		f.objects[kind+":"+reference] = map[string]any{
			"Id": id, "ID": id, "Name": reference, "Labels": labels, "CreatedAt": "2026-09-22T00:00:00Z",
		}
		result.Stdout = id + "\n"
	case "ls":
		var lines []string
		for key, object := range f.objects {
			if !strings.HasPrefix(key, kind+":") || !cliClusterMatches(object, args) {
				continue
			}
			line := object["Name"].(string)
			if kind == "network" {
				line = object["Id"].(string) + " " + line
			}
			lines = append(lines, line)
		}
		sort.Strings(lines)
		if len(lines) > 0 {
			result.Stdout = strings.Join(lines, "\n") + "\n"
		}
	case "inspect", "rm":
		for key, object := range f.objects {
			if !strings.HasPrefix(key, kind+":") || object["Id"] != reference && object["Name"] != reference {
				continue
			}
			if args[1] == "rm" {
				delete(f.objects, key)
				return result, true
			}
			for i, arg := range args[:len(args)-1] {
				if arg != "--format" {
					continue
				}
				format, err := template.New("docker-inspect").Funcs(template.FuncMap{"json": func(value any) (string, error) {
					data, err := json.Marshal(value)
					return string(data), err
				}}).Parse(args[i+1])
				var buffer bytes.Buffer
				if err == nil {
					err = format.Execute(&buffer, object)
				}
				if err != nil {
					result.ExitCode, result.FailureType, result.Stderr = 1, model.FailureExit, err.Error()
					return result, true
				}
				result.Stdout = buffer.String() + "\n"
				return result, true
			}
		}
		result.ExitCode, result.FailureType = 1, model.FailureExit
		result.Stderr = "Error: No such object: " + reference
	default:
		return result, false
	}
	return result, true
}

func cliClusterMatches(object map[string]any, args []string) bool {
	for i, arg := range args[:len(args)-1] {
		if arg != "--filter" {
			continue
		}
		filter := args[i+1]
		if name, ok := strings.CutPrefix(filter, "name="); ok && !strings.Contains(object["Name"].(string), name) {
			return false
		}
		if label, ok := strings.CutPrefix(filter, "label="); ok {
			key, value, _ := strings.Cut(label, "=")
			if object["Labels"].(map[string]string)[key] != value {
				return false
			}
		}
	}
	return true
}
