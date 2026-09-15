package storage

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/redhat-et/protobot/internal/records"
	"gopkg.in/yaml.v3"
)

const managedHeader = "# managed by ears-manager; do not edit by hand\n"

func Decode(data []byte, target any) error {
	if target == nil {
		return fmt.Errorf("YAML decode target must not be nil")
	}
	if !utf8.Valid(data) {
		return fmt.Errorf("YAML input is not valid UTF-8")
	}
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		return fmt.Errorf("YAML input must not contain a UTF-8 BOM")
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("parse YAML: %w", err)
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return fmt.Errorf("YAML document is empty")
	}
	if err := inspectNode(document.Content[0], "$"); err != nil {
		return err
	}

	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("YAML input contains more than one document")
		}
		return fmt.Errorf("read YAML document boundary: %w", err)
	}

	strictDecoder := yaml.NewDecoder(bytes.NewReader(data))
	strictDecoder.KnownFields(true)
	if err := strictDecoder.Decode(target); err != nil {
		return fmt.Errorf("decode YAML record: %w", err)
	}
	return nil
}

func Encode(value any) ([]byte, error) {
	value = canonicalValue(value)
	raw, err := yaml.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode YAML record: %w", err)
	}
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	raw = bytes.ReplaceAll(raw, []byte("\r"), []byte("\n"))
	raw = bytes.TrimRight(raw, "\n")
	if len(raw) == 0 {
		return nil, fmt.Errorf("cannot encode an empty YAML record")
	}
	return append([]byte(managedHeader), append(raw, '\n')...), nil
}

func ReadFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := Decode(data, target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func WriteFile(path string, value any) error {
	data, err := Encode(value)
	if err != nil {
		return err
	}
	if err := atomicWrite(path, data); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func inspectNode(node *yaml.Node, location string) error {
	if node.Kind == yaml.AliasNode {
		return fmt.Errorf("unsafe YAML alias at %s", location)
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
			if err := inspectNode(node.Content[i+1], location+"."+key.Value); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, child := range node.Content {
			if err := inspectNode(child, fmt.Sprintf("%s[%d]", location, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func canonicalValue(value any) any {
	switch typed := value.(type) {
	case records.Requirement:
		return records.CanonicalRequirement(typed)
	case *records.Requirement:
		copy := records.CanonicalRequirement(*typed)
		return &copy
	case records.InterfaceRecord:
		return records.CanonicalInterface(typed)
	case *records.InterfaceRecord:
		copy := records.CanonicalInterface(*typed)
		return &copy
	case records.ChangeSet:
		return records.CanonicalChangeSet(typed)
	case *records.ChangeSet:
		copy := records.CanonicalChangeSet(*typed)
		return &copy
	case records.ProjectConfig:
		return records.CanonicalProjectConfig(typed)
	case *records.ProjectConfig:
		copy := records.CanonicalProjectConfig(*typed)
		return &copy
	default:
		return value
	}
}

func atomicWrite(path string, data []byte) error {
	directory := filepath.Dir(path)
	for {
		if _, err := os.Stat(directory); err == nil {
			break
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return fmt.Errorf("cannot locate parent directory for %s", path)
		}
		directory = parent
	}
	temporary, err := os.CreateTemp(directory, ".ears-manager-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}
