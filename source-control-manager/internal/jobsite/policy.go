package jobsite

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Projection classes from the version-1 projection policy.
const (
	ClassShared          = "shared"
	ClassTest            = "test"
	ClassImplementation  = "implementation"
	ClassIntegrationOnly = "integration-only"
	ClassAttestationOnly = "attestation-only"
	ClassUnclassified    = "unclassified"
)

// Roles that receive a projection.
const (
	RoleWorkerA     = "worker-a"
	RoleWorkerB     = "worker-b"
	RoleIntegration = "integration"
)

const policyVersion = 1

var projectionClasses = map[string]bool{
	ClassShared:          true,
	ClassTest:            true,
	ClassImplementation:  true,
	ClassIntegrationOnly: true,
	ClassAttestationOnly: true,
}

// Policy is a validated version-1 projection policy. Directory entries keep
// a trailing slash; file entries do not. A file entry applies only to that
// file; only a trailing-slash directory entry applies to descendants.
type Policy struct {
	Version int
	Digest  string
	data    []byte
	classes map[string]string
}

// ParsePolicy parses and validates a version-1 projection policy. Missing,
// malformed, unsupported, duplicate, or unsafe policies fail closed.
func ParsePolicy(data []byte) (Policy, error) {
	if len(bytesWithoutComments(data)) == 0 {
		return Policy{}, fail(CodePolicyInvalid, "Projection policy must declare version: 1.")
	}
	document, err := safeDecodePolicyDocument(data)
	if err != nil {
		return Policy{}, fail(CodePolicyInvalid, "Projection policy is not a valid YAML document.")
	}
	var doc map[string]any
	if err := document.Decode(&doc); err != nil {
		return Policy{}, fail(CodePolicyInvalid, "Projection policy is not a valid YAML document.")
	}
	if doc == nil {
		return Policy{}, fail(CodePolicyInvalid, "Projection policy must be a YAML mapping.")
	}
	version, ok := asInt(doc["version"])
	if !ok {
		return Policy{}, fail(CodePolicyInvalid, "Projection policy must declare version: 1.")
	}
	if version != policyVersion {
		return Policy{}, fail(CodePolicyUnsupported, "Projection policy version is unsupported; this fixture supports version 1.")
	}
	rawPaths, exists := doc["paths"]
	if !exists {
		return Policy{Version: policyVersion, Digest: digestBytes(data), data: append([]byte(nil), data...), classes: map[string]string{}}, nil
	}
	list, ok := rawPaths.([]any)
	if !ok {
		return Policy{}, fail(CodePolicyInvalid, "Projection field \"paths\" must be a list of path and class entries.")
	}
	classes := map[string]string{}
	for i, item := range list {
		entry, ok := item.(map[string]any)
		if !ok || len(entry) != 2 {
			return Policy{}, fail(CodePolicyInvalid, fmt.Sprintf("Projection entry paths[%d] must contain exactly a string path and a string class.", i))
		}
		entryPath, okPath := entry["path"].(string)
		class, okClass := entry["class"].(string)
		if !okPath || !okClass || entryPath == "" || class == "" {
			return Policy{}, fail(CodePolicyInvalid, fmt.Sprintf("Projection entry paths[%d] must contain exactly a string path and a string class.", i))
		}
		if !projectionClasses[class] {
			return Policy{}, fail(CodePolicyInvalid, "Projection class must be shared, test, implementation, integration-only, or attestation-only.")
		}
		isDirectory := strings.HasSuffix(entryPath, "/")
		canonical, err := canonicalPath(strings.TrimSuffix(entryPath, "/"))
		if err != nil {
			return Policy{}, fail(CodePolicyInvalid, "Projection path must be a slash-separated project-relative path.")
		}
		key := canonical
		counterpart := canonical + "/"
		if isDirectory {
			key = canonical + "/"
			counterpart = canonical
		}
		if _, dup := classes[key]; dup {
			return Policy{}, fail(CodePolicyInvalid, "Projection path is classified more than once.")
		}
		if _, conflict := classes[counterpart]; conflict {
			return Policy{}, fail(CodePolicyInvalid, "Projection path is classified more than once.")
		}
		classes[key] = class
	}
	return Policy{
		Version: policyVersion,
		Digest:  digestBytes(data),
		data:    append([]byte(nil), data...),
		classes: classes,
	}, nil
}

// Classify returns the class of path. Unclassified paths are denied by
// default.
func (p Policy) Classify(projectPath string) string {
	class, ok := p.lookup(projectPath)
	if !ok {
		return ClassUnclassified
	}
	return class
}

func (p Policy) lookup(projectPath string) (string, bool) {
	projectPath = strings.TrimSuffix(projectPath, "/")
	if class, ok := p.classes[projectPath]; ok {
		return class, true
	}
	if class, ok := p.classes[projectPath+"/"]; ok {
		return class, true
	}
	for current := path.Dir(projectPath); current != "." && current != "/" && current != ""; current = path.Dir(current) {
		if class, ok := p.classes[current+"/"]; ok {
			return class, true
		}
	}
	return "", false
}

// VisibleTo reports whether role may receive path in a Worker snapshot.
// Integration retains unclassified paths in the private baseline; Workers
// do not.
func (p Policy) VisibleTo(role, projectPath string) bool {
	class := p.Classify(projectPath)
	switch role {
	case RoleWorkerA:
		return class == ClassShared || class == ClassTest
	case RoleWorkerB:
		return class == ClassShared || class == ClassImplementation
	case RoleIntegration:
		return true
	default:
		return false
	}
}

// WritableBy reports whether role may patch path. Shared paths are
// read-only to Workers. Unclassified paths cannot be patched by Workers.
func (p Policy) WritableBy(role, projectPath string) bool {
	class := p.Classify(projectPath)
	switch role {
	case RoleWorkerA:
		return class == ClassTest
	case RoleWorkerB:
		return class == ClassImplementation
	case RoleIntegration:
		return class != ClassAttestationOnly
	default:
		return false
	}
}

func hasGitComponent(projectPath string) bool {
	for _, part := range strings.Split(projectPath, "/") {
		if strings.EqualFold(part, ".git") {
			return true
		}
	}
	return false
}

func canonicalPath(projectPath string) (string, error) {
	if projectPath == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	if strings.ContainsAny(projectPath, "\\\x00") || strings.HasPrefix(projectPath, "/") || strings.Contains(projectPath, ":") {
		return "", fmt.Errorf("path must use slash-separated project-relative form")
	}
	parts := strings.Split(projectPath, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("path contains an invalid component")
		}
	}
	return strings.Join(parts, "/"), nil
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func bytesWithoutComments(data []byte) []byte {
	var out []byte
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			out = append(out, trimmed...)
		}
	}
	return out
}

func asInt(value any) (int, bool) {
	switch n := value.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case uint64:
		return int(n), true
	default:
		return 0, false
	}
}

func safeDecodePolicyDocument(data []byte) (*yaml.Node, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("YAML input is not valid UTF-8")
	}
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		return nil, fmt.Errorf("YAML input must not contain a UTF-8 BOM")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil, fmt.Errorf("YAML document is empty")
	}
	if err := inspectPolicyNode(document.Content[0], "$"); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("YAML input contains more than one document")
		}
		return nil, fmt.Errorf("read YAML document boundary: %w", err)
	}
	return &document, nil
}

func inspectPolicyNode(node *yaml.Node, location string) error {
	if node.Kind == yaml.AliasNode {
		return fmt.Errorf("unsafe YAML alias at %s", location)
	}
	if node.Tag == "!!null" {
		return fmt.Errorf("YAML null values are not allowed at %s", location)
	}
	if node.Style&yaml.TaggedStyle != 0 {
		return fmt.Errorf("unsafe YAML tag at %s", location)
	}
	if strings.HasPrefix(node.Tag, "!") && !strings.HasPrefix(node.Tag, "!!") {
		return fmt.Errorf("unsafe YAML tag %q at %s", node.Tag, location)
	}

	switch node.Kind {
	case yaml.MappingNode:
		keys := make(map[string]struct{}, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return fmt.Errorf("YAML mapping key at %s must be a string", location)
			}
			if key.Value == "<<" {
				return fmt.Errorf("YAML merge key is not allowed at %s", location)
			}
			if _, exists := keys[key.Value]; exists {
				return fmt.Errorf("duplicate YAML key %q at %s", key.Value, location)
			}
			keys[key.Value] = struct{}{}
			if err := inspectPolicyNode(node.Content[i+1], location+"."+key.Value); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, child := range node.Content {
			if err := inspectPolicyNode(child, fmt.Sprintf("%s[%d]", location, i)); err != nil {
				return err
			}
		}
	}
	return nil
}
