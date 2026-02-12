package local

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

type LocalClient struct{}

type LocalCommand struct {
	Command string            `json:"command"`
	Env     map[string]string `json:"env"`
	WorkDir string            `json:"work_dir"`
	Timeout time.Duration     `json:"timeout"`
}

type LocalResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Duration string `json:"duration"`
}

func NewLocalClient() *LocalClient {
	return &LocalClient{}
}

func (c *LocalClient) ExecuteCommand(ctx context.Context, cmd *LocalCommand) (*LocalResult, error) {
	if cmd.Timeout == 0 {
		cmd.Timeout = 30 * time.Second
	}

	// Create command with context
	execCmd := exec.CommandContext(ctx, "sh", "-c", cmd.Command)

	// Set environment variables
	if cmd.Env != nil {
		env := execCmd.Env
		for key, value := range cmd.Env {
			env = append(env, fmt.Sprintf("%s=%s", key, value))
		}
		execCmd.Env = env
	}

	// Set working directory
	if cmd.WorkDir != "" {
		execCmd.Dir = cmd.WorkDir
	}

	// Execute command with timeout
	var stdout, stderr bytes.Buffer
	execCmd.Stdout = &stdout
	execCmd.Stderr = &stderr

	start := time.Now()
	err := execCmd.Start()
	if err != nil {
		return nil, fmt.Errorf("failed to start command: %w", err)
	}

	// Wait for command to finish or timeout
	done := make(chan error, 1)
	go func() {
		done <- execCmd.Wait()
	}()

	select {
	case <-ctx.Done():
		execCmd.Process.Kill()
		return nil, fmt.Errorf("command timed out")
	case err := <-done:
		duration := time.Since(start)

		result := &LocalResult{
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			Duration: duration.String(),
		}

		if err != nil {
			if exitError, ok := err.(*exec.ExitError); ok {
				result.ExitCode = exitError.ExitCode()
			} else {
				result.ExitCode = -1
				result.Stderr += fmt.Sprintf("\nExecution error: %v", err)
			}
		} else {
			result.ExitCode = 0
		}

		return result, nil
	}
}
