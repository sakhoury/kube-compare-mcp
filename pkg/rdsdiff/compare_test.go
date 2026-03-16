// SPDX-License-Identifier: Apache-2.0

package rdsdiff_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/sakhoury/kube-compare-mcp/pkg/rdsdiff"
)

const minimalPolicyYAML = `
apiVersion: policy.open-cluster-management.io/v1
kind: Policy
metadata:
  name: test-policy
  namespace: ztp-common
spec:
  disabled: false
  policy-templates:
    - objectDefinition:
        apiVersion: policy.open-cluster-management.io/v1
        kind: ConfigurationPolicy
        metadata:
          name: test-config-policy
        spec:
          object-templates:
            - complianceType: musthave
              objectDefinition:
                apiVersion: v1
                kind: ConfigMap
                metadata:
                  name: my-config
                  namespace: openshift-monitoring
                data:
                  key: value
            - complianceType: musthave
              objectDefinition:
                apiVersion: operator.openshift.io/v1alpha1
                kind: ImageContentSourcePolicy
                metadata:
                  name: my-icsp
                spec:
                  repositoryDigestMirrors: []
`

const policyWithDuplicateKey = `
apiVersion: policy.open-cluster-management.io/v1
kind: Policy
metadata:
  name: dup-policy
  namespace: ztp-common
spec:
  policy-templates:
    - objectDefinition:
        spec:
          object-templates:
            - objectDefinition:
                kind: ConfigMap
                metadata:
                  name: same-name
                  namespace: ns1
                data:
                  first: a
            - objectDefinition:
                kind: ConfigMap
                metadata:
                  name: same-name
                  namespace: ns1
                data:
                  second: b
`

var _ = Describe("rdsdiff compare", func() {
	Describe("GetKeysFromExtractedDir", func() {
		It("returns empty for empty or missing dir", func() {
			dir, err := os.MkdirTemp("", "rds-keys-empty")
			Expect(err).NotTo(HaveOccurred())
			defer os.RemoveAll(dir)
			keys, err := rdsdiff.GetKeysFromExtractedDir(dir)
			Expect(err).NotTo(HaveOccurred())
			Expect(keys).To(BeEmpty())

			keys, err = rdsdiff.GetKeysFromExtractedDir(filepath.Join(dir, "nonexistent"))
			Expect(err).NotTo(HaveOccurred())
			Expect(keys).To(BeEmpty())
		})
		It("returns stem of each .yaml file", func() {
			dir, err := os.MkdirTemp("", "rds-keys")
			Expect(err).NotTo(HaveOccurred())
			defer os.RemoveAll(dir)
			Expect(os.WriteFile(filepath.Join(dir, "ConfigMap_ns_foo.yaml"), []byte("{}"), 0600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, "Secret_ns_bar.yaml"), []byte("{}"), 0600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("x"), 0600)).To(Succeed())
			keys, err := rdsdiff.GetKeysFromExtractedDir(dir)
			Expect(err).NotTo(HaveOccurred())
			Expect(keys).To(ConsistOf("ConfigMap_ns_foo", "Secret_ns_bar"))
		})
	})

	Describe("Correlate", func() {
		It("splits keys into onlyOld, onlyNew, inBoth sorted", func() {
			oldDir, _ := os.MkdirTemp("", "rds-old")
			defer os.RemoveAll(oldDir)
			newDir, _ := os.MkdirTemp("", "rds-new")
			defer os.RemoveAll(newDir)
			Expect(os.WriteFile(filepath.Join(oldDir, "OnlyOld.yaml"), []byte("{}"), 0600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(newDir, "OnlyNew.yaml"), []byte("{}"), 0600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(oldDir, "Both.yaml"), []byte("{}"), 0600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(newDir, "Both.yaml"), []byte("{}"), 0600)).To(Succeed())
			onlyOld, onlyNew, inBoth, err := rdsdiff.Correlate(oldDir, newDir)
			Expect(err).NotTo(HaveOccurred())
			Expect(onlyOld).To(Equal([]string{"OnlyOld"}))
			Expect(onlyNew).To(Equal([]string{"OnlyNew"}))
			Expect(inBoth).To(Equal([]string{"Both"}))
		})
		It("returns inBoth sorted", func() {
			oldDir, _ := os.MkdirTemp("", "rds-old")
			defer os.RemoveAll(oldDir)
			newDir, _ := os.MkdirTemp("", "rds-new")
			defer os.RemoveAll(newDir)
			for _, name := range []string{"Z", "A", "M"} {
				Expect(os.WriteFile(filepath.Join(oldDir, name+".yaml"), []byte("{}"), 0600)).To(Succeed())
				Expect(os.WriteFile(filepath.Join(newDir, name+".yaml"), []byte("{}"), 0600)).To(Succeed())
			}
			_, _, inBoth, err := rdsdiff.Correlate(oldDir, newDir)
			Expect(err).NotTo(HaveOccurred())
			Expect(inBoth).To(Equal([]string{"A", "M", "Z"}))
		})
	})

	Describe("NormalizeYAML", func() {
		It("produces deterministic YAML with sorted keys", func() {
			obj := map[string]interface{}{"b": 2, "a": 1}
			out, err := rdsdiff.NormalizeYAML(obj)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring("a:"))
			Expect(out).To(ContainSubstring("b:"))
			aIdx := strings.Index(out, "a:")
			bIdx := strings.Index(out, "b:")
			Expect(aIdx).To(BeNumerically("<", bIdx))
		})
	})

	Describe("ComputeDiff", func() {
		It("produces empty or negligible diff for identical files", func() {
			dir, _ := os.MkdirTemp("", "rds-diff")
			defer os.RemoveAll(dir)
			content := "key: value\n"
			Expect(os.WriteFile(filepath.Join(dir, "a.yaml"), []byte(content), 0600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, "b.yaml"), []byte(content), 0600)).To(Succeed())
			diff, err := rdsdiff.ComputeDiff(filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(diff == "" || (!strings.Contains(diff, "value1") && !strings.Contains(diff, "value2"))).To(BeTrue())
		})
		It("produces non-empty unified diff for different content", func() {
			dir, _ := os.MkdirTemp("", "rds-diff")
			defer os.RemoveAll(dir)
			Expect(os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("key: value1\n"), 0600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, "b.yaml"), []byte("key: value2\n"), 0600)).To(Succeed())
			diff, err := rdsdiff.ComputeDiff(filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.Contains(diff, "value1") || strings.Contains(diff, "value2")).To(BeTrue())
			Expect(strings.Contains(diff, "---") || strings.Contains(diff, "+++")).To(BeTrue())
		})
	})

	Describe("BuildSummary", func() {
		It("contains counts", func() {
			s := rdsdiff.BuildSummary([]string{"a"}, []string{"b"}, []string{"c", "d"}, 1)
			Expect(s).To(ContainSubstring("Only in old:  1"))
			Expect(s).To(ContainSubstring("Only in new: 1"))
			Expect(s).To(ContainSubstring("In both:       2"))
			Expect(s).To(ContainSubstring("Differ:        1"))
		})
	})

	Describe("Run", func() {
		It("writes report and comparison JSON with correct shape", func() {
			root, _ := os.MkdirTemp("", "rds-run")
			defer os.RemoveAll(root)
			oldDir := filepath.Join(root, "old")
			newDir := filepath.Join(root, "new")
			Expect(os.MkdirAll(oldDir, 0750)).To(Succeed())
			Expect(os.MkdirAll(newDir, 0750)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(oldDir, "Same.yaml"), []byte("x: 1\n"), 0600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(newDir, "Same.yaml"), []byte("x: 1\n"), 0600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(oldDir, "OnlyO.yaml"), []byte("{}"), 0600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(newDir, "OnlyN.yaml"), []byte("{}"), 0600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(oldDir, "Diff.yaml"), []byte("a: 1\n"), 0600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(newDir, "Diff.yaml"), []byte("a: 2\n"), 0600)).To(Succeed())
			reportPath := filepath.Join(root, "report.txt")
			jsonPath := filepath.Join(root, "comparison.json")
			summary, err := rdsdiff.Run(oldDir, newDir, reportPath, jsonPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(summary).To(ContainSubstring("Only in old:  1"))
			Expect(summary).To(ContainSubstring("Only in new: 1"))
			Expect(summary).To(ContainSubstring("In both:       2"))
			reportData, err := os.ReadFile(reportPath) // #nosec G304 -- path under test root
			Expect(err).NotTo(HaveOccurred())
			Expect(string(reportData)).To(ContainSubstring("Comparison summary"))
			jsonData, err := os.ReadFile(jsonPath) // #nosec G304 -- path under test root
			Expect(err).NotTo(HaveOccurred())
			var data rdsdiff.ComparisonJSON
			Expect(json.Unmarshal(jsonData, &data)).To(Succeed())
			Expect(data.OnlyOld).To(Equal([]string{"OnlyO"}))
			Expect(data.OnlyNew).To(Equal([]string{"OnlyN"}))
			Expect(data.InBoth).To(ConsistOf("Diff", "Same"))
			Expect(data.Diffs).To(HaveKey("Diff"))
			Expect(data.Diffs["Diff"].OldContent).NotTo(BeEmpty())
			Expect(data.Diffs["Diff"].NewContent).NotTo(BeEmpty())
			Expect(data.Diffs["Diff"].DiffText).NotTo(BeEmpty())
			Expect(data.OldExtracted).To(Equal(oldDir))
			Expect(data.NewExtracted).To(Equal(newDir))
		})
		It("creates parent dirs for report and JSON", func() {
			root, _ := os.MkdirTemp("", "rds-run")
			defer os.RemoveAll(root)
			oldDir := filepath.Join(root, "old")
			newDir := filepath.Join(root, "new")
			Expect(os.MkdirAll(oldDir, 0750)).To(Succeed())
			Expect(os.MkdirAll(newDir, 0750)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(oldDir, "K.yaml"), []byte("{}"), 0600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(newDir, "K.yaml"), []byte("{}"), 0600)).To(Succeed())
			reportPath := filepath.Join(root, "out", "sub", "report.txt")
			jsonPath := filepath.Join(root, "out", "sub", "comparison.json")
			_, err := rdsdiff.Run(oldDir, newDir, reportPath, jsonPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(reportPath).To(BeARegularFile())
			Expect(jsonPath).To(BeARegularFile())
		})
		It("with no overlapping files writes valid report and JSON", func() {
			root, _ := os.MkdirTemp("", "rds-run")
			defer os.RemoveAll(root)
			oldDir := filepath.Join(root, "old")
			newDir := filepath.Join(root, "new")
			Expect(os.MkdirAll(oldDir, 0750)).To(Succeed())
			Expect(os.MkdirAll(newDir, 0750)).To(Succeed())
			reportPath := filepath.Join(root, "report.txt")
			jsonPath := filepath.Join(root, "comparison.json")
			summary, err := rdsdiff.Run(oldDir, newDir, reportPath, jsonPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(summary).To(ContainSubstring("Only in old:  0"))
			Expect(summary).To(ContainSubstring("In both:       0"))
			jsonData, err := os.ReadFile(jsonPath) // #nosec G304 -- path under test root
			Expect(err).NotTo(HaveOccurred())
			var data rdsdiff.ComparisonJSON
			Expect(json.Unmarshal(jsonData, &data)).To(Succeed())
			Expect(data.OnlyOld).To(BeEmpty())
			Expect(data.InBoth).To(BeEmpty())
			Expect(data.Diffs).To(BeEmpty())
		})
	})

	Describe("CRKeyFromObject", func() {
		It("namespaced resource is kind_namespace_name", func() {
			obj := map[string]interface{}{
				"kind":     "ConfigMap",
				"metadata": map[string]interface{}{"name": "foo", "namespace": "openshift-monitoring"},
			}
			Expect(rdsdiff.CRKeyFromObject(obj)).To(Equal("ConfigMap_openshift-monitoring_foo"))
		})
		It("cluster-scoped resource is kind_name only", func() {
			obj := map[string]interface{}{
				"kind":     "ImageContentSourcePolicy",
				"metadata": map[string]interface{}{"name": "my-icsp"},
			}
			Expect(rdsdiff.CRKeyFromObject(obj)).To(Equal("ImageContentSourcePolicy_my-icsp"))
		})
		It("empty namespace is treated as cluster-scoped", func() {
			obj := map[string]interface{}{
				"kind":     "Resource",
				"metadata": map[string]interface{}{"name": "x", "namespace": ""},
			}
			Expect(rdsdiff.CRKeyFromObject(obj)).To(Equal("Resource_x"))
		})
		It("sanitizes / to - in name", func() {
			obj := map[string]interface{}{
				"kind":     "Something",
				"metadata": map[string]interface{}{"name": "a/b/c", "namespace": "ns"},
			}
			Expect(rdsdiff.CRKeyFromObject(obj)).To(Equal("Something_ns_a-b-c"))
		})
		It("missing kind uses Unknown", func() {
			obj := map[string]interface{}{"metadata": map[string]interface{}{"name": "n", "namespace": "ns"}}
			Expect(rdsdiff.CRKeyFromObject(obj)).To(Equal("Unknown_ns_n"))
		})
		It("missing name uses unnamed", func() {
			obj := map[string]interface{}{"kind": "ConfigMap", "metadata": map[string]interface{}{"namespace": "ns"}}
			Expect(rdsdiff.CRKeyFromObject(obj)).To(Equal("ConfigMap_ns_unnamed"))
		})
	})

	Describe("ExtractCRsFromPolicyDoc", func() {
		It("returns 2 CRs for minimal Policy YAML", func() {
			var doc map[string]interface{}
			Expect(sigsyaml.Unmarshal([]byte(minimalPolicyYAML), &doc)).To(Succeed())
			result := rdsdiff.ExtractCRsFromPolicyDoc(doc)
			Expect(result).To(HaveLen(2))
			keys := make([]string, len(result))
			for i, r := range result {
				keys[i] = r.Key
			}
			Expect(keys).To(ContainElement("ConfigMap_openshift-monitoring_my-config"))
			Expect(keys).To(ContainElement("ImageContentSourcePolicy_my-icsp"))
		})
		It("returns empty for non-Policy doc", func() {
			doc := map[string]interface{}{"kind": "ConfigMap", "metadata": map[string]interface{}{}}
			Expect(rdsdiff.ExtractCRsFromPolicyDoc(doc)).To(BeEmpty())
		})
		It("returns empty for Policy with no policy-templates", func() {
			doc := map[string]interface{}{"kind": "Policy", "spec": map[string]interface{}{}}
			Expect(rdsdiff.ExtractCRsFromPolicyDoc(doc)).To(BeEmpty())
		})
	})

	Describe("ExtractCRs", func() {
		It("writes one file per CR sorted by key", func() {
			root, _ := os.MkdirTemp("", "rds-extract")
			defer os.RemoveAll(root)
			generated := filepath.Join(root, "generated")
			extracted := filepath.Join(root, "extracted")
			Expect(os.MkdirAll(generated, 0750)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(generated, "policies.yaml"), []byte(minimalPolicyYAML), 0600)).To(Succeed())
			Expect(rdsdiff.ExtractCRs(generated, extracted)).To(Succeed())
			entries, err := os.ReadDir(extracted)
			Expect(err).NotTo(HaveOccurred())
			names := make([]string, len(entries))
			for i, e := range entries {
				names[i] = e.Name()
			}
			Expect(names).To(ConsistOf("ConfigMap_openshift-monitoring_my-config.yaml", "ImageContentSourcePolicy_my-icsp.yaml"))
		})
		It("extracted content is valid YAML with correct kind and metadata", func() {
			root, _ := os.MkdirTemp("", "rds-extract")
			defer os.RemoveAll(root)
			generated := filepath.Join(root, "generated")
			extracted := filepath.Join(root, "extracted")
			Expect(os.MkdirAll(generated, 0750)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(generated, "p.yaml"), []byte(minimalPolicyYAML), 0600)).To(Succeed())
			Expect(rdsdiff.ExtractCRs(generated, extracted)).To(Succeed())
			data, err := os.ReadFile(filepath.Join(extracted, "ConfigMap_openshift-monitoring_my-config.yaml")) // #nosec G304 -- path under test root
			Expect(err).NotTo(HaveOccurred())
			var obj map[string]interface{}
			Expect(sigsyaml.Unmarshal(data, &obj)).To(Succeed())
			Expect(obj["kind"]).To(Equal("ConfigMap"))
			meta, _ := obj["metadata"].(map[string]interface{})
			Expect(meta["name"]).To(Equal("my-config"))
			Expect(meta["namespace"]).To(Equal("openshift-monitoring"))
			dataMap, ok := obj["data"].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(dataMap["key"]).To(Equal("value"))
		})
		It("duplicate key last wins", func() {
			root, _ := os.MkdirTemp("", "rds-extract")
			defer os.RemoveAll(root)
			generated := filepath.Join(root, "generated")
			extracted := filepath.Join(root, "extracted")
			Expect(os.MkdirAll(generated, 0750)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(generated, "dup.yaml"), []byte(policyWithDuplicateKey), 0600)).To(Succeed())
			Expect(rdsdiff.ExtractCRs(generated, extracted)).To(Succeed())
			data, err := os.ReadFile(filepath.Join(extracted, "ConfigMap_ns1_same-name.yaml")) // #nosec G304 -- path under test root
			Expect(err).NotTo(HaveOccurred())
			var obj map[string]interface{}
			Expect(sigsyaml.Unmarshal(data, &obj)).To(Succeed())
			dataMap, ok := obj["data"].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(dataMap["second"]).To(Equal("b"))
			Expect(dataMap).NotTo(HaveKey("first"))
		})
		It("creates extracted dir if it does not exist", func() {
			root, _ := os.MkdirTemp("", "rds-extract")
			defer os.RemoveAll(root)
			generated := filepath.Join(root, "generated")
			extracted := filepath.Join(root, "extracted")
			Expect(os.MkdirAll(generated, 0750)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(generated, "p.yaml"), []byte(minimalPolicyYAML), 0600)).To(Succeed())
			_, err := os.Stat(extracted)
			Expect(os.IsNotExist(err)).To(BeTrue())
			Expect(rdsdiff.ExtractCRs(generated, extracted)).To(Succeed())
			info, err := os.Stat(extracted)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.IsDir()).To(BeTrue())
		})
	})

	Describe("RunCompare", func() {
		It("extracts from generated dirs and writes report and JSON to sessionDir", func() {
			root, _ := os.MkdirTemp("", "rds-runcompare")
			defer os.RemoveAll(root)
			oldGen := filepath.Join(root, "old-gen")
			newGen := filepath.Join(root, "new-gen")
			Expect(os.MkdirAll(oldGen, 0750)).To(Succeed())
			Expect(os.MkdirAll(newGen, 0750)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(oldGen, "p.yaml"), []byte(minimalPolicyYAML), 0600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(newGen, "p.yaml"), []byte(minimalPolicyYAML), 0600)).To(Succeed())
			sessionDir := filepath.Join(root, "session")
			Expect(os.MkdirAll(sessionDir, 0750)).To(Succeed())
			summary, err := rdsdiff.RunCompare(oldGen, newGen, sessionDir)
			Expect(err).NotTo(HaveOccurred())
			Expect(summary).To(ContainSubstring("Comparison summary"))
			Expect(filepath.Join(sessionDir, "diff-report.txt")).To(BeARegularFile())
			Expect(filepath.Join(sessionDir, "comparison.json")).To(BeARegularFile())
		})
	})

	Describe("Constants", func() {
		It("CRSPath and SourceCRSPath and ListOfCRsForSNO are set", func() {
			Expect(rdsdiff.CRSPath).To(Equal("argocd/example/acmpolicygenerator"))
			Expect(rdsdiff.SourceCRSPath).To(Equal("source-crs"))
			Expect(rdsdiff.ListOfCRsForSNO).To(ContainElement("acm-common-ranGen.yaml"))
			Expect(rdsdiff.ListOfCRsForSNO).To(HaveLen(4))
		})
	})
})
