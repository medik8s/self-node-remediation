package version

import (
	"os"
	"regexp"
	"testing"
)

func TestCommittedVersionMatchesMakefile(t *testing.T) {
	makefile, err := os.ReadFile("../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`(?m)^DEFAULT_VERSION := (\S+)$`).FindSubmatch(makefile)
	if len(match) != 2 {
		t.Fatal("Makefile must define DEFAULT_VERSION")
	}
	if Version != string(match[1]) {
		t.Fatalf("binary version %q differs from Makefile version %q", Version, match[1])
	}
}
