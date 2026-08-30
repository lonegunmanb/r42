package spec

import (
	"fmt"
	"strings"
)

type Issue struct {
	ID         string  `json:"id,omitempty"`
	Code       string  `json:"code"`
	Message    string  `json:"message"`
	Path       *string `json:"path,omitempty"`
	RepairHint *string `json:"repair_hint,omitempty"`
}

func (i Issue) Validate() error {
	if strings.TrimSpace(i.Code) == "" {
		return fmt.Errorf("issue code is required")
	}
	if strings.TrimSpace(i.Message) == "" {
		return fmt.Errorf("issue message is required")
	}

	return nil
}

type ToolResponse[T any] struct {
	Accepted bool    `json:"accepted"`
	Output   *T      `json:"output,omitempty"`
	Issues   []Issue `json:"issues,omitempty"`
}

func (r ToolResponse[T]) Validate() error {
	if r.Accepted && len(r.Issues) != 0 {
		return fmt.Errorf("accepted response must not contain issues")
	}
	if !r.Accepted && r.Output != nil {
		return fmt.Errorf("rejected response must not contain output")
	}
	if !r.Accepted && len(r.Issues) == 0 {
		return fmt.Errorf("rejected response must contain at least one issue")
	}
	for index, issue := range r.Issues {
		if err := issue.Validate(); err != nil {
			return fmt.Errorf("issue %d: %w", index, err)
		}
	}

	return nil
}
