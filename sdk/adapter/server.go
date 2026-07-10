package adapter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

const MaxLineBytes = 8 << 20

type NormalizeFunc func(hook string, payload json.RawMessage) ([]Event, error)

// Serve runs a protocol server until input reaches EOF. It deliberately has no
// persistence API: an adapter can only describe itself and normalize input.
func Serve(in io.Reader, out io.Writer, manifest Manifest, normalize NormalizeFunc) error {
	if err := ValidateManifest(manifest); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), MaxLineBytes)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		var request Request
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			if writeErr := encoder.Encode(errorResponse("", "invalid_request", "decode request: "+err.Error())); writeErr != nil {
				return writeErr
			}
			continue
		}
		if err := ValidateRequest(request); err != nil {
			if writeErr := encoder.Encode(errorResponse(request.ID, "invalid_request", err.Error())); writeErr != nil {
				return writeErr
			}
			continue
		}
		switch request.Method {
		case MethodDescribe:
			response := baseResponse(request.ID, ResponseManifest)
			response.Manifest = &manifest
			if err := encoder.Encode(response); err != nil {
				return err
			}
		case MethodNormalize:
			events, err := normalize(request.Hook, request.Payload)
			if err != nil {
				if writeErr := encoder.Encode(errorResponse(request.ID, "normalize_failed", err.Error())); writeErr != nil {
					return writeErr
				}
				continue
			}
			for index := range events {
				if err := ValidateEvent(events[index]); err != nil {
					if writeErr := encoder.Encode(errorResponse(request.ID, "invalid_event", err.Error())); writeErr != nil {
						return writeErr
					}
					break
				}
				response := baseResponse(request.ID, ResponseEvent)
				response.Event = &events[index]
				if err := encoder.Encode(response); err != nil {
					return err
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read protocol input: %w", err)
	}
	return nil
}

func baseResponse(id string, responseType ResponseType) Response {
	return Response{Protocol: ProtocolName, Version: ProtocolVersion, ID: id, Type: responseType}
}

func errorResponse(id, code, message string) Response {
	response := baseResponse(id, ResponseError)
	response.Error = &Error{Code: code, Message: message}
	return response
}
