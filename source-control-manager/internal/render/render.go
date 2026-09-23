// Package render writes every text the SCM puts into Git and the Git host:
// the commit subject and Change-Set trailer, the refresh merge message, and
// the pull-request title and body. The same inputs give the same text,
// byte for byte.
package render

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"

	"github.com/redhat-et/protobot/source-control-manager/internal/ears"
)

// OneLine replaces every run of white space with one space and trims the
// ends.
func OneLine(text string) string {
	return strings.Join(strings.FieldsFunc(text, unicode.IsSpace), " ")
}

// Trailer returns the Change-Set trailer.
func Trailer(changeSetID string) string { return "Change-Set: " + changeSetID }

// Subject returns the commit subject.
func Subject(changeSetID, intent string) string {
	return "spec(" + changeSetID + "): " + OneLine(intent)
}

// Title returns the pull-request title: the subject without the spec()
// prefix.
func Title(intent string) string { return OneLine(intent) }

// CommitMessage returns the full commit message: the subject, the optional
// body after a blank line, and the trailer as the last paragraph.
func CommitMessage(changeSetID, intent, body string) string {
	var b strings.Builder
	b.WriteString(Subject(changeSetID, intent))
	b.WriteString("\n\n")
	if body = TrimBody(body); body != "" {
		b.WriteString(body)
		b.WriteString("\n\n")
	}
	b.WriteString(Trailer(changeSetID))
	b.WriteString("\n")
	return b.String()
}

// TrimBody drops blank lines and trailing white space at both ends of a
// commit body.
func TrimBody(body string) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimRightFunc(lines[i], unicode.IsSpace)
	}
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// RefreshMessage returns the message of the refresh merge commit.
func RefreshMessage(from, branch, changeSetID string) string {
	return "Merge " + from + " into " + branch + "\n\n" + Trailer(changeSetID)
}

var (
	// A closing keyword, with or without a colon, followed by an issue
	// reference: #12, GH-12, owner/repo#12, or an issue URL.
	closingKeyword = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\b\s*:?\s*(?:#[0-9]+|gh-[0-9]+|[a-z0-9_.-]+/[a-z0-9_.-]+#[0-9]+|https?://\S+/(?:issues|pull)/[0-9]+)`)
	// An @ mention of a user or a team: an @ after the start or any
	// character that is not a word character, as GitHub matches it. The @
	// of an e-mail address follows a word character.
	mention = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])@[A-Za-z0-9][A-Za-z0-9-]*(?:/[A-Za-z0-9_.-]+)?`)
	// A token that makes GitHub skip the workflow runs of a commit, so CI
	// would not gate it.
	skipCI = regexp.MustCompile(`(?i)\[(?:skip ci|ci skip|no ci|skip actions|actions skip)\]|(?m)^\s*skip-checks\s*:\s*true\s*$`)
	// A line that starts a Change-Set trailer.
	trailerLine = regexp.MustCompile(`(?im)^\s*change-set\s*:`)
)

// ActsOnGitHub reports text that GitHub acts on outside code: a closing
// keyword with an issue reference, an @ mention, or a token that skips
// the CI of a commit. It matches the folded text, so a Unicode space
// cannot hide a keyword that OneLine or GitHub turns back into one.
func ActsOnGitHub(text string) bool {
	text = fold(text)
	return closingKeyword.MatchString(text) || mention.MatchString(text) || skipCI.MatchString(text)
}

// fold prepares text for matching: every Unicode space but a line break
// becomes an ASCII space, every line ending becomes LF, and invisible
// format characters, such as a zero-width space, are dropped. Folding only
// makes a match more likely, never less.
func fold(text string) string {
	var b strings.Builder
	for _, r := range lineBreaks(text) {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteByte(' ')
		case unicode.Is(unicode.Cf, r):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// lineBreaks replaces every line ending that CommonMark knows, CR LF, CR,
// and LF, with LF.
func lineBreaks(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}

// HasTrailerLine reports a line that starts with Change-Set:.
func HasTrailerLine(text string) bool { return trailerLine.MatchString(fold(text)) }

// The longest pull-request title and body that GitHub accepts, in
// characters.
const (
	MaxTitleLength = 256
	MaxBodyLength  = 65536
)

func longestRun(value string, char byte) int {
	longest, current := 0, 0
	for i := 0; i < len(value); i++ {
		if value[i] == char {
			current++
			if current > longest {
				longest = current
			}
		} else {
			current = 0
		}
	}
	return longest
}

// Code renders a one-line value as a code span. The delimiter is a
// backtick run one longer than the longest run in the value, so the value
// cannot close its own span. A line ending in the value becomes a space,
// as CommonMark shows it inside a span anyway, so no value can end the
// line and start a block of its own.
func Code(value string) string {
	value = strings.ReplaceAll(lineBreaks(value), "\n", " ")
	if value == "" {
		return "` `"
	}
	fence := strings.Repeat("`", longestRun(value, '`')+1)
	pad := ""
	if strings.HasPrefix(value, "`") || strings.HasSuffix(value, "`") ||
		(strings.HasPrefix(value, " ") && strings.HasSuffix(value, " ")) {
		pad = " "
	}
	return fence + pad + value + pad + fence
}

// Fenced renders a value as a fenced block, each line prefixed with
// indent. The fence is a backtick run one longer than the longest run in
// the value, and at least three.
func Fenced(value, indent string) string {
	value = lineBreaks(value)
	size := longestRun(value, '`') + 1
	if size < 3 {
		size = 3
	}
	fence := strings.Repeat("`", size)
	var b strings.Builder
	b.WriteString(indent + fence + "\n")
	for _, line := range strings.Split(strings.TrimSuffix(value, "\n"), "\n") {
		if line == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(indent + line + "\n")
	}
	b.WriteString(indent + fence + "\n")
	return b.String()
}

func multiline(value string) bool { return strings.ContainsAny(value, "\n\r") }

// value writes a labelled record value: inline as a code span, or, when it
// spans lines, as a fenced block below the label.
func value(b *strings.Builder, prefix, label, v, indent string) {
	if multiline(v) {
		b.WriteString(prefix + label + ":\n\n")
		b.WriteString(Fenced(v, indent))
		b.WriteString("\n")
		return
	}
	b.WriteString(prefix + label + ": " + Code(v) + "\n")
}

// BodyInput is what the pull-request body is rendered from.
type BodyInput struct {
	ChangeSetID string
	Manifest    ears.Manifest
	Compare     *ears.Compare
	Impact      *ears.Impact
	Files       []string
}

// Header is the first line of every rendered body.
const Header = "<!-- Rendered by the ProtoBot Source Control Manager from ears-manager output. The next publish replaces this body; edits made on the host are lost. -->"

// Body renders the pull-request body in #34's order.
func Body(in BodyInput) string {
	var b strings.Builder
	b.WriteString(Header + "\n\n")

	// 1. The change-set ID, the intent, and the base commit.
	b.WriteString("## Change set " + Code(in.ChangeSetID) + "\n\n")
	value(&b, "- ", "Intent", in.Manifest.Intent, "  ")
	value(&b, "- ", "Base commit", in.Manifest.BaseCommit, "  ")
	b.WriteString("\n")

	var requirements, others []ears.Changed
	if in.Compare != nil {
		for _, op := range in.Compare.Changed {
			if op.RequirementID != "" {
				requirements = append(requirements, op)
			} else {
				others = append(others, op)
			}
		}
	}

	// 2. The changed set.
	b.WriteString("### Changed requirements\n\n")
	if len(requirements) == 0 {
		b.WriteString("No requirement operations.\n\n")
	}
	for _, op := range requirements {
		b.WriteString("- " + Code(op.Action) + " " + Code(op.RequirementID) + "\n")
		beforeAndAfter(&b, op)
	}
	if len(requirements) > 0 {
		b.WriteString("\n")
	}

	// 3. The interface and artifact operations, when there are any.
	if len(others) > 0 {
		b.WriteString("### Interface and artifact operations\n\n")
		for _, op := range others {
			switch {
			case op.InterfaceID != "":
				b.WriteString("- Interface " + Code(op.Action) + " " + Code(op.InterfaceID) + "\n")
			case op.ArtifactID != "":
				b.WriteString("- Artifact " + Code(op.Action) + " " + Code(op.ArtifactID) + "\n")
			default:
				b.WriteString("- Operation " + Code(op.Action) + "\n")
			}
			beforeAndAfter(&b, op)
		}
		b.WriteString("\n")
	}

	// 4. The impact assessment.
	b.WriteString("### Impact assessment\n\n")
	impactLines(&b, in)

	// 5. implementation_required, with the rationale when it is false.
	required := in.Manifest.ImplementationRequired
	if in.Compare != nil && in.Compare.ImplementationRequired != nil {
		required = *in.Compare.ImplementationRequired
	}
	b.WriteString("### Implementation\n\n")
	if required {
		b.WriteString("- Implementation required: " + Code("true") + "\n\n")
	} else {
		b.WriteString("- Implementation required: " + Code("false") + "\n")
		value(&b, "- ", "Rationale", in.Manifest.ImplementationRationale, "  ")
		b.WriteString("\n")
	}

	// 6. The files that the pull request changes.
	b.WriteString("### Files\n\n")
	if len(in.Files) == 0 {
		b.WriteString("No files.\n")
	}
	for _, file := range in.Files {
		b.WriteString("- " + Code(file) + "\n")
	}
	return b.String()
}

// beforeAndAfter writes the before and after values that compare gives for
// an operation, such as the text of a revised requirement.
func beforeAndAfter(b *strings.Builder, op ears.Changed) {
	if before, ok := text(op.Before); ok {
		value(b, "  - ", "Before", before, "    ")
	}
	if after, ok := text(op.After); ok {
		value(b, "  - ", "After", after, "    ")
	}
}

func impactLines(b *strings.Builder, in BodyInput) {
	type entry struct {
		id, origin, disposition, rationale string
		decided                            bool
	}
	var entries []entry
	listed := map[string]bool{}
	if in.Impact != nil {
		for _, c := range in.Impact.Candidates {
			e := entry{id: c.RequirementID, origin: c.Origin}
			if c.RecordedDisposition != nil && *c.RecordedDisposition != "" {
				e.decided = true
				e.disposition = *c.RecordedDisposition
			}
			if c.Rationale != nil {
				e.rationale = *c.Rationale
			}
			entries = append(entries, e)
			listed[c.RequirementID] = true
		}
	}
	for _, a := range in.Manifest.ImpactAssessment {
		if listed[a.RequirementID] {
			continue
		}
		entries = append(entries, entry{id: a.RequirementID, origin: a.Origin, disposition: a.Disposition, rationale: a.Rationale, decided: a.Disposition != ""})
		listed[a.RequirementID] = true
	}
	if len(entries) == 0 {
		b.WriteString("No impact candidates.\n\n")
		return
	}
	for _, e := range entries {
		origin := e.origin
		if origin == "" {
			origin = "mechanical"
		}
		if e.decided {
			b.WriteString("- " + Code(e.id) + " (" + Code(origin) + "): " + Code(e.disposition) + "\n")
			value(b, "  - ", "Rationale", e.rationale, "    ")
		} else {
			b.WriteString("- " + Code(e.id) + " (" + Code(origin) + "): no disposition yet\n")
		}
	}
	b.WriteString("\n")
}

// text returns the text of a before or after value: its "text" field when
// it is an object that has one, the string itself, or compact JSON.
func text(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var obj struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Text != nil {
		return *obj.Text, true
	}
	return string(raw), true
}
