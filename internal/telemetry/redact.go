package telemetry

import (
	"regexp"
	"strings"
)

// secrets are what Scrub removes before a string reaches telemetry (ynr ADR-006, rule 10): ynf
// redacts at the source, so what the spool holds, and an operator's own endpoint receives, is
// already clean.
var secrets = []*regexp.Regexp{
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{16,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{16,}`),
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`(?i)x-access-token:[^@\s]+`),
	regexp.MustCompile(`(?i)(authorization|token|secret|password|passwd|api[_-]?key)(["']?\s*[:=]\s*["']?)[^\s"',;&]+`),
	regexp.MustCompile(`://[^/@\s:]+:[^/@\s]+@`),
}

// emails are people's addresses, which never appear: people are handles (ynr ADR-002).
var emails = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+`)

const redacted = "[redacted]"

// Scrub removes secrets and email addresses from s.
func Scrub(s string) string {
	for i, re := range secrets {
		switch i {
		case 7:
			s = re.ReplaceAllString(s, "${1}${2}"+redacted)
		case 8:
			s = re.ReplaceAllString(s, "://"+redacted+"@")
		default:
			s = re.ReplaceAllString(s, redacted)
		}
	}
	return emails.ReplaceAllString(s, redacted)
}

// Handle is a person as telemetry names them: the handle they have in the system where they
// acted, qualified by that system's host, such as github.com/octocat. It is "" for a login that is
// empty or looks like an email address or a name with spaces, since those are never exported.
func Handle(host, login string) string {
	login = strings.TrimSpace(login)
	if host == "" || login == "" || strings.ContainsAny(login, "@ \t\n/") {
		return ""
	}
	return host + "/" + login
}
