package specvalidation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/redhat-et/protobot/ears-manager/internal/records"
	"github.com/redhat-et/protobot/ears-manager/internal/storage"
)

func validateStoreIntegrity(result *Result, snapshot Snapshot, projectPath string, stores records.StorePaths, expected records.StoreDigests) {
	if snapshot.ConfigFields == nil && emptyStoreDigests(expected) {
		return
	}
	for _, store := range []struct {
		name   string
		path   string
		digest string
		field  string
	}{
		{name: "requirements", path: stores.Requirements, digest: expected.Requirements, field: "store_digests.requirements"},
		{name: "interfaces", path: stores.Interfaces, digest: expected.Interfaces, field: "store_digests.interfaces"},
		{name: "change_sets", path: stores.ChangeSets, digest: expected.ChangeSets, field: "store_digests.change_sets"},
	} {
		if strings.TrimSpace(store.digest) == "" {
			result.add(diagnostic("project.missing_field", projectPath, "", store.field, fmt.Sprintf("Required store integrity digest for %s is missing.", store.name), "Record the canonical store digest through ears-manager."))
			continue
		}
		if !digestPattern.MatchString(store.digest) {
			result.add(diagnostic("project.invalid_digest", projectPath, "", store.field, "Store integrity digest must match sha256:<64 lowercase hexadecimal characters>.", "Compute the digest through ears-manager."))
			continue
		}
		if snapshot.Root == "" {
			continue
		}
		actual, err := canonicalStoreDigest(snapshot.Root, store.path)
		if err != nil {
			result.add(diagnostic("project.store_digest_unreadable", projectPath, "", store.field, "Inspect the configured record store before comparing its integrity digest.", "Make the configured store readable and free of symlinked or non-record entries."))
			continue
		}
		if actual != store.digest {
			result.add(diagnostic("project.store_digest_mismatch", projectPath, "", store.field, "Store integrity digest does not match its canonical record file set.", "Rewrite the store through ears-manager or update it in a reviewed change set."))
		}
	}
}

func canonicalStoreDigest(root, relativeDirectory string) (string, error) {
	return CanonicalStoreDigestWithOverrides(root, relativeDirectory, nil)
}

// CanonicalStoreDigestWithOverrides computes a store digest while replacing
// files that a governed transaction is about to write. The overrides let a
// caller persist the updated store digest in the same transaction as its
// record writes.
func CanonicalStoreDigestWithOverrides(root, relativeDirectory string, overrides map[string][]byte) (string, error) {
	canonical, err := canonicalStorePath(relativeDirectory)
	if err != nil {
		return "", err
	}
	if _, err := storage.ValidatePathWithin(root, filepath.FromSlash(canonical)); err != nil {
		return "", err
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer func() { _ = rootHandle.Close() }()

	files := make(map[string][]byte)
	entryPath := filepath.FromSlash(canonical)
	info, err := rootHandle.Lstat(entryPath)
	if os.IsNotExist(err) {
		info = nil
	} else if err != nil {
		return "", err
	}
	if info != nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return "", fmt.Errorf("record store path must be a regular directory")
	}
	if info != nil {
		directory, err := rootHandle.Open(entryPath)
		if err != nil {
			return "", err
		}
		defer func() { _ = directory.Close() }()
		entries, err := directory.ReadDir(-1)
		if err != nil {
			return "", err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			relativePath := filepath.ToSlash(filepath.Join(canonical, entry.Name()))
			entryInfo, err := rootHandle.Lstat(filepath.FromSlash(relativePath))
			if err != nil {
				return "", err
			}
			if entryInfo.Mode()&os.ModeSymlink != 0 || !entryInfo.Mode().IsRegular() {
				return "", fmt.Errorf("record store contains a non-regular entry")
			}
			if filepath.Ext(entry.Name()) != ".yaml" {
				return "", fmt.Errorf("record store contains an unexpected file")
			}
			data, exists := overrides[relativePath]
			if !exists {
				data, err = rootHandle.ReadFile(filepath.FromSlash(relativePath))
				if err != nil {
					return "", err
				}
			}
			files[relativePath] = data
		}
	}

	for path, data := range overrides {
		if !isImmediateStoreRecord(canonical, path) {
			continue
		}
		if _, exists := files[path]; exists {
			continue
		}
		files[path] = data
	}

	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		fileDigest, err := CanonicalTextDigest(files[path])
		if err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00", path, fileDigest)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func isImmediateStoreRecord(storePath, path string) bool {
	prefix := storePath + "/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	name := strings.TrimPrefix(path, prefix)
	return name != "" && !strings.Contains(name, "/") && !strings.HasPrefix(name, ".") && filepath.Ext(name) == ".yaml"
}

func emptyStoreDigests(value records.StoreDigests) bool {
	return value.Requirements == "" && value.Interfaces == "" && value.ChangeSets == ""
}
