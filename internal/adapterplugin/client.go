package adapterplugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	adaptersdk "github.com/AadiJo/turnal/sdk/adapter"
)

const executablePrefix = "turnal-adapter-"

type Adapter struct {
	Name string
	Path string
}

type Inspection struct {
	Adapter
	Manifest adaptersdk.Manifest
	Duration time.Duration
	Err      error
}

func Discover() ([]Adapter, error) {
	seenPaths := map[string]bool{}
	byName := map[string]Adapter{}
	dirs := filepath.SplitList(os.Getenv("PATH"))
	if executable, err := os.Executable(); err == nil {
		dirs = append([]string{filepath.Dir(executable)}, dirs...)
	}
	for _, dir := range dirs {
		if dir == "" || seenPaths[dir] {
			continue
		}
		seenPaths[dir] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			filename := entry.Name()
			if runtime.GOOS == "windows" {
				extension := filepath.Ext(filename)
				if !strings.EqualFold(extension, ".exe") {
					continue
				}
				filename = strings.TrimSuffix(filename, extension)
			}
			name := strings.TrimPrefix(filename, executablePrefix)
			if name == filename {
				continue
			}
			if name == "" {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			info, err := entry.Info()
			if err != nil || runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
				continue
			}
			if _, exists := byName[name]; !exists {
				absolute, err := filepath.Abs(path)
				if err == nil {
					byName[name] = Adapter{Name: name, Path: absolute}
				}
			}
		}
	}
	adapters := make([]Adapter, 0, len(byName))
	for _, adapter := range byName {
		adapters = append(adapters, adapter)
	}
	sort.Slice(adapters, func(i, j int) bool { return adapters[i].Name < adapters[j].Name })
	return adapters, nil
}

func Find(name string) (Adapter, error) {
	executable := executablePrefix + name
	lookupName := executable
	if runtime.GOOS == "windows" {
		lookupName += ".exe"
		if adapter, found := findSibling(name, lookupName); found {
			return adapter, nil
		}
	}
	path, err := exec.LookPath(lookupName)
	if err == nil {
		absolute, absErr := filepath.Abs(path)
		if absErr == nil {
			return Adapter{Name: name, Path: absolute}, nil
		}
	}
	if runtime.GOOS != "windows" {
		if adapter, found := findSibling(name, executable); found {
			return adapter, nil
		}
	}
	return Adapter{}, fmt.Errorf("adapter %q not found on PATH (expected %s)", name, executable)
}

func findSibling(name, executable string) (Adapter, bool) {
	current, err := os.Executable()
	if err != nil {
		return Adapter{}, false
	}
	candidate := filepath.Join(filepath.Dir(current), executable)
	if info, err := os.Stat(candidate); err != nil || info.IsDir() {
		return Adapter{}, false
	}
	return Adapter{Name: name, Path: candidate}, true
}

func Inspect(ctx context.Context, adapter Adapter) Inspection {
	started := time.Now()
	request := adaptersdk.NewRequest("doctor", adaptersdk.MethodDescribe)
	responses, err := exchange(ctx, adapter.Path, request)
	inspection := Inspection{Adapter: adapter, Duration: time.Since(started), Err: err}
	if err != nil {
		return inspection
	}
	if len(responses) != 1 || responses[0].Type != adaptersdk.ResponseManifest || responses[0].Manifest == nil {
		inspection.Err = fmt.Errorf("describe returned %d response(s), expected one manifest", len(responses))
		return inspection
	}
	inspection.Manifest = *responses[0].Manifest
	if inspection.Manifest.Name != adapter.Name {
		inspection.Err = fmt.Errorf("manifest name %q does not match executable name %q", inspection.Manifest.Name, adapter.Name)
		return inspection
	}
	inspection.Err = adaptersdk.ValidateManifest(inspection.Manifest)
	return inspection
}

func Normalize(ctx context.Context, adapter Adapter, hook string, payload json.RawMessage) ([]adaptersdk.Event, error) {
	request := adaptersdk.NewRequest("capture", adaptersdk.MethodNormalize)
	request.Hook = hook
	request.Payload = payload
	responses, err := exchange(ctx, adapter.Path, request)
	if err != nil {
		return nil, err
	}
	events := make([]adaptersdk.Event, 0, len(responses))
	for _, response := range responses {
		switch response.Type {
		case adaptersdk.ResponseEvent:
			if response.Event == nil {
				return nil, fmt.Errorf("adapter returned event response without event")
			}
			if err := adaptersdk.ValidateEvent(*response.Event); err != nil {
				return nil, fmt.Errorf("adapter returned invalid event: %w", err)
			}
			events = append(events, *response.Event)
		case adaptersdk.ResponseError:
			if response.Error == nil {
				return nil, fmt.Errorf("adapter returned an unspecified error")
			}
			return nil, fmt.Errorf("adapter %s: %s", response.Error.Code, response.Error.Message)
		default:
			return nil, fmt.Errorf("unexpected adapter response type %q", response.Type)
		}
	}
	return events, nil
}

func exchange(ctx context.Context, path string, request adaptersdk.Request) ([]adaptersdk.Response, error) {
	input, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	input = append(input, '\n')
	command := exec.CommandContext(ctx, path)
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("adapter timed out: %w", ctx.Err())
		}
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return nil, fmt.Errorf("run adapter: %w: %s", err, message)
		}
		return nil, fmt.Errorf("run adapter: %w", err)
	}
	scanner := bufio.NewScanner(&stdout)
	scanner.Buffer(make([]byte, 64*1024), adaptersdk.MaxLineBytes)
	var responses []adaptersdk.Response
	for scanner.Scan() {
		var response adaptersdk.Response
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			return nil, fmt.Errorf("decode adapter response: %w", err)
		}
		if response.Protocol != adaptersdk.ProtocolName || response.Version != adaptersdk.ProtocolVersion || response.ID != request.ID {
			return nil, fmt.Errorf("adapter response has mismatched protocol, version, or request id")
		}
		responses = append(responses, response)
		if len(responses) > 128 {
			return nil, fmt.Errorf("adapter returned too many responses")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read adapter response: %w", err)
	}
	return responses, nil
}
