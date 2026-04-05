package plugin

import "strings"

// ScrapeOutput checks PTY output data against a plugin's scrape patterns
// and returns any matched key-value pairs.
func ScrapeOutput(p *PanePlugin, data []byte) map[string]string {
	if p == nil || len(p.Persistence.Scrapers) == 0 {
		return nil
	}

	var result map[string]string
	for i := range p.Persistence.Scrapers {
		sp := &p.Persistence.Scrapers[i]
		re := sp.Compiled()
		if re == nil {
			continue
		}
		matches := re.FindSubmatch(data)
		if len(matches) > 1 {
			if result == nil {
				result = make(map[string]string)
			}
			result[sp.Name] = string(matches[1])
		}
	}
	return result
}

// MatchError checks PTY output against a plugin's error handlers.
// Returns the first matching handler, or nil if none match.
func MatchError(p *PanePlugin, data []byte) *ErrorHandler {
	if p == nil || len(p.ErrorHandlers) == 0 {
		return nil
	}

	for i := range p.ErrorHandlers {
		eh := &p.ErrorHandlers[i]
		re := eh.Compiled()
		if re == nil {
			continue
		}
		if re.Match(data) {
			return eh
		}
	}
	return nil
}

// Deprecated: MatchNotification is no longer called from the daemon.
// Notification matching was replaced by idle-time analysis via MatchIdle.
// Kept for backward compatibility with TOML files that define [[notification_handlers]].
func MatchNotification(p *PanePlugin, data []byte) *NotificationHandler {
	if p == nil || len(p.NotificationHandlers) == 0 {
		return nil
	}

	for i := range p.NotificationHandlers {
		nh := &p.NotificationHandlers[i]
		re := nh.Compiled()
		if re == nil {
			continue
		}
		if re.Match(data) {
			return nh
		}
	}
	return nil
}

// MatchIdle checks ANSI-stripped pane text against a plugin's idle handlers.
// Called at idle time with the last few lines of output, not on every chunk.
func MatchIdle(p *PanePlugin, text string) *IdleHandler {
	if p == nil || len(p.IdleHandlers) == 0 {
		return nil
	}

	for i := range p.IdleHandlers {
		ih := &p.IdleHandlers[i]
		re := ih.Compiled()
		if re == nil {
			continue
		}
		if re.MatchString(text) {
			return ih
		}
	}
	return nil
}

// ExpandResumeArgs replaces {key} placeholders in resume args with
// scraped values from plugin state. Returns nil if any placeholder
// remains unresolved (missing state value).
func ExpandResumeArgs(template []string, state map[string]string) []string {
	if len(template) == 0 || len(state) == 0 {
		return nil
	}

	result := make([]string, len(template))
	for i, arg := range template {
		expanded := arg
		for k, v := range state {
			expanded = strings.ReplaceAll(expanded, "{"+k+"}", v)
		}
		// Check for unresolved placeholders
		if strings.Contains(expanded, "{") && strings.Contains(expanded, "}") {
			return nil
		}
		result[i] = expanded
	}
	return result
}

// ExpandMessage replaces {key} placeholders in error messages with
// values from instance args. Supports {host}, {user}, {port} extracted
// from common argument patterns.
func ExpandMessage(msg string, instanceArgs []string) string {
	if len(instanceArgs) == 0 {
		return msg
	}

	// Try to extract user@host:port from args
	vars := extractConnectionVars(instanceArgs)
	for k, v := range vars {
		msg = strings.ReplaceAll(msg, "{"+k+"}", v)
	}
	return msg
}

// extractConnectionVars parses connection-style args (e.g., "user@host")
// into named variables.
func extractConnectionVars(args []string) map[string]string {
	vars := make(map[string]string)

	for i, arg := range args {
		// Skip flags
		if strings.HasPrefix(arg, "-") {
			// Check if next arg is a port number for -p flag
			if arg == "-p" && i+1 < len(args) {
				vars["port"] = args[i+1]
			}
			continue
		}

		// Try user@host pattern
		if strings.Contains(arg, "@") {
			parts := strings.SplitN(arg, "@", 2)
			vars["user"] = parts[0]
			host := parts[1]
			if colonIdx := strings.Index(host, ":"); colonIdx >= 0 {
				vars["host"] = host[:colonIdx]
				vars["port"] = host[colonIdx+1:]
			} else {
				vars["host"] = host
			}
		} else if _, exists := vars["host"]; !exists {
			// Bare hostname
			vars["host"] = arg
		}
	}

	return vars
}
