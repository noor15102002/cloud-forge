package verification

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

const (
	backendMinimumMemory    uint64 = 7 << 30
	backendMinimumAvailable uint64 = 13 << 29 // 6.5 GiB, including host headroom.
	backendMinimumDisk      uint64 = 30 << 30
	backendDockerInfoFormat        = `{"memory":{{.MemTotal}},"root":{{json .DockerRootDir}},"os":{{json .OSType}},"name":{{json .Name}},"kernel":{{json .KernelVersion}}}`
)

type backendCapacityReaders struct {
	platform string
	getenv   func(string) string
	memory   func() (total, available uint64, err error)
	hostname func() (string, error)
	kernel   func() (string, error)
	disk     func(string) (uint64, error)
}

// checkBackendCapacity is called only for an opted-in backend profile, before
// builds or runtime mutations. Its observations are a snapshot, not a resource
// reservation; concurrent host activity can still change available capacity.
func checkBackendCapacity(ctx context.Context, runner command.Runner, out *Outcome) bool {
	readers := backendCapacityReaders{
		platform: runtime.GOOS, getenv: os.Getenv, memory: readBackendMemory,
		hostname: os.Hostname, kernel: readBackendKernel, disk: backendDiskAvailable,
	}
	return readers.check(ctx, runner, out)
}

func (r backendCapacityReaders) check(ctx context.Context, runner command.Runner, out *Outcome) bool {
	measurements := []model.Measurement{
		{Name: "required_host_memory_total_bytes", Value: strconv.FormatUint(backendMinimumMemory, 10), Unit: "bytes"},
		{Name: "required_host_memory_available_bytes", Value: strconv.FormatUint(backendMinimumAvailable, 10), Unit: "bytes"},
		{Name: "required_docker_memory_total_bytes", Value: strconv.FormatUint(backendMinimumMemory, 10), Unit: "bytes"},
		{Name: "required_docker_root_available_bytes", Value: strconv.FormatUint(backendMinimumDisk, 10), Unit: "bytes"},
	}
	finish := func(status model.Status, code, summary, guidance string) bool {
		sort.Slice(measurements, func(i, j int) bool { return measurements[i].Name < measurements[j].Name })
		out.Run.Evidence = append(out.Run.Evidence, model.Evidence{
			ExperimentID: "backend-capacity", Title: "Bounded backend host capacity", Status: status,
			Summary: summary, Measurements: measurements, Execution: &model.ExperimentExecution{Executed: true},
		})
		if status == model.StatusPass {
			return true
		}
		out.Run.Status, out.ExitCode = status, 1
		if status == model.StatusError {
			out.ExitCode = 2
		}
		out.Run.Diagnostics = append(out.Run.Diagnostics, model.Diagnostic{Code: code, Status: status, Message: summary, Guidance: guidance})
		return false
	}
	unobservable := func() bool {
		return finish(model.StatusError, "backend_capacity_unobservable", "Backend capacity could not be observed reliably; no build or deployment was started.", "Use a local Linux Docker daemon with readable memory and storage-capacity information, and retry after resolving the observation error.")
	}
	unsupported := func() bool {
		return finish(model.StatusBlocked, "backend_capacity_unsupported", "The bounded backend profile requires the default local Linux Docker daemon; this environment was not established.", "Use the default local Unix-socket Docker daemon on the same Linux host. Nondefault sockets, remote endpoints, proxies and Docker VMs are not qualified for this profile.")
	}
	if ctx.Err() != nil {
		return unobservable()
	}
	if r.platform != "linux" {
		return unsupported()
	}
	// DOCKER_CONTEXT takes precedence over DOCKER_HOST in the Docker CLI. With
	// no host override, inspect the effective context without logging its name.
	endpoint := r.getenv("DOCKER_HOST")
	if endpoint == "" || r.getenv("DOCKER_CONTEXT") != "" {
		result := runner.Run(ctx, command.Request{Name: "docker", Args: []string{"context", "inspect", "--format", "{{json .Endpoints.docker.Host}}"}, Timeout: 10 * time.Second, OutputLimit: 4096})
		if ctx.Err() != nil || failed(result) || result.Truncated || json.Unmarshal([]byte(result.Stdout), &endpoint) != nil {
			return unobservable()
		}
	}
	if endpoint != backendDockerEndpoint {
		return unsupported()
	}
	pinned := scopedRunner{runner: runner, backendDocker: true}
	result := pinned.Run(ctx, command.Request{Name: "docker", Args: []string{"info", "--format", backendDockerInfoFormat}, Timeout: 10 * time.Second, OutputLimit: 16 * 1024})
	var daemon struct {
		Memory uint64 `json:"memory"`
		Root   string `json:"root"`
		OS     string `json:"os"`
		Name   string `json:"name"`
		Kernel string `json:"kernel"`
	}
	if ctx.Err() != nil || failed(result) || result.Truncated || json.Unmarshal([]byte(result.Stdout), &daemon) != nil || daemon.Memory == 0 || daemon.OS == "" || daemon.Name == "" || daemon.Kernel == "" || !filepath.IsAbs(daemon.Root) || strings.ContainsRune(daemon.Root, '\x00') {
		return unobservable()
	}
	// A local Unix socket may proxy a separate VM. Match host identity before
	// treating DockerRootDir as a path in this host's filesystem namespace.
	host, err := r.hostname()
	if err != nil || host == "" {
		return unobservable()
	}
	kernel, err := r.kernel()
	if err != nil || kernel == "" {
		return unobservable()
	}
	if daemon.OS != "linux" || daemon.Name != host || daemon.Kernel != kernel {
		return unsupported()
	}
	total, available, err := r.memory()
	if err != nil || total == 0 || available > total || ctx.Err() != nil {
		return unobservable()
	}
	measurements = append(measurements,
		model.Measurement{Name: "host_memory_total_bytes", Value: strconv.FormatUint(total, 10), Unit: "bytes"},
		model.Measurement{Name: "host_memory_available_bytes", Value: strconv.FormatUint(available, 10), Unit: "bytes"},
		model.Measurement{Name: "docker_memory_total_bytes", Value: strconv.FormatUint(daemon.Memory, 10), Unit: "bytes"},
	)
	disk, err := r.disk(daemon.Root)
	if err != nil || ctx.Err() != nil {
		return unobservable()
	}
	measurements = append(measurements, model.Measurement{Name: "docker_root_available_bytes", Value: strconv.FormatUint(disk, 10), Unit: "bytes"})
	if total < backendMinimumMemory || available < backendMinimumAvailable || daemon.Memory < backendMinimumMemory || disk < backendMinimumDisk {
		return finish(model.StatusBlocked, "backend_capacity_insufficient", "Observed host or Docker capacity is below the bounded backend requirement; no build or deployment was started.", "Provide at least 7 GiB host and Docker memory, 6.5 GiB available host memory, and 30 GiB available Docker storage. Stop competing workloads or use a larger disposable runner.")
	}
	return finish(model.StatusPass, "", "The local Linux backend capacity snapshot meets the fixed budget. This check does not reserve resources or exclude concurrent host activity.", "")
}

func readBackendMemory() (uint64, uint64, error) {
	data, err := readBackendSystemFile("/proc/meminfo", 64<<10)
	if err != nil {
		return 0, 0, err
	}
	return parseBackendMemory(data)
}

func parseBackendMemory(data []byte) (uint64, uint64, error) {
	values := map[string]uint64{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || (fields[0] != "MemTotal:" && fields[0] != "MemAvailable:") {
			continue
		}
		if _, exists := values[fields[0]]; exists || len(fields) != 3 || fields[2] != "kB" {
			return 0, 0, errors.New("invalid memory observation")
		}
		kib, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || kib > math.MaxUint64/1024 {
			return 0, 0, errors.New("invalid memory observation")
		}
		values[fields[0]] = kib * 1024
	}
	total, haveTotal := values["MemTotal:"]
	available, haveAvailable := values["MemAvailable:"]
	if !haveTotal || !haveAvailable || total == 0 || available > total {
		return 0, 0, errors.New("incomplete memory observation")
	}
	return total, available, nil
}

func readBackendKernel() (string, error) {
	data, err := readBackendSystemFile("/proc/sys/kernel/osrelease", 4096)
	return strings.TrimSpace(string(data)), err
}

func readBackendSystemFile(path string, limit int64) ([]byte, error) {
	// #nosec G304 -- only fixed /proc paths are passed by the platform readers.
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("system observation unavailable")
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("system observation exceeded its bound")
	}
	return data, nil
}
