package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"text/template"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

type k3dContractObject struct {
	kind, id, name string
	data           map[string]any
	present        bool
}

type k3dContractRunner struct {
	t        *testing.T
	objects  []*k3dContractObject
	active   bool
	removals int
}

func (r *k3dContractRunner) Run(_ context.Context, request command.Request) model.CommandResult {
	args := request.Args
	if request.Name != "docker" || len(args) < 2 {
		r.t.Fatalf("unexpected command: %+v", request)
	}
	format, filter := "", ""
	for i, arg := range args[:len(args)-1] {
		if arg == "--format" {
			format = args[i+1]
		}
		if arg == "--filter" {
			filter = args[i+1]
		}
	}
	result := model.CommandResult{}
	if args[1] == "ls" {
		if !r.active {
			return result
		}
		for _, object := range r.objects {
			if !object.present || object.kind != args[0] {
				continue
			}
			if strings.HasPrefix(filter, "name=") && !strings.Contains(object.name, strings.TrimPrefix(filter, "name=")) {
				continue
			}
			if strings.HasPrefix(filter, "label=") {
				key, value, _ := strings.Cut(strings.TrimPrefix(filter, "label="), "=")
				if object.labels()[key] != value {
					continue
				}
			}
			result.Stdout += r.render(format, object.data) + "\n"
		}
		return result
	}
	reference := args[len(args)-1]
	for _, object := range r.objects {
		if !object.present || object.kind != args[0] || reference != object.id && reference != object.name {
			continue
		}
		switch args[1] {
		case "inspect":
			result.Stdout = r.render(format, object.data) + "\n"
		case "rm":
			r.removals++
			object.present = false
		default:
			r.t.Fatalf("unexpected Docker operation: %+v", request)
		}
		return result
	}
	return model.CommandResult{ExitCode: 1, FailureType: model.FailureExit, Stderr: "Error: No such object: " + reference}
}

func (r *k3dContractRunner) render(format string, data map[string]any) string {
	r.t.Helper()
	parsed, err := template.New("docker").Funcs(template.FuncMap{"json": func(value any) string {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			r.t.Fatal(marshalErr)
		}
		return string(encoded)
	}}).Parse(format)
	if err != nil {
		r.t.Fatal(err)
	}
	var output bytes.Buffer
	if err := parsed.Execute(&output, data); err != nil {
		r.t.Fatal(err)
	}
	if strings.Contains(output.String(), "secret-canary") {
		r.t.Fatal("ownership inspection exposed unrelated private data")
	}
	return output.String()
}

func (o *k3dContractObject) labels() map[string]string {
	if o.kind == "container" {
		return o.data["Config"].(map[string]any)["Labels"].(map[string]string)
	}
	return o.data["Labels"].(map[string]string)
}

func (r *k3dContractRunner) object(suffix string) *k3dContractObject {
	r.t.Helper()
	for _, object := range r.objects {
		if object.name == "k3d-"+cleanupBuilderName+suffix {
			return object
		}
	}
	r.t.Fatalf("missing test resource: %s", suffix)
	return nil
}

// These are the Docker shapes produced by k3d v5.9.0, commit
// 15dd1e2dbd62c53ed990fce9b7332335e8cb053c. The network has only app=k3d;
// the tools helper has app/cluster labels but never receives --runtime-label.
// A failed helper deletion is only a warning in GatherEnvironmentInfo.
func k3dContractFixture(t *testing.T) (*Client, *k3dContractRunner) {
	t.Helper()
	prefix, token, networkID := "k3d-"+cleanupBuilderName, strings.Repeat("f", 32), strings.Repeat("d", 64)
	volume := prefix + "-images"
	r := &k3dContractRunner{t: t}
	container := func(suffix, letter string, private bool) *k3dContractObject {
		id, name := strings.Repeat(letter, 64), prefix+suffix
		labels := map[string]string{"app": "k3d", "k3d.cluster": cleanupBuilderName, "k3d.version": "5.9.0"}
		labels["k3d.role"] = "noRole"
		switch suffix {
		case "-server-0":
			labels["k3d.role"] = "server"
		case "-serverlb":
			labels["k3d.role"] = "loadbalancer"
		}
		if private {
			labels["cloudforge.dev/ownership"] = token
			labels["k3d.cluster.network"] = prefix
			labels["k3d.cluster.network.id"] = networkID
			labels["k3d.cluster.network.external"] = "false"
			labels["k3d.cluster.imageVolume"] = volume
		}
		data := map[string]any{
			"Id": id, "ID": id, "Name": "/" + name, "Names": name,
			"Config":          map[string]any{"Labels": labels, "Env": []string{"PRIVATE=secret-canary"}},
			"NetworkSettings": map[string]any{"Networks": map[string]any{prefix: map[string]any{"NetworkID": networkID}}},
			"Mounts":          []map[string]string{{"Type": "volume", "Name": volume, "Destination": "/k3d/images"}, {"Type": "bind", "Source": "secret-canary", "Destination": "/unrelated"}},
		}
		return &k3dContractObject{kind: "container", id: id, name: name, data: data, present: true}
	}
	// Inventory order cannot be used as proof: Docker can list the helper first.
	r.objects = []*k3dContractObject{container("-tools", "c", false), container("-serverlb", "b", true), container("-server-0", "a", true)}
	r.objects = append(r.objects,
		&k3dContractObject{kind: "network", id: networkID, name: prefix, present: true, data: map[string]any{
			"Id": networkID, "ID": networkID, "Name": prefix, "Labels": map[string]string{"app": "k3d"},
			"Containers": map[string]any{strings.Repeat("a", 64): map[string]string{"Name": prefix + "-server-0"}, strings.Repeat("b", 64): map[string]string{"Name": prefix + "-serverlb"}, strings.Repeat("c", 64): map[string]string{"Name": prefix + "-tools"}},
		}},
		&k3dContractObject{kind: "volume", id: volume, name: volume, present: true, data: map[string]any{
			"Name": volume, "Labels": map[string]string{"app": "k3d", "k3d.cluster": cleanupBuilderName, "k3d.version": "5.9.0"},
			"CreatedAt": "2026-09-22T12:00:00Z", "Driver": "local", "Scope": "local",
		}},
	)
	c := New(r)
	c.IsolateBuild(cleanupBuilderName)
	c.ownershipToken = token
	if result := c.PrepareCluster(context.Background(), cleanupBuilderName); builderCleanupFailed(result) {
		t.Fatal("empty preflight failed", result)
	}
	r.active = true
	return c, r
}

func TestK3d59DefaultCleanupOwnershipContract(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		name := "completed-create"
		if interrupted {
			name = "interrupted-create"
		}
		t.Run(name, func(t *testing.T) {
			client, runner := k3dContractFixture(t)
			if !interrupted {
				client.ClusterCreated()
				if result := client.VerifyClusterOwnership(context.Background(), cleanupBuilderName); builderCleanupFailed(result) {
					t.Fatal("default k3d resources were rejected", result)
				}
				// A partial normal deletion may remove both private nodes first.
				runner.object("-server-0").present = false
				runner.object("-serverlb").present = false
			}
			for _, kind := range []string{"container", "network", "volume"} {
				if result := client.RemoveClusterRemnants(context.Background(), cleanupBuilderName, kind); builderCleanupFailed(result) {
					t.Fatalf("%s default remnant was rejected: %+v", kind, result)
				}
			}
			for _, object := range runner.objects {
				if object.present {
					t.Fatalf("verified resource remained: %s", object.name)
				}
			}
		})
	}
}

func TestK3d59OwnershipRequiresActualRelationships(t *testing.T) {
	cases := map[string]func(*k3dContractRunner){
		"foreign tools network": func(r *k3dContractRunner) {
			r.object("-tools").data["NetworkSettings"] = map[string]any{"Networks": map[string]any{"k3d-" + cleanupBuilderName: map[string]string{"NetworkID": strings.Repeat("e", 64)}}}
		},
		"foreign tools mount": func(r *k3dContractRunner) {
			r.object("-tools").data["Mounts"] = []map[string]string{{"Type": "volume", "Name": "unrelated-sentinel", "Destination": "/k3d/images"}}
		},
		"unrelated network identity": func(r *k3dContractRunner) {
			network := r.object("")
			network.id = strings.Repeat("e", 64)
			network.data["Id"], network.data["ID"] = network.id, network.id
		},
		"foreign private node": func(r *k3dContractRunner) {
			r.object("-server-0").labels()["cloudforge.dev/ownership"] = "other-invocation"
		},
		"tools without private node proof": func(r *k3dContractRunner) {
			r.object("-server-0").present, r.object("-serverlb").present = false, false
		},
		"wrong network deletion reference": func(r *k3dContractRunner) {
			r.object("-server-0").labels()["k3d.cluster.network"] = "unrelated-sentinel"
		},
		"wrong image volume deletion reference": func(r *k3dContractRunner) {
			r.object("-server-0").labels()["k3d.cluster.imageVolume"] = "unrelated-sentinel"
		},
		"missing mounted volume": func(r *k3dContractRunner) {
			r.object("-images").present = false
		},
		"missing attached network": func(r *k3dContractRunner) {
			r.object("").present = false
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			client, runner := k3dContractFixture(t)
			mutate(runner)
			client.ClusterCreated()
			result := client.VerifyClusterOwnership(context.Background(), cleanupBuilderName)
			if !builderCleanupFailed(result) || result.ExitCode == 0 || result.FailureType == model.FailureNone || runner.removals != 0 {
				t.Fatalf("unproven resource authorized broad cleanup: %+v, removals=%d", result, runner.removals)
			}
		})
	}
}

func TestK3d59MissingAttachmentPreventsNodeRemoval(t *testing.T) {
	for _, suffix := range []string{"", "-images"} {
		name := "network"
		if suffix != "" {
			name = "volume"
		}
		t.Run(name, func(t *testing.T) {
			client, runner := k3dContractFixture(t)
			runner.object(suffix).present = false
			result := client.RemoveClusterRemnants(context.Background(), cleanupBuilderName, "container")
			if !builderCleanupFailed(result) || runner.removals != 0 || !runner.object("-server-0").present {
				t.Fatalf("node removal discarded an unproven attachment identity: %+v, removals=%d", result, runner.removals)
			}
		})
	}
}

func TestK3d59VolumeCreationIdentitySurvivesNodeRemoval(t *testing.T) {
	client, runner := k3dContractFixture(t)
	client.ClusterCreated()
	if result := client.VerifyClusterOwnership(context.Background(), cleanupBuilderName); builderCleanupFailed(result) {
		t.Fatal(result)
	}
	runner.object("-server-0").present, runner.object("-serverlb").present, runner.object("-tools").present = false, false, false
	runner.object("-images").data["CreatedAt"] = "2026-09-22T12:01:00Z"
	result := client.RemoveClusterRemnants(context.Background(), cleanupBuilderName, "volume")
	if !builderCleanupFailed(result) || runner.removals != 0 || !runner.object("-images").present {
		t.Fatalf("replacement volume inherited deletion authority: %+v", result)
	}
}
