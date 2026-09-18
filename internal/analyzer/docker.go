package analyzer

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

func (a *Analyzer) analyzeDocker(root string, files []string, result *model.AnalysisResult) {
	for _, path := range files {
		base := filepathBase(path)
		if base != "Dockerfile" && !strings.HasPrefix(base, "Dockerfile.") {
			continue
		}
		if strings.Contains(path, "/") {
			continue
		}
		data, err := readBounded(root, path)
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, diagnostic("dockerfile_unreadable", "Could not read Dockerfile.", path, err.Error()))
			continue
		}
		parsed, err := parser.Parse(bytes.NewReader(data))
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, diagnostic("dockerfile_invalid", "Dockerfile could not be parsed.", path, "Fix the Dockerfile syntax: "+err.Error()))
			continue
		}
		container := model.Container{Source: model.SourceReference{Path: path}}
		for _, node := range parsed.AST.Children {
			switch strings.ToLower(node.Value) {
			case "from":
				container.Image = firstNodeArgument(node)
				container.User = ""
				container.Ports = nil
				container.UnresolvedPorts = false
			case "user":
				container.User = firstNodeArgument(node)
			case "expose":
				for _, value := range nodeArguments(node) {
					portValue, protocol, ok := parseExposedPort(value)
					if ok {
						container.Ports = append(container.Ports, model.ContainerPort{Port: portValue, Protocol: protocol, Source: model.SourceReference{Path: path}})
					} else {
						container.UnresolvedPorts = true
						result.Diagnostics = append(result.Diagnostics, diagnostic("docker_port_unknown", "Docker EXPOSE contains an unresolved or invalid port.", path, "Specify runtime.port in the verification configuration; environment variables are not evaluated."))
					}
				}
			}
		}
		sortContainerPorts(container.Ports)
		result.Application.Containers = append(result.Application.Containers, container)
	}
}

func nodeArguments(node *parser.Node) []string {
	var values []string
	for current := node.Next; current != nil; current = current.Next {
		if current.Value != "" {
			values = append(values, current.Value)
		}
	}
	if len(values) == 0 && node.Next == nil && node.Attributes != nil {
		for key := range node.Attributes {
			values = append(values, key)
		}
	}
	return values
}

func firstNodeArgument(node *parser.Node) string {
	values := nodeArguments(node)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func parseExposedPort(value string) (int32, string, bool) {
	parts := strings.SplitN(value, "/", 2)
	parsed, err := strconv.ParseInt(parts[0], 10, 32)
	if err != nil || parsed < 1 || parsed > 65535 {
		return 0, "", false
	}
	protocol := "TCP"
	if len(parts) == 2 {
		protocol = strings.ToUpper(parts[1])
	}
	return int32(parsed), protocol, true
}

func filepathBase(path string) string {
	if index := strings.LastIndexByte(path, '/'); index >= 0 {
		return path[index+1:]
	}
	return path
}

func sortContainerPorts(ports []model.ContainerPort) {
	for i := 1; i < len(ports); i++ {
		for j := i; j > 0 && (ports[j].Port < ports[j-1].Port || (ports[j].Port == ports[j-1].Port && ports[j].Protocol < ports[j-1].Protocol)); j-- {
			ports[j], ports[j-1] = ports[j-1], ports[j]
		}
	}
}
