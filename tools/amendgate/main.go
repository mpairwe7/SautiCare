// Command amendgate enforces Build Prompt rule 3.11 in CI: while the study
// window is active, a change to any gated path (triage rules, Tool 2 list,
// dispatch parameters, SMS templates, decline reasons, section 11 flags)
// fails the build unless governance/amendments.yaml records an approved
// amendment for exactly that content.
//
// Usage: go run ./tools/amendgate -base origin/main
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"go.yaml.in/yaml/v4"
)

func main() {
	base := flag.String("base", "origin/main", "git ref to diff against")
	manifest := flag.String("manifest", "governance/amendments.yaml", "amendment records")
	gatedList := flag.String("gated", "governance/gated-paths.txt", "gated path globs")
	study := flag.String("study", "config/study.yaml", "study window file")
	flag.Parse()

	if err := run(*base, *manifest, *gatedList, *study); err != nil {
		fmt.Fprintln(os.Stderr, "amendgate:", err)
		os.Exit(1)
	}
}

func run(base, manifest, gatedList, study string) error {
	globs, err := readGlobs(gatedList)
	if err != nil {
		return err
	}
	var am struct {
		Amendments []Amendment `yaml:"amendments"`
	}
	if b, err := os.ReadFile(manifest); err != nil {
		return err
	} else if err := yaml.Unmarshal(b, &am); err != nil {
		return fmt.Errorf("%s: %w", manifest, err)
	}

	headActive, err := windowActive(readFileBytes(study))
	if err != nil {
		return err
	}
	baseBytes, _ := gitOutput("show", base+":"+study) // absent at base is fine
	baseActive, err := windowActive(baseBytes, nil)
	if err != nil {
		return err
	}
	active := headActive || baseActive

	changed, err := changedFiles(base)
	if err != nil {
		return err
	}
	v := Check(active, globs, changed, am.Amendments)
	fmt.Printf("amendgate: window active=%v, %d changed files, %d violations\n", active, len(changed), len(v))
	for _, x := range v {
		fmt.Printf("  VIOLATION %s: %s\n", x.Path, x.Reason)
	}
	if len(v) > 0 {
		return fmt.Errorf("%d gated changes lack an amendment record", len(v))
	}
	return nil
}

func readFileBytes(p string) ([]byte, error) { return os.ReadFile(p) }

func windowActive(b []byte, err error) (bool, error) {
	if err != nil || len(b) == 0 {
		return false, err
	}
	var s struct {
		StudyWindowActive bool `yaml:"study_window_active"`
	}
	if err := yaml.Unmarshal(b, &s); err != nil {
		return false, err
	}
	return s.StudyWindowActive, nil
}

func readGlobs(p string) ([]string, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out, sc.Err()
}

func gitOutput(args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.Bytes(), err
}

func changedFiles(base string) ([]ChangedFile, error) {
	out, err := gitOutput("diff", "--name-status", "--no-renames", base+"...HEAD")
	if err != nil {
		return nil, fmt.Errorf("git diff against %s: %w", base, err)
	}
	var files []ChangedFile
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		status, p, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		if strings.HasPrefix(status, "D") {
			files = append(files, ChangedFile{Path: p, Deleted: true})
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		files = append(files, ChangedFile{Path: p, SHA256: hex.EncodeToString(sum[:])})
	}
	return files, nil
}
