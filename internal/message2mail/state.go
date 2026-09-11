package message2mail

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const stateVersion = 1

type DeliveryStatus string

const (
	StatusSeeded    DeliveryStatus = "seeded"
	StatusPending   DeliveryStatus = "pending"
	StatusDelivered DeliveryStatus = "delivered"
	StatusRetryWait DeliveryStatus = "retry_wait"
	StatusFailed    DeliveryStatus = "failed"
)

type Delivery struct {
	NewsID          string         `json:"news_id"`
	ListFingerprint string         `json:"list_fingerprint"`
	Revision        string         `json:"revision,omitempty"`
	Status          DeliveryStatus `json:"status"`
	Attempts        int            `json:"attempts,omitempty"`
	LastAttempt     time.Time      `json:"last_attempt,omitempty"`
	DeliveredAt     time.Time      `json:"delivered_at,omitempty"`
}

type State struct {
	Version     int                 `json:"version"`
	Initialized bool                `json:"initialized"`
	Deliveries  map[string]Delivery `json:"deliveries"`
}

type StateStore interface {
	Load() (State, error)
	Save(State) error
}

type FileStateStore struct {
	Path string
}

func (s FileStateStore) Load() (State, error) {
	state := State{Version: stateVersion, Deliveries: make(map[string]Delivery)}
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read delivery state: %w", err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("decode delivery state: %w", err)
	}
	if state.Version != stateVersion {
		return State{}, fmt.Errorf("unsupported delivery state version %d", state.Version)
	}
	if state.Deliveries == nil {
		state.Deliveries = make(map[string]Delivery)
	}
	return state, nil
}

func (s FileStateStore) Save(state State) error {
	if s.Path == "" {
		return errors.New("state path is required")
	}
	state.Version = stateVersion
	directory := filepath.Dir(s.Path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".message2mail-state-*")
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("set state permissions: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(state); err != nil {
		return fmt.Errorf("encode delivery state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync delivery state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close delivery state: %w", err)
	}
	if err := os.Rename(temporaryName, s.Path); err != nil {
		return fmt.Errorf("replace delivery state: %w", err)
	}
	removeTemporary = false
	if directoryHandle, err := os.Open(directory); err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return nil
}
