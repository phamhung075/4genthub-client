// Package secretscan detects credential-shaped text so seat status reports never
// store or echo secrets. The same fixture (testdata/scan_cases.json) drives the Python
// bridge scrubber.
package secretscan

import "regexp"

var patterns = []*regexp.Regexp{
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
	regexp.MustCompile(`Bearer\s+[A-Za-z0-9._~+/=-]{20,}`),
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{30,}`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`://[^\s/:@]*:[^\s/]+@`),
	regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key)\s*[=:]\s*\S{6,}`),
}

// Contains reports whether text holds a credential-shaped substring.
func Contains(text string) bool {
	for _, p := range patterns {
		if p.MatchString(text) {
			return true
		}
	}
	return false
}
