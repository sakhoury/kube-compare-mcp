// SPDX-License-Identifier: Apache-2.0

package rdsdiff

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RunPolicyGen copies source-crs into CRSPath under configRoot, runs the PolicyGenerator binary
// for each SNO policy file, and writes generated YAMLs to generatedDir.
// configRoot is the effective configuration root (e.g. .../telco-ran/configuration).
// policyGeneratorPath is the path to the PolicyGenerator binary (e.g. from ACM or ztp-site-generate image).
// PolicyGenerator accepts config file path(s) as positional args and writes generated policies to stdout.
func RunPolicyGen(configRoot, policyGeneratorPath, generatedDir string) error {
	configRoot = filepath.Clean(configRoot)
	generatedDir = filepath.Clean(generatedDir)
	sourceCRS := filepath.Join(configRoot, SourceCRSPath)
	crsPathDir := filepath.Join(configRoot, CRSPath)
	if err := os.MkdirAll(generatedDir, 0750); err != nil {
		return fmt.Errorf("mkdir generated dir: %w", err)
	}
	// Copy source-crs into CRSPath so PolicyGenerator finds it
	destSourceCRS := filepath.Join(crsPathDir, "source-crs")
	if err := os.RemoveAll(destSourceCRS); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove dest source-crs: %w", err)
	}
	if err := copyDir(sourceCRS, destSourceCRS); err != nil {
		return fmt.Errorf("copy source-crs to %s: %w", destSourceCRS, err)
	}
	// Run PolicyGenerator for each policy file; CWD = crsPathDir so relative paths resolve.
	// PolicyGenerator [flags] <policy-generator-config-file>... writes generated YAML to stdout.
	for _, policyFile := range ListOfCRsForSNO {
		policyPath := filepath.Join(crsPathDir, policyFile)
		if _, err := os.Stat(policyPath); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("stat %s: %w", policyPath, err)
		}
		cmd := exec.Command(policyGeneratorPath, policyPath) // #nosec G204 -- paths from config root and our binary path
		cmd.Dir = crsPathDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("policyGenerator %s: %w\n%s", policyFile, err, string(out))
		}
		// Write stdout to a file in generatedDir (name stem + -generated.yaml to avoid overwriting config)
		base := strings.TrimSuffix(policyFile, filepath.Ext(policyFile))
		outPath := filepath.Join(generatedDir, base+"-generated.yaml")
		if err := os.WriteFile(outPath, out, 0600); err != nil {
			return fmt.Errorf("write PolicyGenerator output %s: %w", outPath, err)
		}
	}
	return nil
}

func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("read dir %s: %w", src, err)
	}
	if err := os.MkdirAll(dst, 0750); err != nil {
		return fmt.Errorf("mkdir %s: %w", dst, err)
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(srcPath) // #nosec G304 -- srcPath from Join(src, e.Name()), our dir
		if err != nil {
			return fmt.Errorf("read %s: %w", srcPath, err)
		}
		if err := os.WriteFile(dstPath, data, 0600); err != nil { // #nosec G703 -- dstPath from Join(dst, e.Name()), our dir
			return fmt.Errorf("write %s: %w", dstPath, err)
		}
	}
	return nil
}
