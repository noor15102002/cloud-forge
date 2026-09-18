package analyzer

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/noor15102002/cloud-forge/pkg/model"
)

type packageJSON struct {
	Name            string            `json:"name"`
	PackageManager  string            `json:"packageManager"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	Engines         struct {
		Node string `json:"node"`
	} `json:"engines"`
}

var nodeFrameworks = map[string]string{
	"@hapi/hapi": "hapi", "@nestjs/core": "nestjs", "express": "express",
	"fastify": "fastify", "koa": "koa", "next": "next.js",
}

var nodeArchitectureDependencies = map[string]struct{ name, kind string }{
	"pg": {"postgresql", "database"}, "postgres": {"postgresql", "database"},
	"sequelize": {"unknown", "database"}, "typeorm": {"unknown", "database"},
	"redis": {"redis", "cache"}, "ioredis": {"redis", "cache"},
}

func (a *Analyzer) analyzeNode(root string, files []string, result *model.AnalysisResult) {
	if !contains(files, "package.json") {
		return
	}
	data, err := readBounded(root, "package.json")
	if err != nil {
		result.Diagnostics = append(result.Diagnostics, diagnostic("manifest_unreadable", "Could not read package.json.", "package.json", err.Error()))
		return
	}
	var manifest packageJSON
	if err := json.Unmarshal(data, &manifest); err != nil {
		result.Diagnostics = append(result.Diagnostics, diagnostic("manifest_invalid", "package.json is not valid JSON.", "package.json", "Fix the manifest syntax: "+err.Error()))
		return
	}
	dependencies := make(map[string]string, len(manifest.Dependencies)+len(manifest.DevDependencies))
	for name, version := range manifest.Dependencies {
		dependencies[name] = version
	}
	for name, version := range manifest.DevDependencies {
		dependencies[name] = version
	}
	frameworks := matchingFrameworks(dependencies, nodeFrameworks)
	language := "nodejs"
	if contains(files, "tsconfig.json") || dependencies["typescript"] != "" {
		language = "typescript"
	}
	runtime := model.Runtime{
		Language: language, Version: manifest.Engines.Node, PackageManager: nodePackageManager(files, manifest.PackageManager),
		Source: model.SourceReference{Path: "package.json"},
	}
	if len(frameworks) == 1 {
		runtime.Framework = frameworks[0]
	} else if len(frameworks) > 1 {
		runtime.FrameworkCandidates = frameworks
		result.Diagnostics = append(result.Diagnostics, diagnostic("multiple_frameworks", "Multiple Node.js framework candidates were detected.", "package.json", "Select the intended application subdirectory when analyzing a monorepo."))
	}
	result.Application.Runtimes = append(result.Application.Runtimes, runtime)
	if result.Application.Name == "" {
		result.Application.Name = manifest.Name
	}
	for packageName, architecture := range nodeArchitectureDependencies {
		if _, ok := dependencies[packageName]; ok {
			addDependency(result, architecture.name, architecture.kind, "package.json")
			if architecture.name == "unknown" {
				result.Diagnostics = append(result.Diagnostics, diagnostic("database_backend_unknown", "A database ORM does not identify a specific backend.", "package.json", "Configure and validate test dependencies explicitly; no database is provisioned automatically."))
			}
		}
	}
}

func nodePackageManager(files []string, declared string) string {
	switch {
	case contains(files, "pnpm-lock.yaml"):
		return "pnpm"
	case contains(files, "yarn.lock"):
		return "yarn"
	case contains(files, "package-lock.json"):
		return "npm"
	default:
		if index := strings.IndexByte(declared, '@'); index >= 0 {
			declared = declared[:index]
		}
		return declared
	}
}

func matchingFrameworks(dependencies map[string]string, known map[string]string) []string {
	var values []string
	for name, framework := range known {
		if _, ok := dependencies[name]; ok {
			values = append(values, framework)
		}
	}
	sort.Strings(values)
	return values
}

func addDependency(result *model.AnalysisResult, name, kind, path string) {
	for _, existing := range result.Application.Dependencies {
		if existing.Name == name && existing.Type == kind && existing.Source.Path == path {
			return
		}
	}
	result.Application.Dependencies = append(result.Application.Dependencies, model.Dependency{Name: name, Type: kind, Source: model.SourceReference{Path: path}})
}
