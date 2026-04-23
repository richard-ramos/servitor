package main

import (
	"regexp"
	"strings"
)

type Redactor struct {
	values   []string
	patterns []*regexp.Regexp
}

func NewRedactor(values ...string) *Redactor {
	var clean []string
	for _, v := range values {
		if len(v) >= 8 {
			clean = append(clean, v)
		}
	}
	pats := []*regexp.Regexp{
		regexp.MustCompile(`sk-[A-Za-z0-9_\-]{16,}`),
		regexp.MustCompile(`gh[pousr]_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(`glpat-[A-Za-z0-9_\-]{20,}`),
		regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]{16,}`),
		regexp.MustCompile(`(?i)(authorization|api-key|x-api-key)\s*[:=]\s*[^\s"']+`),
		regexp.MustCompile(`(?i)([A-Z0-9_]*(KEY|TOKEN|SECRET)[A-Z0-9_]*)\s*=\s*[^\s"']{8,}`),
		regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
	}
	return &Redactor{values: clean, patterns: pats}
}

func (r *Redactor) Redact(s string) string {
	if r == nil || s == "" {
		return s
	}
	out := s
	for _, v := range r.values {
		out = strings.ReplaceAll(out, v, "[REDACTED]")
	}
	for _, p := range r.patterns {
		out = p.ReplaceAllString(out, "[REDACTED]")
	}
	return out
}
