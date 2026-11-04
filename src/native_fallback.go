package src

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

var errNativeConfigRead = errors.New("cannot verify Empryo's native fallback configuration")

type nativeRPCReply struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Native fallback layers read through the supported ACP configuration interface.
// Read-only plans never call config/set; explicit application writes only modelFallback and defaultModel.
type nativeFallbackSnapshot struct {
	Global    map[string][]string
	Project   map[string][]string
	Effective map[string][]string
}

func (snapshot *nativeFallbackSnapshot) declaredFallbacks() map[string][]string {
	if snapshot.Project != nil {
		return snapshot.Project
	}
	return snapshot.Global
}

const nativeConfigInitialBuffer = 4096

const nativeConfigReadTimeout = 15 * time.Second
const nativeConfigShutdownTimeout = 2 * time.Second
const nativeConfigReplyLimit = 8 * 1024 * 1024

func readNativeFallbacks(ctx context.Context, bin, workspace string) (*nativeFallbackSnapshot, error) {
	var snapshot *nativeFallbackSnapshot
	err := withNativeConfig(ctx, bin, workspace, func(request nativeConfigRequest, session string) error {
		var err error
		snapshot, err = readNativeFallbackSnapshot(request, session)
		return err
	})
	return snapshot, err
}

type nativeConfigRequest func(string, any, any) error

func withNativeConfig(parent context.Context, bin, workspace string, use func(nativeConfigRequest, string) error) (err error) {
	ctx, cancel := context.WithTimeout(parent, nativeConfigReadTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "acp")
	cmd.Dir = workspace
	cmd.Env = append(os.Environ(), "EMPRYO_NO_GENOME=1", "CODEFORGE_GUARD=review", "EMPRYO_HOOK_ENV=CODEFORGE_*", "CODEFORGE_LAUNCH=0", "CODEFORGE_RESUME_HINT=launcher")
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("%w: %w", errNativeConfigRead, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("%w: %w", errNativeConfigRead, err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("%w: %w", errNativeConfigRead, err)
	}
	defer func() {
		_ = stdin.Close()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case waitErr := <-done:
			_ = waitErr
		case <-time.After(nativeConfigShutdownTimeout):
			_ = cmd.Process.Kill() //nolint:errcheck // Best-effort cleanup; the context also bounds process lifetime.
			waitErr := <-done
			_ = waitErr
		}
	}()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, nativeConfigInitialBuffer), nativeConfigReplyLimit)
	id := 0
	request := func(method string, params any, result any) error {
		id++
		message := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
		if err := json.NewEncoder(stdin).Encode(message); err != nil {
			return fmt.Errorf("%w: sending %s: %w", errNativeConfigRead, method, err)
		}
		for scanner.Scan() {
			var reply nativeRPCReply
			if json.Unmarshal(scanner.Bytes(), &reply) != nil || reply.ID != id {
				continue
			}
			if reply.Error != nil {
				return fmt.Errorf("%w: %s: %s", errNativeConfigRead, method, reply.Error.Message)
			}
			if result != nil {
				if err := json.Unmarshal(reply.Result, result); err != nil {
					return fmt.Errorf("%w: invalid %s result: %w", errNativeConfigRead, method, err)
				}
			}
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %w", errNativeConfigRead, ctx.Err())
		}
		return fmt.Errorf("%w: %s closed before a response", errNativeConfigRead, method)
	}
	capabilities := map[string]any{"_meta": map[string]any{"empryo": map[string]any{"protocol": "1.0", "extensions": map[string]any{"config": 1}}}}
	if err := request("initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": capabilities}, nil); err != nil {
		return err
	}
	var opened struct {
		SessionID string `json:"sessionId"` //nolint:tagliatelle // ACP wire format.
	} //nolint:tagliatelle // ACP wire format.
	if err := request("session/new", map[string]any{"cwd": workspace, "mcpServers": []any{}}, &opened); err != nil {
		return fmt.Errorf("%w; choose a model in Empryo first if no native model is selected", err)
	}
	if opened.SessionID == "" {
		return fmt.Errorf("%w: ACP did not return a query session", errNativeConfigRead)
	}
	defer func() {
		err = errors.Join(err, request("session/delete", map[string]any{"sessionId": opened.SessionID}, nil))
	}()
	return use(request, opened.SessionID)
}

func readNativeFallbackSnapshot(request nativeConfigRequest, session string) (*nativeFallbackSnapshot, error) {
	snapshot := &nativeFallbackSnapshot{}
	for _, layer := range []struct {
		scope  string
		target *map[string][]string
	}{{"global", &snapshot.Global}, {"project", &snapshot.Project}, {"effective", &snapshot.Effective}} {
		var read struct {
			Values map[string]json.RawMessage `json:"values"`
		}
		if err := request("_empryo/config/get", map[string]any{"sessionId": session, "scope": layer.scope, "keys": []string{"modelFallback"}}, &read); err != nil {
			return nil, err
		}
		if data, ok := read.Values["modelFallback"]; ok {
			if err := json.Unmarshal(data, layer.target); err != nil {
				return nil, fmt.Errorf("%w: malformed %s fallback map: %w", errNativeConfigRead, layer.scope, err)
			}
		}
	}
	return snapshot, nil
}
