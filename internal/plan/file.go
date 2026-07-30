package plan

import (
	"fmt"
	"os"
)

func Save(path string, planned *Plan) (string, error) {
	encoded, err := Marshal(planned)
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("open plan file: %w", err)
	}
	if err = restrictPlanFile(path, file); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("restrict plan file permissions: %w", err)
	}
	if _, err = file.Write(encoded); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write plan file: %w", err)
	}
	if err = file.Close(); err != nil {
		return "", fmt.Errorf("close plan file: %w", err)
	}
	return fmt.Sprintf("plan file %q is unencrypted and may contain sensitive values", path), nil
}

func Load(path string) (*Plan, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read plan file: %w", err)
	}
	planned, err := Unmarshal(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode plan file: %w", err)
	}
	return planned, nil
}
