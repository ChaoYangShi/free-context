package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

func latestTranscriptModel(path string) (string, bool, error) {
	return latestTranscriptString(path, parseTranscriptModel)
}

func latestTranscriptTurnID(path string) (string, bool, error) {
	return latestTranscriptString(path, parseTranscriptTurnID)
}

func latestTranscriptString(path string, parse func([]byte) (string, bool, error)) (string, bool, error) {
	if path == "" {
		return "", false, nil
	}
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("open transcript: %w", err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	model := ""
	found := false
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) != 0 {
			value, ok, err := parse(line)
			if err != nil && readErr != io.EOF {
				return "", false, err
			}
			if err == nil && ok {
				model = value
				found = true
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", false, fmt.Errorf("read transcript: %w", readErr)
		}
	}
	return model, found, nil
}

func parseTranscriptModel(line []byte) (string, bool, error) {
	var event struct {
		Type    string `json:"type"`
		Payload struct {
			Model string `json:"model"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		return "", false, fmt.Errorf("decode transcript event: %w", err)
	}
	if event.Type != "turn_context" || event.Payload.Model == "" {
		return "", false, nil
	}
	return event.Payload.Model, true, nil
}

func parseTranscriptTurnID(line []byte) (string, bool, error) {
	var event struct {
		Type    string `json:"type"`
		Payload struct {
			TurnID string `json:"turn_id"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		return "", false, fmt.Errorf("decode transcript event: %w", err)
	}
	if event.Type != "turn_context" || event.Payload.TurnID == "" {
		return "", false, nil
	}
	return event.Payload.TurnID, true, nil
}
