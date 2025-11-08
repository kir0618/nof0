package llm

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
)

const defaultPromptHeaderScanLimit = 8 * 1024

var versionHeaderRegexp = regexp.MustCompile(`(?i)version:\s*([a-z0-9._-]+)`)

// TemplateVersionGuard validates prompt templates declare the expected schema version.
type TemplateVersionGuard struct {
	Component            string
	ExpectedVersion      string
	RequireVersionHeader bool
	StrictMode           bool
	ScanLimit            int
	Logger               func(format string, args ...any)
}

// Enforce validates the template at templatePath meets the guard expectations and returns the parsed version.
func (g TemplateVersionGuard) Enforce(templatePath string) (string, error) {
	templatePath = strings.TrimSpace(templatePath)
	if templatePath == "" {
		return "", fmt.Errorf("prompt template path is empty")
	}
	version, err := ExtractTemplateVersion(templatePath, g.scanLimit())
	if err != nil {
		if g.RequireVersionHeader {
			return "", err
		}
		g.logf("%s", err)
		return "", nil
	}
	expected := strings.TrimSpace(g.ExpectedVersion)
	if expected != "" && version != expected {
		msg := fmt.Sprintf("%s template %s declared version %s but expected %s", g.componentName(), templatePath, version, expected)
		if g.StrictMode {
			return "", fmt.Errorf(msg)
		}
		g.logf("%s", msg)
	}
	return version, nil
}

// ExtractTemplateVersion scans the file for a {{/* Version: ... */}} style header.

func ExtractTemplateVersion(templatePath string, scanLimit int) (string, error) {
	data, err := os.ReadFile(templatePath)
	if err != nil {
		return "", fmt.Errorf("read prompt template %q: %w", templatePath, err)
	}
	if scanLimit <= 0 {
		scanLimit = defaultPromptHeaderScanLimit
	}
	if scanLimit > len(data) {
		scanLimit = len(data)
	}
	content := string(data[:scanLimit])
	matches := versionHeaderRegexp.FindStringSubmatch(content)
	if len(matches) < 2 {
		return "", fmt.Errorf("prompt template %s missing Version header (expected {{/* Version: <semver> */}})", templatePath)
	}
	return strings.TrimSpace(matches[1]), nil
}

func (g TemplateVersionGuard) scanLimit() int {
	if g.ScanLimit > 0 {
		return g.ScanLimit
	}
	return defaultPromptHeaderScanLimit
}

func (g TemplateVersionGuard) logf(format string, args ...any) {
	logger := g.Logger
	if logger == nil {
		logger = log.Printf
	}
	logger(format, args...)
}

func (g TemplateVersionGuard) componentName() string {
	if strings.TrimSpace(g.Component) != "" {
		return g.Component
	}
	return "prompt"
}
