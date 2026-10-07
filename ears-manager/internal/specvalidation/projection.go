package specvalidation

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/redhat-et/protobot/ears-manager/internal/records"
	"github.com/redhat-et/protobot/ears-manager/internal/storage"
	"gopkg.in/yaml.v3"
)

// ProjectionPath is the project-relative path of the projection manifest.
const ProjectionPath = ".protobot/projection.yaml"

// ProjectionClassShared is the projection class every registered
// specification path must carry.
const ProjectionClassShared = "shared"

var projectionClasses = map[string]bool{
	"attestation-only":    true,
	"implementation":      true,
	"integration-only":    true,
	ProjectionClassShared: true,
	"test":                true,
}

// Projection is the parsed projection manifest. ears-manager owns only the
// shared classification of registered specification paths; every other entry
// and top-level field is reviewed project policy that it preserves.
type Projection struct {
	// Present reports whether .protobot/projection.yaml exists.
	Present bool
	// Version is the projection manifest format version.
	Version int
	// Data holds the exact manifest bytes that were read or staged.
	Data []byte
	// Classes maps a canonical project path to its projection class. Directory
	// entries keep a trailing slash; file entries do not. A file entry applies
	// only to that file; only a trailing-slash directory entry applies to
	// descendants. Listing both a path and the same path with a trailing slash
	// is invalid.
	Classes map[string]string
	// Diagnostics records structural problems found while parsing.
	Diagnostics []Diagnostic
}

// Clone returns a deep copy of the projection.
func (p Projection) Clone() Projection {
	clone := p
	clone.Data = append([]byte(nil), p.Data...)
	clone.Classes = maps.Clone(p.Classes)
	if clone.Classes == nil {
		clone.Classes = map[string]string{}
	}
	clone.Diagnostics = append([]Diagnostic(nil), p.Diagnostics...)
	return clone
}

// ParseProjection parses projection manifest bytes. The accepted format is a
// mapping whose integer `version` key is 1 and whose optional `paths` list
// holds entries with exactly the `path` and `class` keys. A trailing slash on
// a path names a directory. A file entry applies only to that file; only a
// trailing-slash directory entry applies to descendants. Other top-level keys
// are preserved as project policy.
func ParseProjection(data []byte) Projection {
	projection := Projection{Present: true, Data: append([]byte(nil), data...), Classes: map[string]string{}}
	if isBlankYAML(data) {
		projection.addDiagnostic("version", "Projection manifest must declare version: 1.")
		return projection
	}
	root, err := storage.DecodeNode(data)
	if err != nil {
		projection.addDiagnostic("", "Projection manifest is not a valid safe YAML document.")
		return projection
	}
	if root.Kind != yaml.MappingNode {
		projection.addDiagnostic("", "Projection manifest must be a YAML mapping.")
		return projection
	}
	version := mappingValue(root, "version")
	if version == nil || version.Kind != yaml.ScalarNode || version.Tag != "!!int" {
		projection.addDiagnostic("version", "Projection manifest must declare version: 1.")
		return projection
	}
	var parsedVersion int
	if err := version.Decode(&parsedVersion); err != nil || parsedVersion != 1 {
		projection.addDiagnostic("version", "Projection manifest version is unsupported; this tool supports version 1.")
		return projection
	}
	projection.Version = parsedVersion
	paths := mappingValue(root, "paths")
	if paths == nil {
		return projection
	}
	if paths.Kind != yaml.SequenceNode {
		projection.addDiagnostic("paths", "Projection field \"paths\" must be a list of path and class entries.")
		return projection
	}
	for index, item := range paths.Content {
		field := fmt.Sprintf("paths[%d]", index)
		entryPath, class, ok := projectionEntry(item)
		if !ok {
			projection.addDiagnostic(field, "Projection entries must contain exactly a string path and a string class.")
			continue
		}
		isDirectory := strings.HasSuffix(entryPath, "/")
		canonical, err := canonicalProjectPath(strings.TrimSuffix(entryPath, "/"))
		if err != nil {
			projection.addDiagnostic(field+".path", "Projection path must be a slash-separated project-relative path.")
			continue
		}
		if !projectionClasses[class] {
			projection.addDiagnostic(field+".class", "Projection class must be shared, test, implementation, integration-only, or attestation-only.")
			continue
		}
		key := canonical
		counterpart := canonical + "/"
		if isDirectory {
			key = canonical + "/"
			counterpart = canonical
		}
		if _, duplicate := projection.Classes[key]; duplicate {
			projection.addDiagnostic(field+".path", "Projection path is classified more than once.")
			continue
		}
		if _, conflict := projection.Classes[counterpart]; conflict {
			projection.addDiagnostic(field+".path", "Projection path is classified more than once.")
			continue
		}
		projection.Classes[key] = class
	}
	return projection
}

func (p *Projection) addDiagnostic(field, message string) {
	p.Diagnostics = append(p.Diagnostics, diagnostic("projection.invalid", ProjectionPath, "", field, message, "Correct the projection manifest in a reviewed change."))
}

func projectionEntry(node *yaml.Node) (string, string, bool) {
	if node.Kind != yaml.MappingNode || len(node.Content) != 4 {
		return "", "", false
	}
	pathNode := mappingValue(node, "path")
	classNode := mappingValue(node, "class")
	if pathNode == nil || classNode == nil || !stringScalar(pathNode) || !stringScalar(classNode) {
		return "", "", false
	}
	return pathNode.Value, classNode.Value, true
}

func stringScalar(node *yaml.Node) bool {
	return node.Kind == yaml.ScalarNode && node.Tag == "!!str"
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
}

func isBlankYAML(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			return false
		}
	}
	return true
}

// ErrProjectionInvalid reports a projection manifest that ears-manager cannot
// safely update because it is not in the accepted format.
var ErrProjectionInvalid = errors.New("projection manifest is invalid")

// AddSharedClassifications returns manifest bytes that classify every listed
// path as shared. Existing entries, including a different class for a listed
// path, and every other top-level field are preserved; semantic validation
// reports a registered path whose class is not shared. A path that ends in a
// slash is written as a directory entry. A path already covered as shared by
// an entry for one of its ancestor directories gets no entry of its own. A nil
// data argument creates a new manifest that declares version 1; the boolean
// result reports whether the manifest changed. The edit keeps comments but,
// because yaml.v3 does not record them, not blank lines.
func AddSharedClassifications(data []byte, paths []string) ([]byte, bool, error) {
	newManifest := data == nil
	current := Projection{Classes: map[string]string{}}
	if !newManifest {
		current = ParseProjection(data)
		if len(current.Diagnostics) > 0 {
			return nil, false, ErrProjectionInvalid
		}
	}
	missing := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		isDirectory := strings.HasSuffix(path, "/")
		canonical, err := canonicalProjectPath(strings.TrimSuffix(path, "/"))
		if err != nil {
			return nil, false, fmt.Errorf("projection path: %w", err)
		}
		key := canonical
		if isDirectory {
			key += "/"
		}
		if _, exists := current.Classes[key]; exists || seen[canonical] {
			continue
		}
		if class, covered := projectionClass(current.Classes, canonical); covered && class == ProjectionClassShared {
			continue
		}
		seen[canonical] = true
		written := canonical
		if isDirectory {
			written += "/"
		}
		missing = append(missing, written)
	}
	if len(missing) == 0 && !newManifest {
		return append([]byte(nil), data...), false, nil
	}
	sort.Strings(missing)

	var document *yaml.Node
	if newManifest {
		root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "version"},
			{Kind: yaml.ScalarNode, Tag: "!!int", Value: "1"},
		}}
		document = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	} else {
		parsed, err := storage.DecodeDocumentNode(data)
		if err != nil {
			return nil, false, ErrProjectionInvalid
		}
		document = parsed
	}
	root := document.Content[0]
	sequence := mappingValue(root, "paths")
	if sequence == nil {
		sequence = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "paths"},
			sequence,
		)
	}
	if len(sequence.Content) == 0 {
		sequence.Style = 0
	}
	for _, path := range missing {
		sequence.Content = append(sequence.Content, &yaml.Node{
			Kind: yaml.MappingNode,
			Tag:  "!!map",
			Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: "path"},
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: path},
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: "class"},
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: ProjectionClassShared},
			},
		})
	}
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, false, fmt.Errorf("encode projection manifest: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, false, fmt.Errorf("encode projection manifest: %w", err)
	}
	encoded := buffer.Bytes()
	if updated := ParseProjection(encoded); len(updated.Diagnostics) > 0 {
		return nil, false, ErrProjectionInvalid
	}
	return encoded, true, nil
}

func loadProjection(rootHandle *os.Root) Projection {
	relative := filepath.FromSlash(ProjectionPath)
	info, err := rootHandle.Lstat(relative)
	if errors.Is(err, fs.ErrNotExist) {
		return Projection{Classes: map[string]string{}}
	}
	projection := Projection{Present: true, Classes: map[string]string{}}
	if err != nil {
		projection.addDiagnostic("", "Projection manifest could not be inspected.")
		return projection
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		projection.addDiagnostic("", "Projection manifest must be a regular file, not a symlink or directory.")
		return projection
	}
	data, err := rootHandle.ReadFile(relative)
	if err != nil {
		projection.addDiagnostic("", "Projection manifest could not be read.")
		return projection
	}
	return ParseProjection(data)
}

// projectionClass resolves the class of a canonical path: its own file
// entry first, then a directory entry for the path itself, then the entry of
// its nearest ancestor directory. A file entry applies only to that file; only
// a trailing-slash directory entry applies to descendants. The most specific
// entry wins, so a file entry overrides the entry of a directory that holds
// it.
func projectionClass(classes map[string]string, path string) (string, bool) {
	path = strings.TrimSuffix(path, "/")
	if class, ok := classes[path]; ok {
		return class, true
	}
	if class, ok := classes[path+"/"]; ok {
		return class, true
	}
	for current := filepath.ToSlash(filepath.Dir(path)); current != "." && current != "/" && current != ""; current = filepath.ToSlash(filepath.Dir(current)) {
		if class, ok := classes[current+"/"]; ok {
			return class, true
		}
	}
	return "", false
}

// validateProjection checks that every registered specification path and
// every configured store directory is classified shared. It runs only when the snapshot carries a projection,
// which every snapshot loaded from a project root does.
func validateProjection(result *Result, snapshot Snapshot, artifacts []records.ArtifactEntry, stores records.StorePaths) {
	if snapshot.Projection == nil {
		return
	}
	for _, item := range snapshot.Projection.Diagnostics {
		result.add(item)
	}
	for _, artifact := range artifacts {
		canonical, err := canonicalProjectPath(artifact.Path)
		if err != nil || isReservedProjectPath(canonical) {
			continue
		}
		class, classified := projectionClass(snapshot.Projection.Classes, canonical)
		if classified && class == ProjectionClassShared {
			continue
		}
		message := fmt.Sprintf("Registered path %q is not classified; it requires the class shared.", canonical)
		hint := projectionRestoreHint
		if classified {
			message = fmt.Sprintf("Registered path %q is classified %s; it requires the class shared.", canonical, class)
			hint = projectionReclassifyHint
		}
		result.add(diagnostic("projection.unclassified", canonical, artifact.ID, "class", message, hint))
	}
	storePaths := stores.WithDefaults()
	for _, store := range []string{storePaths.Requirements, storePaths.Interfaces, storePaths.ChangeSets} {
		canonical, err := canonicalStorePath(store)
		if err != nil {
			// validateStorePaths reports an invalid store path.
			continue
		}
		class, classified := projectionClass(snapshot.Projection.Classes, canonical)
		if classified && class == ProjectionClassShared {
			continue
		}
		directory := canonical + "/"
		message := fmt.Sprintf("Configured store directory %q is not classified; it requires the class shared.", directory)
		hint := projectionRestoreHint
		if classified {
			message = fmt.Sprintf("Configured store directory %q is classified %s; it requires the class shared.", directory, class)
			hint = projectionReclassifyHint
		}
		result.add(diagnostic("projection.unclassified", directory, "", "class", message, hint))
	}
}

// The repair of a missing or non-shared entry is a reviewed policy edit of
// .protobot/projection.yaml. ears-manager adds an entry only for a path it
// registers, and only while a proposed change set is open.
const (
	projectionRestoreHint    = "Restore a shared entry for this path in .protobot/projection.yaml through a reviewed policy edit."
	projectionReclassifyHint = "Change this path's entry in .protobot/projection.yaml to the class shared through a reviewed policy edit."
)
