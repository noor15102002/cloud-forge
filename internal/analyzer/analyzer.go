// Package analyzer deterministically derives application architecture from repository metadata.
package analyzer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

const (
	maxFiles    = 2000
	maxFileSize = 2 << 20
)

var skippedDirectories = map[string]bool{
	".git": true, ".idea": true, ".next": true, ".tox": true,
	".venv": true, "build": true, "coverage": true, "dist": true,
	"node_modules": true, "vendor": true, "venv": true,
}

// Analyzer discovers supported application architecture without executing repository code.
type Analyzer struct{}

// New creates a repository analyzer.
func New() *Analyzer { return &Analyzer{} }

// Analyze inspects one application root and returns a deterministic result.
func (a *Analyzer) Analyze(path string) (model.AnalysisResult, error) {
	root, err := filepath.Abs(path)
	if err != nil {
		return model.AnalysisResult{}, fmt.Errorf("resolve repository path: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return model.AnalysisResult{}, fmt.Errorf("resolve repository symlinks: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return model.AnalysisResult{}, fmt.Errorf("inspect repository: %w", err)
	}
	if !info.IsDir() {
		return model.AnalysisResult{}, errors.New("analysis path must be a directory")
	}

	result := model.AnalysisResult{
		SchemaVersion: model.SchemaVersion,
		Status:        model.StatusPass,
		Application:   model.Application{Path: "."},
	}
	files, walkDiagnostics, err := discoverFiles(root)
	if err != nil {
		return model.AnalysisResult{}, err
	}
	result.Diagnostics = append(result.Diagnostics, walkDiagnostics...)

	a.analyzeNode(root, files, &result)
	a.analyzePython(root, files, &result)
	a.analyzeDocker(root, files, &result)
	a.analyzeKubernetes(root, files, &result)
	a.detectLimitedFormats(files, &result)

	result.Supported = len(result.Application.Runtimes) > 0
	if !result.Supported {
		result.Status = model.StatusFail
		result.Diagnostics = append(result.Diagnostics, model.Diagnostic{
			Code: "unsupported_application", Status: model.StatusFail,
			Message:  "No supported Node.js, TypeScript, or Python application manifest was found.",
			Guidance: "Analyze a directory containing package.json, pyproject.toml, or requirements.txt.",
		})
	} else if hasWarning(result.Diagnostics) {
		result.Status = model.StatusWarn
	}
	sortResult(&result)
	return result, nil
}

func discoverFiles(root string) ([]string, []model.Diagnostic, error) {
	var files []string
	var diagnostics []model.Diagnostic
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			rel, _ := filepath.Rel(root, path)
			diagnostics = append(diagnostics, diagnostic("path_unreadable", "Could not inspect a repository path.", filepath.ToSlash(rel), walkErr.Error()))
			return nil
		}
		if path != root && entry.Type()&os.ModeSymlink != 0 {
			rel, _ := filepath.Rel(root, path)
			diagnostics = append(diagnostics, diagnostic("symlink_skipped", "Skipped a symbolic link.", filepath.ToSlash(rel), "CloudForge does not follow repository symlinks during analysis."))
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if path != root && skippedDirectories[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if len(files) >= maxFiles {
			return errors.New("repository file limit exceeded")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		if err.Error() == "repository file limit exceeded" {
			return files, append(diagnostics, model.Diagnostic{Code: "file_limit", Status: model.StatusWarn, Message: "Repository file limit reached.", Guidance: "Analyze a smaller application subdirectory."}), nil
		}
		return nil, nil, fmt.Errorf("walk repository: %w", err)
	}
	sort.Strings(files)
	return files, diagnostics, nil
}

func readBounded(root, relative string) ([]byte, error) {
	path := filepath.Join(root, filepath.FromSlash(relative))
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("symbolic links are not read")
	}
	if info.Size() > maxFileSize {
		return nil, fmt.Errorf("file exceeds %d byte analysis limit", maxFileSize)
	}
	// #nosec G304 -- path is rooted under an evaluated repository root and symlinks are rejected.
	return os.ReadFile(path)
}

func diagnostic(code, message, path, guidance string) model.Diagnostic {
	return model.Diagnostic{Code: code, Status: model.StatusWarn, Message: message, Guidance: guidance, Source: &model.SourceReference{Path: path}}
}

func hasWarning(diagnostics []model.Diagnostic) bool {
	for _, item := range diagnostics {
		if item.Status == model.StatusWarn || item.Status == model.StatusError {
			return true
		}
	}
	return false
}

func contains(files []string, name string) bool {
	i := sort.SearchStrings(files, name)
	return i < len(files) && files[i] == name
}

func (a *Analyzer) detectLimitedFormats(files []string, result *model.AnalysisResult) {
	for _, path := range files {
		base := filepath.Base(path)
		switch {
		case base == "Chart.yaml":
			result.Diagnostics = append(result.Diagnostics, diagnostic("helm_limited", "Helm chart detected; template rendering is not supported in this release.", path, "CloudForge analyzes rendered or plain Kubernetes YAML."))
		case path == "compose.yaml" || path == "compose.yml" || path == "docker-compose.yaml" || path == "docker-compose.yml":
			result.Diagnostics = append(result.Diagnostics, diagnostic("compose_limited", "Docker Compose configuration detected; Compose service analysis is not supported in this release.", path, "Provide a Dockerfile and application manifest at the selected application root."))
		}
	}
}

func sortResult(result *model.AnalysisResult) {
	sort.Slice(result.Application.Runtimes, func(i, j int) bool {
		if result.Application.Runtimes[i].Language == result.Application.Runtimes[j].Language {
			return result.Application.Runtimes[i].Source.Path < result.Application.Runtimes[j].Source.Path
		}
		return result.Application.Runtimes[i].Language < result.Application.Runtimes[j].Language
	})
	for i := range result.Application.Runtimes {
		sort.Strings(result.Application.Runtimes[i].FrameworkCandidates)
	}
	sort.Slice(result.Application.Dependencies, func(i, j int) bool {
		if result.Application.Dependencies[i].Type == result.Application.Dependencies[j].Type {
			return result.Application.Dependencies[i].Name < result.Application.Dependencies[j].Name
		}
		return result.Application.Dependencies[i].Type < result.Application.Dependencies[j].Type
	})
	sort.Slice(result.Application.Containers, func(i, j int) bool {
		return result.Application.Containers[i].Source.Path < result.Application.Containers[j].Source.Path
	})
	k := &result.Application.Kubernetes
	sort.Slice(k.Deployments, func(i, j int) bool {
		return resourceKey(k.Deployments[i].Namespace, k.Deployments[i].Name) < resourceKey(k.Deployments[j].Namespace, k.Deployments[j].Name)
	})
	sort.Slice(k.Services, func(i, j int) bool {
		return resourceKey(k.Services[i].Namespace, k.Services[i].Name) < resourceKey(k.Services[j].Namespace, k.Services[j].Name)
	})
	sort.Slice(k.HorizontalPodScalers, func(i, j int) bool {
		return resourceKey(k.HorizontalPodScalers[i].Namespace, k.HorizontalPodScalers[i].Name) < resourceKey(k.HorizontalPodScalers[j].Namespace, k.HorizontalPodScalers[j].Name)
	})
	sort.Slice(k.OtherResources, func(i, j int) bool {
		left, right := k.OtherResources[i], k.OtherResources[j]
		return left.Source.Path+left.Kind+left.Name < right.Source.Path+right.Kind+right.Name
	})
	sort.Slice(result.Diagnostics, func(i, j int) bool {
		left, right := result.Diagnostics[i], result.Diagnostics[j]
		return diagnosticKey(left) < diagnosticKey(right)
	})
}

func resourceKey(namespace, name string) string { return namespace + "/" + name }

func diagnosticKey(value model.Diagnostic) string {
	path := ""
	if value.Source != nil {
		path = value.Source.Path
	}
	return path + "/" + value.Code + "/" + value.Message
}
