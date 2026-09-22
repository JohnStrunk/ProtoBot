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

	entryPath := filepath.FromSlash(canonical)
	info, err := rootHandle.Lstat(entryPath)
	if os.IsNotExist(err) {
		return emptyStoreDigest(), nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("record store path must be a regular directory")
	}
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

	hash := sha256.New()
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		fragment, err := canonicalStoreEntry(rootHandle, canonical, entry)
		if err != nil {
			return "", err
		}
		_, _ = hash.Write([]byte(fragment))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func canonicalStoreEntry(rootHandle *os.Root, canonical string, entry os.DirEntry) (string, error) {
	relativePath := filepath.ToSlash(filepath.Join(canonical, entry.Name()))
	info, err := rootHandle.Lstat(filepath.FromSlash(relativePath))
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("record store contains a non-regular entry")
	}
	if filepath.Ext(entry.Name()) != ".yaml" {
		return "", fmt.Errorf("record store contains an unexpected file")
	}
	data, err := rootHandle.ReadFile(filepath.FromSlash(relativePath))
	if err != nil {
		return "", err
	}
	fileDigest, err := CanonicalTextDigest(data)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s\x00%s\x00", relativePath, fileDigest), nil
}

func emptyStoreDigest() string {
	digest := sha256.Sum256(nil)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func emptyStoreDigests(value records.StoreDigests) bool {
	return value.Requirements == "" && value.Interfaces == "" && value.ChangeSets == ""
}
