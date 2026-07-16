package adapter

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConformanceFixturesAreValidProtocolLines(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "conformance", "v1", "*.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("fixture files = %d, want 4", len(files))
	}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		line := bytes.TrimSpace(data)
		if !json.Valid(line) {
			t.Fatalf("%s is not valid JSON", path)
		}
		if strings.Contains(filepath.Base(path), "request") {
			var request Request
			decodeErr := json.Unmarshal(line, &request)
			validateErr := ValidateRequest(request)
			if decodeErr != nil || validateErr != nil {
				t.Fatalf("invalid request fixture %s: decode=%v validate=%v", path, decodeErr, validateErr)
			}
		} else {
			var response Response
			if err := json.Unmarshal(line, &response); err != nil {
				t.Fatalf("invalid response fixture %s: %v", path, err)
			}
			if response.Manifest != nil && ValidateManifest(*response.Manifest) != nil {
				t.Fatalf("invalid manifest fixture %s: %v", path, ValidateManifest(*response.Manifest))
			}
			if response.Event != nil && ValidateEvent(*response.Event) != nil {
				t.Fatalf("invalid event fixture %s: %v", path, ValidateEvent(*response.Event))
			}
		}
	}
}

func TestServeDescribeAndNormalize(t *testing.T) {
	manifest := testManifest()
	input := "{\"protocol\":\"turnal-adapter\",\"version\":1,\"id\":\"one\",\"method\":\"describe\"}\n" +
		"{\"protocol\":\"turnal-adapter\",\"version\":1,\"id\":\"two\",\"method\":\"normalize\",\"hook\":\"prompt\",\"payload\":{}}\n"
	var output bytes.Buffer
	err := Serve(bytes.NewBufferString(input), &output, manifest, func(hook string, payload json.RawMessage) ([]Event, error) {
		return []Event{{Type: EventPromptUser, SessionID: "fixture", CWD: "/workspace", Text: "hello"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	var describe, event Response
	if err := decoder.Decode(&describe); err != nil || describe.Type != ResponseManifest || describe.Manifest == nil {
		t.Fatalf("describe response = %#v err=%v", describe, err)
	}
	if err := decoder.Decode(&event); err != nil || event.Type != ResponseEvent || event.Event == nil || event.Event.Text != "hello" {
		t.Fatalf("event response = %#v err=%v", event, err)
	}
}

func TestServeRejectsUnsupportedVersionWithoutCallingNormalizer(t *testing.T) {
	manifest := testManifest()
	input := bytes.NewBufferString("{\"protocol\":\"turnal-adapter\",\"version\":2,\"id\":\"bad\",\"method\":\"normalize\",\"hook\":\"x\",\"payload\":{}}\n")
	var output bytes.Buffer
	if err := Serve(input, &output, manifest, func(string, json.RawMessage) ([]Event, error) {
		t.Fatal("normalizer called")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &response); err != nil || response.Type != ResponseError || response.Error.Code != "invalid_request" {
		t.Fatalf("response = %#v err=%v", response, err)
	}
}

func TestValidateEventAcceptsCrossPlatformAbsolutePaths(t *testing.T) {
	for _, cwd := range []string{"/workspace", `C:\workspace`, `\\server\share`} {
		event := Event{Type: EventTurnFinish, SessionID: "fixture", CWD: cwd}
		if err := ValidateEvent(event); err != nil {
			t.Fatalf("ValidateEvent cwd %q: %v", cwd, err)
		}
	}
}

func testManifest() Manifest {
	return Manifest{
		Name: "example", DisplayName: "Example", AdapterVersion: "1.0.0", Provider: "Example",
		ProtocolVersions: []int{1}, EventTypes: []string{string(EventPromptUser)},
	}
}
