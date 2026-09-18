package analyzer

import (
	"bufio"
	"bytes"
	"sort"
	"strings"
	"unicode"

	"github.com/noor15102002/cloud-forge/pkg/model"
	"github.com/pelletier/go-toml/v2"
)

type pyproject struct {
	Project struct {
		Name           string   `toml:"name"`
		RequiresPython string   `toml:"requires-python"`
		Dependencies   []string `toml:"dependencies"`
	} `toml:"project"`
	Tool struct {
		Poetry struct {
			Name         string         `toml:"name"`
			Dependencies map[string]any `toml:"dependencies"`
		} `toml:"poetry"`
	} `toml:"tool"`
}

var pythonFrameworks = map[string]string{
	"django": "django", "fastapi": "fastapi", "flask": "flask", "starlette": "starlette",
}

var pythonArchitectureDependencies = map[string]struct{ name, kind string }{
	"asyncpg": {"postgresql", "database"}, "psycopg": {"postgresql", "database"},
	"psycopg2": {"postgresql", "database"}, "sqlalchemy": {"unknown", "database"},
	"redis": {"redis", "cache"}, "hiredis": {"redis", "cache"},
}

func (a *Analyzer) analyzePython(root string, files []string, result *model.AnalysisResult) {
	var dependencies []string
	var runtime *model.Runtime
	if contains(files, "pyproject.toml") {
		data, err := readBounded(root, "pyproject.toml")
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, diagnostic("manifest_unreadable", "Could not read pyproject.toml.", "pyproject.toml", err.Error()))
			return
		}
		var project pyproject
		if err := toml.Unmarshal(data, &project); err != nil {
			result.Diagnostics = append(result.Diagnostics, diagnostic("manifest_invalid", "pyproject.toml is not valid TOML.", "pyproject.toml", "Fix the manifest syntax: "+err.Error()))
			return
		}
		for _, item := range project.Project.Dependencies {
			dependencies = append(dependencies, pythonRequirementName(item))
		}
		for name := range project.Tool.Poetry.Dependencies {
			if strings.ToLower(name) != "python" {
				dependencies = append(dependencies, normalizePythonName(name))
			}
		}
		name := project.Project.Name
		if name == "" {
			name = project.Tool.Poetry.Name
		}
		if result.Application.Name == "" {
			result.Application.Name = name
		}
		runtime = &model.Runtime{Language: "python", Version: project.Project.RequiresPython, PackageManager: pythonPackageManager(files), Source: model.SourceReference{Path: "pyproject.toml"}}
	} else if contains(files, "requirements.txt") {
		data, err := readBounded(root, "requirements.txt")
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, diagnostic("manifest_unreadable", "Could not read requirements.txt.", "requirements.txt", err.Error()))
			return
		}
		dependencies = parseRequirements(data)
		runtime = &model.Runtime{Language: "python", PackageManager: "pip", Source: model.SourceReference{Path: "requirements.txt"}}
	}
	if runtime == nil {
		return
	}
	dependencySet := make(map[string]string)
	for _, item := range dependencies {
		dependencySet[item] = ""
	}
	frameworks := matchingFrameworks(dependencySet, pythonFrameworks)
	if len(frameworks) == 1 {
		runtime.Framework = frameworks[0]
	} else if len(frameworks) > 1 {
		runtime.FrameworkCandidates = frameworks
		result.Diagnostics = append(result.Diagnostics, diagnostic("multiple_frameworks", "Multiple Python framework candidates were detected.", runtime.Source.Path, "Select the intended application subdirectory when analyzing a monorepo."))
	}
	result.Application.Runtimes = append(result.Application.Runtimes, *runtime)
	for packageName, architecture := range pythonArchitectureDependencies {
		if _, ok := dependencySet[packageName]; ok {
			addDependency(result, architecture.name, architecture.kind, runtime.Source.Path)
			if architecture.name == "unknown" {
				result.Diagnostics = append(result.Diagnostics, diagnostic("database_backend_unknown", "A database ORM does not identify a specific backend.", runtime.Source.Path, "Configure and validate test dependencies explicitly; no database is provisioned automatically."))
			}
		}
	}
}

func parseRequirements(data []byte) []string {
	set := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		if index := strings.Index(line, " #"); index >= 0 {
			line = line[:index]
		}
		name := pythonRequirementName(line)
		if name != "" {
			set[name] = true
		}
	}
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func pythonRequirementName(value string) string {
	value = strings.TrimSpace(value)
	var end int
	for end < len(value) {
		r := rune(value[end])
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' && r != '.' {
			break
		}
		end++
	}
	return normalizePythonName(value[:end])
}

func normalizePythonName(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(value), "_", "-"), ".", "-")
}

func pythonPackageManager(files []string) string {
	switch {
	case contains(files, "poetry.lock"):
		return "poetry"
	case contains(files, "uv.lock"):
		return "uv"
	default:
		return ""
	}
}
