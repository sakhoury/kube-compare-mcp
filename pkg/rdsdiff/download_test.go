// SPDX-License-Identifier: Apache-2.0

package rdsdiff_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/sakhoury/kube-compare-mcp/pkg/rdsdiff"
)

var _ = Describe("rdsdiff download", func() {
	Describe("ParseGitHubTreeURL", func() {
		It("parses full URL with path", func() {
			u, err := rdsdiff.ParseGitHubTreeURL("https://github.com/openshift-kni/telco-reference/tree/konflux-telco-core-rds-4-20/telco-ran/configuration")
			Expect(err).NotTo(HaveOccurred())
			Expect(u.Owner).To(Equal("openshift-kni"))
			Expect(u.Repo).To(Equal("telco-reference"))
			Expect(u.Branch).To(Equal("konflux-telco-core-rds-4-20"))
			Expect(u.Path).To(Equal("telco-ran/configuration"))
			Expect(u.ArchiveURL()).To(Equal("https://github.com/openshift-kni/telco-reference/archive/refs/heads/konflux-telco-core-rds-4-20.zip"))
			Expect(u.TopLevelDir()).To(Equal("telco-reference-konflux-telco-core-rds-4-20"))
		})
		It("parses URL with branch only", func() {
			u, err := rdsdiff.ParseGitHubTreeURL("https://github.com/org/repo/tree/main")
			Expect(err).NotTo(HaveOccurred())
			Expect(u.Owner).To(Equal("org"))
			Expect(u.Repo).To(Equal("repo"))
			Expect(u.Branch).To(Equal("main"))
			Expect(u.Path).To(Equal(""))
		})
		It("rejects empty URL", func() {
			_, err := rdsdiff.ParseGitHubTreeURL("")
			Expect(err).To(HaveOccurred())
		})
		It("rejects non-GitHub URL", func() {
			_, err := rdsdiff.ParseGitHubTreeURL("https://gitlab.com/org/repo/-/tree/branch/path")
			Expect(err).To(HaveOccurred())
		})
		It("rejects URL without tree", func() {
			_, err := rdsdiff.ParseGitHubTreeURL("https://github.com/org/repo")
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("ValidateConfigurationRoot", func() {
		It("returns error when source-crs missing", func() {
			root, _ := os.MkdirTemp("", "rds-validate")
			defer os.RemoveAll(root)
			Expect(os.MkdirAll(filepath.Join(root, rdsdiff.CRSPath), 0750)).To(Succeed())
			err := rdsdiff.ValidateConfigurationRoot(root)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("source-crs"))
		})
		It("returns error when CRSPath missing", func() {
			root, _ := os.MkdirTemp("", "rds-validate")
			defer os.RemoveAll(root)
			Expect(os.MkdirAll(filepath.Join(root, rdsdiff.SourceCRSPath), 0750)).To(Succeed())
			err := rdsdiff.ValidateConfigurationRoot(root)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(rdsdiff.CRSPath))
		})
		It("succeeds when both exist", func() {
			root, _ := os.MkdirTemp("", "rds-validate")
			defer os.RemoveAll(root)
			Expect(os.MkdirAll(filepath.Join(root, rdsdiff.SourceCRSPath), 0750)).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(root, rdsdiff.CRSPath), 0750)).To(Succeed())
			err := rdsdiff.ValidateConfigurationRoot(root)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("DownloadURL", func() {
		It("returns error for empty URL", func() {
			dir, _ := os.MkdirTemp("", "rds-download")
			defer os.RemoveAll(dir)
			_, err := rdsdiff.DownloadURL("", dir, nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("URL is required"))
		})
		It("returns error for unsupported scheme", func() {
			dir, _ := os.MkdirTemp("", "rds-download")
			defer os.RemoveAll(dir)
			_, err := rdsdiff.DownloadURL("ftp://example.com/archive.zip", dir, nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("unsupported scheme"))
		})
	})
})

var _ = Describe("rdsdiff storage", func() {
	Describe("SessionDir", func() {
		It("creates session dir under workDir", func() {
			workDir, _ := os.MkdirTemp("", "rds-work")
			defer os.RemoveAll(workDir)
			sessionDir, err := rdsdiff.SessionDir(workDir, "req-1")
			Expect(err).NotTo(HaveOccurred())
			Expect(sessionDir).To(HavePrefix(workDir))
			Expect(sessionDir).To(ContainSubstring("rds-diff-req-1"))
			info, err := os.Stat(sessionDir)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.IsDir()).To(BeTrue())
		})
		It("uses temp dir when workDir empty", func() {
			sessionDir, err := rdsdiff.SessionDir("", "req-2")
			Expect(err).NotTo(HaveOccurred())
			Expect(sessionDir).NotTo(BeEmpty())
			Expect(sessionDir).To(ContainSubstring("rds-diff-req-2"))
		})
	})
})
