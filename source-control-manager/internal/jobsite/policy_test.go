package jobsite

import "testing"

func TestParsePolicyFileEntryDoesNotCoverDescendants(t *testing.T) {
	policy, err := ParsePolicy([]byte("version: 1\npaths:\n  - path: src\n    class: implementation\n  - path: src/lib/file.go\n    class: shared\n"))
	if err != nil {
		t.Fatal(err)
	}
	if policy.Classify("src") != ClassImplementation {
		t.Fatalf("file entry src = %s", policy.Classify("src"))
	}
	if policy.Classify("src/main.go") != ClassUnclassified {
		t.Fatalf("file entry covered a descendant: %s", policy.Classify("src/main.go"))
	}
	if policy.Classify("src/lib/file.go") != ClassShared {
		t.Fatalf("nested file override = %s", policy.Classify("src/lib/file.go"))
	}
}

func TestParsePolicyDirectoryEntryCoversDescendants(t *testing.T) {
	policy, err := ParsePolicy([]byte("version: 1\npaths:\n  - path: src/\n    class: implementation\n  - path: src/generated/README.md\n    class: shared\n"))
	if err != nil {
		t.Fatal(err)
	}
	if policy.Classify("src/app.go") != ClassImplementation {
		t.Fatalf("directory entry missed descendant: %s", policy.Classify("src/app.go"))
	}
	if policy.Classify("src/generated/README.md") != ClassShared {
		t.Fatalf("file override lost: %s", policy.Classify("src/generated/README.md"))
	}
}

func TestParsePolicyRejectsFileAndDirectoryPair(t *testing.T) {
	_, err := ParsePolicy([]byte("version: 1\npaths:\n  - path: docs/\n    class: shared\n  - path: docs\n    class: test\n"))
	if errorCode(err) != CodePolicyInvalid {
		t.Fatalf("err = %v", err)
	}
}

func TestParsePolicyFailClosed(t *testing.T) {
	cases := map[string]string{
		"missing version":     "paths: []\n",
		"unsupported version": "version: 2\npaths: []\n",
		"unsafe path":         "version: 1\npaths:\n  - path: ../x\n    class: shared\n",
		"absolute path":       "version: 1\npaths:\n  - path: /etc/passwd\n    class: shared\n",
		"unknown class":       "version: 1\npaths:\n  - path: src/\n    class: public\n",
		"not mapping":         "- src/\n",
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParsePolicy([]byte(data))
			if err == nil {
				t.Fatal("accepted invalid policy")
			}
			code := errorCode(err)
			if code != CodePolicyInvalid && code != CodePolicyUnsupported {
				t.Fatalf("code = %s err = %v", code, err)
			}
		})
	}
}

func TestWorkerAccessMatrix(t *testing.T) {
	policy, err := ParsePolicy(fixturePolicy)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path      string
		visibleA  bool
		visibleB  bool
		writableA bool
		writableB bool
	}{
		{path: sharedDocPath, visibleA: true, visibleB: true},
		{path: currentTestPath, visibleA: true, writableA: true},
		{path: currentImplPath, visibleB: true, writableB: true},
		{path: nestedSharedPath, visibleA: true, visibleB: true},
		{path: unclassifiedPath},
		{path: integrationOnlyPath},
		{path: attestationPath},
		{path: fileOnlySharedPath, visibleA: true, visibleB: true},
	}
	for _, tc := range cases {
		if got := policy.VisibleTo(RoleWorkerA, tc.path); got != tc.visibleA {
			t.Errorf("visible A %s = %v, want %v", tc.path, got, tc.visibleA)
		}
		if got := policy.VisibleTo(RoleWorkerB, tc.path); got != tc.visibleB {
			t.Errorf("visible B %s = %v, want %v", tc.path, got, tc.visibleB)
		}
		if got := policy.WritableBy(RoleWorkerA, tc.path); got != tc.writableA {
			t.Errorf("writable A %s = %v, want %v", tc.path, got, tc.writableA)
		}
		if got := policy.WritableBy(RoleWorkerB, tc.path); got != tc.writableB {
			t.Errorf("writable B %s = %v, want %v", tc.path, got, tc.writableB)
		}
	}
}
