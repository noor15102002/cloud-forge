package verification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/doctor"
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
	// Names, image tags and loopback ports vary per run; replace only those
	// generated identifiers before hashing the effective manifest semantics.
	manifest := string(current.manifest) + string(current.hpaManifest)
	for _, name := range []string{current.image, current.workloadName, current.clusterName} {
		manifest = strings.ReplaceAll(manifest, name, "<run>")
	}
	manifest = strings.ReplaceAll(manifest, strings.TrimPrefix(current.clusterName, "cloudforge-"), "<id>")
	fingerprint := &model.RunFingerprint{CloudForgeVersion: version, CloudForgeCommit: commit,
		Platform: runtime.GOOS + "/" + runtime.GOARCH, CPUs: runtime.NumCPU(), Tools: []model.ToolVersion{},
		Configuration: current.config, Resources: current.effectiveResources, Budget: safetyBudget(), WorkloadHash: hashBytes([]byte(manifest))}
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

func fingerprintTools(ctx context.Context, runner command.Runner, current plan, fingerprint *model.RunFingerprint) {
	specs := []struct {
		name, command string
		args          []string
	}{
		{"docker", "docker", []string{"info", "--format", "{{.ServerVersion}}"}},
		{"k3d", "k3d", []string{"version"}}, {"k6", "k6", []string{"version"}}, {"trivy", "trivy", []string{"--version"}},
		{"kubernetes", "kubectl", []string{"--context", "k3d-" + current.clusterName, "version", "--output=json"}},
	}
	complete := true
	for _, spec := range specs {
		result := runner.Run(ctx, command.Request{Name: spec.command, Args: spec.args, Timeout: 10 * time.Second})
		version := ""
		if !failed(result) && !result.Truncated {
			if spec.name == "kubernetes" {
				var versions struct {
					ServerVersion struct {
						GitVersion string `json:"gitVersion"`
					} `json:"serverVersion"`
				}
				if json.Unmarshal([]byte(result.Stdout), &versions) == nil {
					version = doctor.ParsedVersion(versions.ServerVersion.GitVersion)
				}
			} else {
				version = doctor.ParsedVersion(result.Stdout)
			}
		}
		if version == "" {
			complete = false
			version = "unknown"
		}
		fingerprint.Tools = append(fingerprint.Tools, model.ToolVersion{Name: spec.name, Version: version})
	}
	sort.Slice(fingerprint.Tools, func(i, j int) bool { return fingerprint.Tools[i].Name < fingerprint.Tools[j].Name })
	if complete {
		comparison := *fingerprint
		comparison.SourceCommit, comparison.ImageID, comparison.ImageDigest = "", "", ""
		comparison.SourceDirty = false
		data, _ := json.Marshal(comparison)
		fingerprint.CompatibilityKey = hashBytes(data)
	}
}

func hashBytes(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
