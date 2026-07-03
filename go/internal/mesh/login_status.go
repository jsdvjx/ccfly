package mesh

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const (
	LoginPhaseInit         = "init"
	LoginPhaseCheckChannel = "checking_channel"
	LoginPhaseWaitChannel  = "waiting_channel"
	LoginPhaseStarting     = "starting"
	LoginPhasePolling      = "polling"
	LoginPhaseDecrypting   = "decrypting"
	LoginPhaseWriting      = "writing"
	LoginPhaseSucceeded    = "succeeded"
	LoginPhaseFailed       = "failed"
)

type LoginStatus struct {
	Phase       string `json:"phase"`
	JobID       string `json:"job_id,omitempty"`
	Email       string `json:"email,omitempty"`
	EgressV6    string `json:"egress_v6,omitempty"`
	Host        string `json:"host"`
	PID         int    `json:"pid"`
	Error       string `json:"error,omitempty"`
	StartedAt   int64  `json:"started_at"`
	UpdatedAt   int64  `json:"updated_at"`
	CloudStatus string `json:"cloud_status,omitempty"`
}

func (s *LoginStatus) Terminal() bool {
	return s.Phase == LoginPhaseSucceeded || s.Phase == LoginPhaseFailed
}

func loginStatusPath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "claude-login-status.json"), nil
}

func SaveLoginStatus(s LoginStatus) error {
	s.UpdatedAt = time.Now().Unix()
	p, err := loginStatusPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func LoadLoginStatus() (*LoginStatus, error) {
	p, err := loginStatusPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var s LoginStatus
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func ClearLoginStatus() error {
	p, err := loginStatusPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
