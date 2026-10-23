package watcher

import (
	"bytes"
	"fmt"
	"regexp"
	"unicode"

	"github.com/meetmehta/clipsync/internal/clipboard"
)

// Filter decides whether a local clip may leave the device.
type Filter struct {
	MaxBytes int64
	Images   bool
	// Secrets enables the built-in detectors below.
	Secrets bool
	Extra   []*regexp.Regexp
}

var secretPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"private key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"AWS key", regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"GitHub token", regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{40,})\b`)},
	{"Slack token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`)},
	{"Stripe key", regexp.MustCompile(`\b[rs]k_live_[A-Za-z0-9]{20,}`)},
	{"API key", regexp.MustCompile(`\bsk-(ant-|proj-)?[A-Za-z0-9_-]{32,}`)},
	{"JWT", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
}

var cardCandidate = regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`)

// maxScan bounds regex work on huge text clips.
const maxScan = 64 << 10

// Check returns "" if c may be synced, or the reason it was blocked.
func (f Filter) Check(c clipboard.Content) string {
	if c.Concealed {
		return "marked concealed by the source app"
	}
	if c.Empty() {
		return "empty"
	}
	if f.MaxBytes > 0 && int64(len(c.Data)) > f.MaxBytes {
		return fmt.Sprintf("too large (%d bytes > %d)", len(c.Data), f.MaxBytes)
	}
	if c.Type == clipboard.Image && !f.Images {
		return "image sync disabled"
	}
	if c.Type != clipboard.Text {
		return ""
	}
	if len(bytes.TrimFunc(c.Data, unicode.IsSpace)) == 0 {
		return "whitespace only"
	}
	text := c.Data
	if len(text) > maxScan {
		text = text[:maxScan]
	}
	if f.Secrets {
		for _, p := range secretPatterns {
			if p.re.Match(text) {
				return "looks like a " + p.name
			}
		}
		for _, m := range cardCandidate.FindAll(text, -1) {
			if luhn(m) {
				return "looks like a card number"
			}
		}
	}
	for _, re := range f.Extra {
		if re.Match(text) {
			return "matched filter " + re.String()
		}
	}
	return ""
}

// luhn validates a card number, ignoring spaces and dashes.
func luhn(b []byte) bool {
	sum, n, double := 0, 0, false
	for i := len(b) - 1; i >= 0; i-- {
		c := b[i]
		if c == ' ' || c == '-' {
			continue
		}
		d := int(c - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
		n++
	}
	return n >= 13 && n <= 19 && sum%10 == 0
}
