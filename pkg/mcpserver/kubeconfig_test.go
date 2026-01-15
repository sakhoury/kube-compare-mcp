// SPDX-License-Identifier: Apache-2.0

package mcpserver_test

import (
	"encoding/base64"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/sakhoury/kube-compare-mcp/pkg/mcpserver"
)

var _ = Describe("Kubeconfig", func() {

	Describe("DecodeKubeconfig", func() {
		DescribeTable("decoding base64 kubeconfigs",
			func(input string, wantErr bool, errContains string) {
				_, err := mcpserver.DecodeKubeconfig(input)
				if wantErr {
					Expect(err).To(HaveOccurred())
					if errContains != "" {
						Expect(err.Error()).To(ContainSubstring(errContains))
					}
				} else {
					Expect(err).NotTo(HaveOccurred())
				}
			},
			Entry("valid base64 kubeconfig",
				EncodeKubeconfig(ValidKubeconfig), false, ""),
			Entry("invalid base64",
				"not-valid-base64!!!", true, "invalid base64"),
			Entry("empty input",
				"", true, "kubeconfig is empty"),
			Entry("oversized input",
				base64.StdEncoding.EncodeToString(make([]byte, 2*1024*1024)),
				true, "exceeds maximum"),
		)
	})

	Describe("ParseKubeconfig", func() {
		DescribeTable("parsing kubeconfig YAML",
			func(input string, wantErr bool, errContains string) {
				_, err := mcpserver.ParseKubeconfig([]byte(input))
				if wantErr {
					Expect(err).To(HaveOccurred())
					if errContains != "" {
						Expect(err.Error()).To(ContainSubstring(errContains))
					}
				} else {
					Expect(err).NotTo(HaveOccurred())
				}
			},
			Entry("valid kubeconfig",
				ValidKubeconfig, false, ""),
			Entry("invalid YAML",
				"not: valid: yaml: content", true, "failed to parse"),
			Entry("no clusters", `
apiVersion: v1
kind: Config
users:
- name: test-user
  user:
    token: test-token
contexts:
- name: test-context
  context:
    cluster: test-cluster
    user: test-user
`, true, "no clusters"),
			Entry("no users", `
apiVersion: v1
kind: Config
clusters:
- name: test-cluster
  cluster:
    server: https://localhost:6443
contexts:
- name: test-context
  context:
    cluster: test-cluster
    user: test-user
`, true, "no user credentials"),
			Entry("no contexts", `
apiVersion: v1
kind: Config
clusters:
- name: test-cluster
  cluster:
    server: https://localhost:6443
users:
- name: test-user
  user:
    token: test-token
`, true, "no contexts"),
		)
	})

	Describe("BlockExecAuth", func() {
		DescribeTable("blocking exec auth providers",
			func(kubeconfig string, wantBlocked bool, errContains string) {
				config, err := mcpserver.ParseKubeconfig([]byte(kubeconfig))
				Expect(err).NotTo(HaveOccurred())

				err = mcpserver.BlockExecAuth(config)
				if wantBlocked {
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring(errContains))
				} else {
					Expect(err).NotTo(HaveOccurred())
				}
			},
			Entry("token auth allowed",
				ValidKubeconfig, false, ""),
			Entry("cert auth allowed",
				CertAuthKubeconfig, false, ""),
			Entry("exec auth blocked",
				ExecAuthKubeconfig, true, "exec-based authentication"),
		)
	})

	Describe("BlockAuthProviderPlugins", func() {
		DescribeTable("blocking auth provider plugins",
			func(kubeconfig string, wantBlocked bool, errContains string) {
				config, err := mcpserver.ParseKubeconfig([]byte(kubeconfig))
				Expect(err).NotTo(HaveOccurred())

				err = mcpserver.BlockAuthProviderPlugins(config)
				if wantBlocked {
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring(errContains))
				} else {
					Expect(err).NotTo(HaveOccurred())
				}
			},
			Entry("token auth allowed",
				ValidKubeconfig, false, ""),
			Entry("cert auth allowed",
				CertAuthKubeconfig, false, ""),
			Entry("auth provider blocked",
				AuthProviderKubeconfig, true, "auth provider plugin"),
		)
	})

	Describe("ValidateKubeconfigSecurity", func() {
		DescribeTable("security validation",
			func(kubeconfig string, wantErr bool, errContains string) {
				config, err := mcpserver.ParseKubeconfig([]byte(kubeconfig))
				Expect(err).NotTo(HaveOccurred())

				err = mcpserver.ValidateKubeconfigSecurity(config)
				if wantErr {
					Expect(err).To(HaveOccurred())
					Expect(strings.ToLower(err.Error())).To(ContainSubstring(errContains))
				} else {
					Expect(err).NotTo(HaveOccurred())
				}
			},
			Entry("valid token auth",
				ValidKubeconfig, false, ""),
			Entry("valid cert auth",
				CertAuthKubeconfig, false, ""),
			Entry("exec auth rejected",
				ExecAuthKubeconfig, true, "exec"),
			Entry("auth provider rejected",
				AuthProviderKubeconfig, true, "auth provider"),
		)
	})

	Describe("BuildRestConfig", func() {
		DescribeTable("building REST config",
			func(kubeconfig string, contextName string, wantErr bool, errContains string, wantHost string) {
				config, err := mcpserver.ParseKubeconfig([]byte(kubeconfig))
				Expect(err).NotTo(HaveOccurred())

				restConfig, err := mcpserver.BuildRestConfig(config, contextName)
				if wantErr {
					Expect(err).To(HaveOccurred())
					Expect(strings.ToLower(err.Error())).To(ContainSubstring(errContains))
				} else {
					Expect(err).NotTo(HaveOccurred())
					Expect(restConfig.Host).To(Equal(wantHost))
				}
			},
			Entry("use current context",
				ValidKubeconfig, "", false, "", "https://192.168.1.100:6443"),
			Entry("specify context explicitly",
				ValidKubeconfig, "test-context", false, "", "https://192.168.1.100:6443"),
			Entry("non-existent context",
				ValidKubeconfig, "non-existent-context", true, "not found", ""),
			Entry("no current context and none specified",
				NoCurrentContextKubeconfig, "", true, "no context specified", ""),
		)
	})

	Describe("BuildSecureRestConfig", func() {
		DescribeTable("end-to-end secure config building",
			func(kubeconfig string, contextName string, wantErr bool, errContains string) {
				encoded := EncodeKubeconfig(kubeconfig)

				_, err := mcpserver.BuildSecureRestConfig(encoded, contextName)
				if wantErr {
					Expect(err).To(HaveOccurred())
					Expect(strings.ToLower(err.Error())).To(ContainSubstring(errContains))
				} else {
					Expect(err).NotTo(HaveOccurred())
				}
			},
			Entry("valid kubeconfig end-to-end",
				ValidKubeconfig, "", false, ""),
			Entry("cert auth kubeconfig",
				CertAuthKubeconfig, "", false, ""),
			Entry("exec auth blocked end-to-end",
				ExecAuthKubeconfig, "", true, "exec"),
			Entry("auth provider blocked end-to-end",
				AuthProviderKubeconfig, "", true, "auth provider"),
		)

		It("rejects invalid base64", func() {
			_, err := mcpserver.BuildSecureRestConfig("!!!not-valid-base64-chars###", "")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("base64"))
		})
	})

	Describe("DecodeOrParseKubeconfig", func() {
		DescribeTable("auto-detecting kubeconfig format",
			func(input string, wantNil bool, wantErr bool, errContains string) {
				result, err := mcpserver.DecodeOrParseKubeconfig(input)
				if wantErr {
					Expect(err).To(HaveOccurred())
					if errContains != "" {
						Expect(err.Error()).To(ContainSubstring(errContains))
					}
				} else {
					Expect(err).NotTo(HaveOccurred())
					if wantNil {
						Expect(result).To(BeNil())
					} else {
						Expect(result).NotTo(BeNil())
					}
				}
			},
			Entry("empty input returns nil (in-cluster mode)",
				"", true, false, ""),
			Entry("whitespace-only returns nil",
				"   \n\t  ", true, false, ""),
			Entry("raw YAML kubeconfig",
				ValidKubeconfig, false, false, ""),
			Entry("base64-encoded kubeconfig",
				EncodeKubeconfig(ValidKubeconfig), false, false, ""),
			Entry("raw YAML with cert auth",
				CertAuthKubeconfig, false, false, ""),
			Entry("base64 of cert auth kubeconfig",
				EncodeKubeconfig(CertAuthKubeconfig), false, false, ""),
			Entry("invalid content (not YAML or base64)",
				"this is not valid kubeconfig content!!!", false, true, "unable to parse"),
			Entry("valid base64 but not kubeconfig",
				base64.StdEncoding.EncodeToString([]byte("just some random text")),
				false, true, "unable to parse"),
		)

		It("returns raw bytes for valid YAML input", func() {
			result, err := mcpserver.DecodeOrParseKubeconfig(ValidKubeconfig)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(result)).To(ContainSubstring("apiVersion"))
			Expect(string(result)).To(ContainSubstring("test-cluster"))
		})

		It("returns decoded bytes for base64 input", func() {
			encoded := EncodeKubeconfig(ValidKubeconfig)
			result, err := mcpserver.DecodeOrParseKubeconfig(encoded)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(result)).To(ContainSubstring("apiVersion"))
		})

		It("rejects oversized input", func() {
			oversized := strings.Repeat("a", 2*1024*1024)
			_, err := mcpserver.DecodeOrParseKubeconfig(oversized)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("exceeds maximum"))
		})
	})

	Describe("BuildSecureRestConfigFromBytes", func() {
		DescribeTable("building REST config from bytes",
			func(kubeconfig string, contextName string, wantErr bool, errContains string) {
				_, err := mcpserver.BuildSecureRestConfigFromBytes([]byte(kubeconfig), contextName)
				if wantErr {
					Expect(err).To(HaveOccurred())
					Expect(strings.ToLower(err.Error())).To(ContainSubstring(errContains))
				} else {
					Expect(err).NotTo(HaveOccurred())
				}
			},
			Entry("valid kubeconfig with default context",
				ValidKubeconfig, "", false, ""),
			Entry("valid kubeconfig with explicit context",
				ValidKubeconfig, "test-context", false, ""),
			Entry("cert auth kubeconfig",
				CertAuthKubeconfig, "", false, ""),
			Entry("exec auth blocked",
				ExecAuthKubeconfig, "", true, "exec"),
			Entry("auth provider blocked",
				AuthProviderKubeconfig, "", true, "auth provider"),
			Entry("non-existent context",
				ValidKubeconfig, "non-existent", true, "not found"),
		)

		It("returns correct host from kubeconfig", func() {
			restConfig, err := mcpserver.BuildSecureRestConfigFromBytes([]byte(ValidKubeconfig), "")
			Expect(err).NotTo(HaveOccurred())
			Expect(restConfig.Host).To(Equal("https://192.168.1.100:6443"))
		})
	})

	Describe("SanitizeErrorMessage", func() {
		DescribeTable("sanitizing error messages",
			func(input string, wantClean bool) {
				result := mcpserver.SanitizeErrorMessage(input)
				if wantClean {
					Expect(result).NotTo(Equal(input))
					Expect(result).To(ContainSubstring("redacted"))
				} else {
					Expect(result).To(Equal(input))
				}
			},
			Entry("safe message",
				"failed to connect to server", false),
			Entry("contains token",
				"invalid token: abc123", true),
			Entry("contains password",
				"wrong password provided", true),
			Entry("contains secret",
				"secret key is invalid", true),
			Entry("contains credential",
				"credential expired", true),
			Entry("contains bearer",
				"bearer token rejected", true),
		)
	})

	Describe("Multiple users with mixed auth", func() {
		It("blocks kubeconfig with any exec auth user", func() {
			mixedKubeconfig := `
apiVersion: v1
kind: Config
current-context: valid-context
clusters:
- name: test-cluster
  cluster:
    server: https://192.168.1.100:6443
users:
- name: valid-user
  user:
    token: valid-token
- name: exec-user
  user:
    exec:
      command: /bin/malicious
      apiVersion: client.authentication.k8s.io/v1beta1
contexts:
- name: valid-context
  context:
    cluster: test-cluster
    user: valid-user
- name: exec-context
  context:
    cluster: test-cluster
    user: exec-user
`
			config, err := mcpserver.ParseKubeconfig([]byte(mixedKubeconfig))
			Expect(err).NotTo(HaveOccurred())

			err = mcpserver.BlockExecAuth(config)
			Expect(err).To(HaveOccurred())
		})
	})
})
