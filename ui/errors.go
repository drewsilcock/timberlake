package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WriteErrorLog writes any per-file upload failures to a timestamped log file in
// the current directory and returns its absolute path. If there were no
// failures it writes nothing and returns an empty path.
func WriteErrorLog(m Model) (string, error) {
	if len(m.Errors) == 0 {
		return "", nil
	}

	name := fmt.Sprintf("timberlake-errors-%s.log", time.Now().Format("20060102-150405"))
	f, err := os.Create(name)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	fmt.Fprintf(f, "Timberlake error log — %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(f, "Source:      %s\n", m.Config.SourceDir)
	fmt.Fprintf(f, "Destination: s3://%s/%s\n", m.Config.Bucket, m.Config.Prefix)
	fmt.Fprintf(f, "\n%d file(s) failed to upload:\n\n", len(m.Errors))
	for _, e := range m.Errors {
		fmt.Fprintf(f, "%s\n    %s\n\n", e.RelativePath, e.Message)
	}

	abs, err := filepath.Abs(name)
	if err != nil {
		return name, nil
	}
	return abs, nil
}
