package primitives

import (
	"fmt"
	"strings"
)

func invalid(name, value, reason string) error {
	if value == "" {
		return fmt.Errorf("invalid %s: %s", name, reason)
	}
	return fmt.Errorf("invalid %s %q: %s", name, value, reason)
}

func isASCIIAlpha(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func isASCIILowerAlpha(r rune) bool {
	return r >= 'a' && r <= 'z'
}

func isASCIIDigit(r rune) bool {
	return r >= '0' && r <= '9'
}

func isASCIIAlphaNum(r rune) bool {
	return isASCIIAlpha(r) || isASCIIDigit(r)
}

func isASCIILowerAlphaNum(r rune) bool {
	return isASCIILowerAlpha(r) || isASCIIDigit(r)
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !isASCIIDigit(r) {
			return false
		}
	}
	return true
}

func validateRefSafeComponent(name, value string, maxLen int, allowUpper bool) error {
	if value == "" {
		return invalid(name, value, "must not be empty")
	}
	if len(value) > maxLen {
		return invalid(name, value, fmt.Sprintf("must be at most %d bytes", maxLen))
	}
	if value == "." || value == ".." {
		return invalid(name, value, "must not be . or ..")
	}
	if strings.Contains(value, "..") {
		return invalid(name, value, "must not contain ..")
	}
	if strings.Contains(value, "@{") {
		return invalid(name, value, "must not contain @{")
	}
	if strings.HasSuffix(value, ".") {
		return invalid(name, value, "must not end with .")
	}
	if strings.HasSuffix(value, ".lock") {
		return invalid(name, value, "must not end with .lock")
	}

	for i, r := range value {
		if i == 0 {
			if allowUpper {
				if !isASCIIAlphaNum(r) {
					return invalid(name, value, "must start with an ASCII letter or digit")
				}
			} else if !isASCIILowerAlphaNum(r) {
				return invalid(name, value, "must start with a lowercase ASCII letter or digit")
			}
		}

		if allowUpper && isASCIIAlphaNum(r) {
			continue
		}
		if !allowUpper && isASCIILowerAlphaNum(r) {
			continue
		}
		if r == '.' || r == '_' || r == '-' {
			continue
		}
		return invalid(name, value, "may only contain ASCII letters, digits, dot, underscore, or hyphen")
	}

	return nil
}

func validateGitRefName(ref string) error {
	if ref == "" {
		return invalid("git ref", ref, "must not be empty")
	}
	if strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") {
		return invalid("git ref", ref, "must not start or end with /")
	}
	if strings.Contains(ref, "//") {
		return invalid("git ref", ref, "must not contain //")
	}
	if strings.Contains(ref, "..") {
		return invalid("git ref", ref, "must not contain ..")
	}
	if ref == "@" || strings.Contains(ref, "@{") {
		return invalid("git ref", ref, "must not be @ or contain @{")
	}
	if strings.HasSuffix(ref, ".") {
		return invalid("git ref", ref, "must not end with .")
	}

	for _, part := range strings.Split(ref, "/") {
		if part == "" {
			return invalid("git ref", ref, "must not contain empty path components")
		}
		if strings.HasPrefix(part, ".") {
			return invalid("git ref", ref, "components must not start with .")
		}
		if strings.HasSuffix(part, ".lock") {
			return invalid("git ref", ref, "components must not end with .lock")
		}
	}

	for _, r := range ref {
		if r <= ' ' || r == 0x7f {
			return invalid("git ref", ref, "must not contain control characters or spaces")
		}
		switch r {
		case '~', '^', ':', '?', '*', '[', '\\':
			return invalid("git ref", ref, "contains a character Git refs do not allow")
		}
	}

	return nil
}
