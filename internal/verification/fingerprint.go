package verification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/yaml"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/dependency"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
var imagePattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func newFingerprint(ctx context.Context, runner command.Runner, root string, current plan, options Options) *model.RunFingerprint {
	version, commit := options.Version, options.Commit
	if version == "" {
		version = "dev"
	}
	if commit == "" {
		commit = "unknown"
	}
	fingerprint := &model.RunFingerprint{CloudForgeVersion: version, CloudForgeCommit: commit,
		Platform: runtime.GOOS + "/" + runtime.GOARCH, CPUs: runtime.NumCPU(), Tools: []model.ToolVersion{},
		Configuration: safeConfiguration(current.config), Resources: current.effectiveResources, Budget: safetyBudget(), WorkloadHash: workloadFingerprint(current)}
	if len(current.config.Environment) > 0 {
		encoded, _ := json.Marshal(current.config.Environment)
		fingerprint.EnvironmentHash = hashBytes(encoded)
	}
	if current.config.Dependencies["redis"].Enabled {
		fingerprint.Dependencies = []model.DependencyFingerprint{dependency.RedisFingerprint()}
	}
	result := runner.Run(ctx, command.Request{Name: "git", Args: []string{"rev-parse", "HEAD"}, Dir: root, Timeout: 5 * time.Second})
	if !failed(result) && commitPattern.MatchString(strings.TrimSpace(result.Stdout)) {
		fingerprint.SourceCommit = strings.TrimSpace(result.Stdout)
	}
	result = runner.Run(ctx, command.Request{Name: "git", Args: []string{"status", "--porcelain", "--", "."}, Dir: root, Timeout: 5 * time.Second})
	fingerprint.SourceDirty = failed(result) || strings.TrimSpace(result.Stdout) != ""
	return fingerprint
}

func fingerprintImage(ctx context.Context, runner command.Runner, image string, fingerprint *model.RunFingerprint) {
	result := runner.Run(ctx, command.Request{Name: "docker", Args: []string{"image", "inspect", "--format", "{{json .Id}} {{json .RepoDigests}}", image}, Timeout: 10 * time.Second})
	if failed(result) || result.Truncated {
		return
	}
	decoder := json.NewDecoder(strings.NewReader(result.Stdout))
	var id string
	var digests []string
	if decoder.Decode(&id) != nil || !imagePattern.MatchString(id) {
		return
	}
	fingerprint.ImageID = id
	if decoder.Decode(&digests) == nil {
		sort.Strings(digests)
		for _, digest := range digests {
			_, value, ok := strings.Cut(digest, "@")
			if ok && imagePattern.MatchString(value) {
				fingerprint.ImageDigest = value
				break
			}
		}
	}
}

func hashBytes(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }

func workloadFingerprint(current plan) string {
	decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(string(current.manifest)+"\n---\n"+string(current.hpaManifest)), 4096)
	var documents []any
	replacements := strings.NewReplacer(current.image, "<image>", current.workloadName, "<workload>", current.clusterName, "<cluster>", strings.TrimPrefix(current.clusterName, "cloudforge-"), "<run-id>")
	var normalize func(any) any
	normalize = func(value any) any {
		switch v := value.(type) {
		case string:
			return replacements.Replace(v)
		case []any:
			for i := range v {
				v[i] = normalize(v[i])
			}
		case map[string]any:
			for key, item := range v {
				v[key] = normalize(item)
			}
		}
		return value
	}
	for {
		var document any
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ""
		}
		if document != nil {
			documents = append(documents, normalize(document))
		}
	}
	data, _ := json.Marshal(documents)
	return hashBytes(data)
}
