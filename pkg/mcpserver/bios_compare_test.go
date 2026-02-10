// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	sigsyaml "sigs.k8s.io/yaml"
)

var discardLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

var biosTestGVRToListKind = map[schema.GroupVersionResource]string{
	{Group: "metal3.io", Version: "v1alpha1", Resource: "baremetalhosts"}:         "BareMetalHostList",
	{Group: "metal3.io", Version: "v1alpha1", Resource: "hardwaredata"}:           "HardwareDataList",
	{Group: "metal3.io", Version: "v1alpha1", Resource: "hostfirmwarecomponents"}: "HostFirmwareComponentsList",
	{Group: "metal3.io", Version: "v1alpha1", Resource: "hostfirmwaresettings"}:   "HostFirmwareSettingsList",
	{Group: "", Version: "v1", Resource: "configmaps"}:                            "ConfigMapList",
}

func newBIOSTestFakeDynamicClient(objects ...runtime.Object) dynamic.Interface {
	scheme := runtime.NewScheme()
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, biosTestGVRToListKind, objects...)
}

var _ = Describe("BIOSCompare", func() {

	Describe("BIOSDiffTool", func() {
		var tool = BIOSDiffTool()

		It("has the correct name", func() {
			Expect(tool.Name).To(Equal("baremetal_bios_diff"))
		})

		It("has a title", func() {
			Expect(tool.Title).To(Equal("BIOS Configuration Comparator"))
		})

		It("has a description", func() {
			Expect(tool.Description).NotTo(BeEmpty())
			Expect(tool.Description).To(ContainSubstring("BIOS"))
		})

		It("has annotations indicating read-only behavior", func() {
			Expect(tool.Annotations).NotTo(BeNil())
			Expect(tool.Annotations.ReadOnlyHint).To(BeTrue())
			Expect(*tool.Annotations.DestructiveHint).To(BeFalse())
			Expect(tool.Annotations.IdempotentHint).To(BeTrue())
		})

		It("has input and output schemas", func() {
			Expect(tool.InputSchema).NotTo(BeNil())
			Expect(tool.OutputSchema).NotTo(BeNil())
		})
	})

	Describe("BIOSDiffInput struct", func() {
		It("can be created with all fields", func() {
			input := BIOSDiffInput{
				Kubeconfig:        "base64data",
				Context:           "my-context",
				Namespace:         "spoke-cluster",
				HostName:          "node-0",
				ReferenceSource:   "custom-refs",
				ReferenceOverride: "my-configmap",
				OutputFormat:      "yaml",
			}
			Expect(input.Kubeconfig).To(Equal("base64data"))
			Expect(input.Context).To(Equal("my-context"))
			Expect(input.Namespace).To(Equal("spoke-cluster"))
			Expect(input.HostName).To(Equal("node-0"))
			Expect(input.ReferenceSource).To(Equal("custom-refs"))
			Expect(input.ReferenceOverride).To(Equal("my-configmap"))
			Expect(input.OutputFormat).To(Equal("yaml"))
		})
	})

	Describe("extractBIOSVersion", func() {
		It("extracts BIOS version from valid HostFirmwareComponents", func() {
			hfc := newTestHostFirmwareComponents("node-0", "test-ns", "2.1.0")
			version := extractBIOSVersion(hfc)
			Expect(version).To(Equal("2.1.0"))
		})

		It("returns empty string when no bios component exists", func() {
			hfc := &unstructured.Unstructured{
				Object: map[string]any{
					"status": map[string]any{
						"components": []any{
							map[string]any{
								"component":      "bmc",
								"currentVersion": "1.0.0",
							},
						},
					},
				},
			}
			version := extractBIOSVersion(hfc)
			Expect(version).To(BeEmpty())
		})

		It("returns empty string when components are missing", func() {
			hfc := &unstructured.Unstructured{
				Object: map[string]any{
					"status": map[string]any{},
				},
			}
			version := extractBIOSVersion(hfc)
			Expect(version).To(BeEmpty())
		})

		It("handles malformed component entries", func() {
			hfc := &unstructured.Unstructured{
				Object: map[string]any{
					"status": map[string]any{
						"components": []any{
							"not-a-map",
							nil,
						},
					},
				},
			}
			version := extractBIOSVersion(hfc)
			Expect(version).To(BeEmpty())
		})
	})

	Describe("extractBIOSSettings", func() {
		It("extracts settings from valid HostFirmwareSettings", func() {
			hfs := newTestHostFirmwareSettings("node-0", "test-ns", map[string]string{
				"ProcVirtualization": "Enabled",
				"BootMode":           "UEFI",
			})
			settings := extractBIOSSettings(hfs)
			Expect(settings).To(HaveLen(2))
			Expect(settings["ProcVirtualization"]).To(Equal("Enabled"))
			Expect(settings["BootMode"]).To(Equal("UEFI"))
		})

		It("returns empty map when settings are missing", func() {
			hfs := &unstructured.Unstructured{
				Object: map[string]any{
					"status": map[string]any{},
				},
			}
			settings := extractBIOSSettings(hfs)
			Expect(settings).To(BeEmpty())
		})
	})

	Describe("parseSettingsYAML", func() {
		It("parses simple key-value pairs", func() {
			yaml := "ProcVirtualization: Enabled\nBootMode: UEFI"
			settings := parseSettingsYAML(yaml)
			Expect(settings).To(HaveLen(2))
			Expect(settings["ProcVirtualization"]).To(Equal("Enabled"))
			Expect(settings["BootMode"]).To(Equal("UEFI"))
		})

		It("ignores empty lines", func() {
			yaml := "Key1: Value1\n\nKey2: Value2\n\n"
			settings := parseSettingsYAML(yaml)
			Expect(settings).To(HaveLen(2))
		})

		It("ignores comment lines", func() {
			yaml := "# This is a comment\nKey1: Value1\n# Another comment\nKey2: Value2"
			settings := parseSettingsYAML(yaml)
			Expect(settings).To(HaveLen(2))
			Expect(settings["Key1"]).To(Equal("Value1"))
		})

		It("handles values with colons", func() {
			yaml := "URL: http://example.com:8080"
			settings := parseSettingsYAML(yaml)
			Expect(settings["URL"]).To(Equal("http://example.com:8080"))
		})

		It("trims whitespace around keys and values", func() {
			yaml := "  Key1  :  Value1  \n  Key2:Value2"
			settings := parseSettingsYAML(yaml)
			Expect(settings["Key1"]).To(Equal("Value1"))
			Expect(settings["Key2"]).To(Equal("Value2"))
		})

		It("returns empty map for empty input", func() {
			settings := parseSettingsYAML("")
			Expect(settings).To(BeEmpty())
		})
	})

	Describe("compareBIOSSettings", func() {
		It("returns no diffs when settings match", func() {
			expected := map[string]string{"Key1": "Value1", "Key2": "Value2"}
			actual := map[string]string{"Key1": "Value1", "Key2": "Value2", "Key3": "Value3"}
			diffs := compareBIOSSettings(expected, actual)
			Expect(diffs).To(BeEmpty())
		})

		It("returns diffs for mismatched values", func() {
			expected := map[string]string{"Key1": "Expected"}
			actual := map[string]string{"Key1": "Actual"}
			diffs := compareBIOSSettings(expected, actual)
			Expect(diffs).To(HaveLen(1))
			Expect(diffs[0].Setting).To(Equal("Key1"))
			Expect(diffs[0].Expected).To(Equal("Expected"))
			Expect(diffs[0].Actual).To(Equal("Actual"))
		})

		It("returns diffs for missing settings", func() {
			expected := map[string]string{"MissingSetting": "Value"}
			actual := map[string]string{}
			diffs := compareBIOSSettings(expected, actual)
			Expect(diffs).To(HaveLen(1))
			Expect(diffs[0].Setting).To(Equal("MissingSetting"))
			Expect(diffs[0].Expected).To(Equal("Value"))
			Expect(diffs[0].Actual).To(BeEmpty())
		})

		It("handles empty expected settings", func() {
			expected := map[string]string{}
			actual := map[string]string{"Key1": "Value1"}
			diffs := compareBIOSSettings(expected, actual)
			Expect(diffs).To(BeEmpty())
		})
	})

	Describe("normalizeForK8sName", func() {
		DescribeTable("normalization",
			func(input, expected string) {
				result := normalizeForK8sName(input, 0)
				Expect(result).To(Equal(expected))
			},
			Entry("lowercase conversion", "Dell Inc.", "dell-inc"),
			Entry("spaces to hyphens", "Dell PowerEdge R750", "dell-poweredge-r750"),
			Entry("removes parentheses", "HPE (Gen10)", "hpe-gen10"),
			Entry("removes periods", "Dell Inc.", "dell-inc"),
			Entry("removes commas", "ACME, Inc", "acme-inc"),
			Entry("slashes to hyphens", "model/variant", "model-variant"),
			Entry("underscores to hyphens", "model_variant", "model-variant"),
			Entry("multiple spaces collapse", "Dell   Inc", "dell-inc"),
			Entry("mixed special chars", "Dell (PowerEdge) R750/Plus", "dell-poweredge-r750-plus"),
		)

		It("truncates to maxLen when specified", func() {
			longInput := "This is a very long string that exceeds sixty three characters limit for labels"
			result := normalizeForK8sName(longInput, validation.DNS1123LabelMaxLength)
			Expect(len(result)).To(BeNumerically("<=", validation.DNS1123LabelMaxLength))
		})

		It("removes trailing hyphens after truncation", func() {
			input := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa bb"
			result := normalizeForK8sName(input, validation.DNS1123LabelMaxLength)
			Expect(result).NotTo(HaveSuffix("-"))
		})

		DescribeTable("produces valid DNS-1123 labels for real-world inputs",
			func(input string) {
				result := normalizeForK8sName(input, validation.DNS1123LabelMaxLength)
				Expect(validation.IsDNS1123Label(result)).To(BeEmpty(),
					"normalizeForK8sName(%q) = %q should be a valid DNS-1123 label", input, result)
			},
			Entry("Dell manufacturer", "Dell Inc."),
			Entry("HPE manufacturer", "Hewlett Packard Enterprise"),
			Entry("model with parens", "ProLiant DL380 (Gen10)"),
			Entry("model with slash", "PowerEdge R750/Plus"),
			Entry("model with underscore", "XR8620t_v2"),
			Entry("simple role", "master"),
			Entry("hyphenated role", "control-plane"),
		)
	})

	Describe("buildReferenceConfigMapName", func() {
		It("constructs correct name format", func() {
			name := buildReferenceConfigMapName("Dell Inc.", "PowerEdge R750", "master")
			Expect(name).To(Equal("bios-ref-dell-inc-poweredge-r750-master"))
		})

		It("handles simple values", func() {
			name := buildReferenceConfigMapName("dell", "r750", "worker")
			Expect(name).To(Equal("bios-ref-dell-r750-worker"))
		})
	})

	Describe("scoreModelMatch", func() {
		It("returns 1.0 for identical strings", func() {
			score := scoreModelMatch("PowerEdge R750", "PowerEdge R750")
			Expect(score).To(BeNumerically("~", 1.0, 0.01))
		})

		It("returns high score for similar strings", func() {
			score := scoreModelMatch("PowerEdge R750", "poweredge-r750")
			Expect(score).To(BeNumerically(">", 0.7))
		})

		It("returns low score for dissimilar strings", func() {
			score := scoreModelMatch("PowerEdge R750", "ProLiant DL380")
			Expect(score).To(BeNumerically("<", 0.5))
		})

		It("returns 0.0 for empty model label", func() {
			score := scoreModelMatch("PowerEdge R750", "")
			Expect(score).To(Equal(0.0))
		})

		It("is case insensitive", func() {
			score1 := scoreModelMatch("POWEREDGE R750", "poweredge r750")
			score2 := scoreModelMatch("PowerEdge R750", "PowerEdge R750")
			Expect(score1).To(BeNumerically("~", score2, 0.01))
		})
	})

	// Note: Full runBIOSComparison integration tests require a real cluster or
	// envtest because metal3 CRDs use singular resource names (e.g., "hardwaredata"
	// instead of "hardwaredatas") which is incompatible with the k8s fake dynamic
	// client's auto-pluralization.
	Describe("runBIOSComparison", func() {
		var ctx context.Context

		BeforeEach(func() {
			ctx = context.Background()
		})

		It("returns error when no BMHs found", func() {
			targetClient := newBIOSTestFakeDynamicClient()
			referenceClient := newBIOSTestFakeDynamicClient()

			_, err := runBIOSComparison(ctx, targetClient, referenceClient, "test-ns", "", "reference-configs", "", discardLogger)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no BareMetalHosts"))
		})

		It("returns error when specific host not found", func() {
			targetClient := newBIOSTestFakeDynamicClient()
			referenceClient := newBIOSTestFakeDynamicClient()

			_, err := runBIOSComparison(ctx, targetClient, referenceClient, "test-ns", "nonexistent-host", "reference-configs", "", discardLogger)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("findBestMatchConfigMap", func() {
		var ctx context.Context

		BeforeEach(func() {
			ctx = context.Background()
		})

		It("finds ConfigMap with matching labels", func() {
			cm := newTestReferenceConfigMap("bios-ref-dell-poweredge-r750-master", "reference-configs",
				"dell-inc", "poweredge-r750", "master", "2.1.0", "")
			client := newBIOSTestFakeDynamicClient(cm)

			result, name, err := findBestMatchConfigMap(ctx, client, "reference-configs", "Dell Inc.", "PowerEdge R750", "master", discardLogger)
			Expect(err).NotTo(HaveOccurred())
			Expect(name).To(Equal("bios-ref-dell-poweredge-r750-master"))
			Expect(result).NotTo(BeNil())
		})

		It("returns error when no matching vendor/role found", func() {
			cm := newTestReferenceConfigMap("bios-ref-hpe-proliant-master", "reference-configs",
				"hpe", "proliant-dl380", "master", "2.1.0", "")
			client := newBIOSTestFakeDynamicClient(cm)

			_, _, err := findBestMatchConfigMap(ctx, client, "reference-configs", "Dell Inc.", "PowerEdge R750", "master", discardLogger)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no ConfigMaps found"))
		})

		It("returns error when model similarity is below threshold", func() {
			cm := newTestReferenceConfigMap("bios-ref-dell-different-model-master", "reference-configs",
				"dell-inc", "completely-different-xyz", "master", "2.1.0", "")
			client := newBIOSTestFakeDynamicClient(cm)

			_, _, err := findBestMatchConfigMap(ctx, client, "reference-configs", "Dell Inc.", "PowerEdge R750", "master", discardLogger)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("similar enough"))
		})

		It("selects best match when multiple ConfigMaps exist", func() {
			cm1 := newTestReferenceConfigMap("bios-ref-dell-poweredge-r740-master", "reference-configs",
				"dell-inc", "poweredge-r740", "master", "2.0.0", "")
			cm2 := newTestReferenceConfigMap("bios-ref-dell-poweredge-r750-master", "reference-configs",
				"dell-inc", "poweredge-r750", "master", "2.1.0", "")
			client := newBIOSTestFakeDynamicClient(cm1, cm2)

			_, name, err := findBestMatchConfigMap(ctx, client, "reference-configs", "Dell Inc.", "PowerEdge R750", "master", discardLogger)
			Expect(err).NotTo(HaveOccurred())
			Expect(name).To(Equal("bios-ref-dell-poweredge-r750-master"))
		})
	})
})

func newTestHostFirmwareComponents(name, namespace, biosVersion string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "metal3.io/v1alpha1",
			"kind":       "HostFirmwareComponents",
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
			},
			"status": map[string]any{
				"components": []any{
					map[string]any{
						"component":      "bios",
						"currentVersion": biosVersion,
					},
				},
			},
		},
	}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "metal3.io",
		Version: "v1alpha1",
		Kind:    "HostFirmwareComponents",
	})
	return obj
}

func newTestHostFirmwareSettings(name, namespace string, settings map[string]string) *unstructured.Unstructured {
	settingsAny := make(map[string]any, len(settings))
	for k, v := range settings {
		settingsAny[k] = v
	}
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "metal3.io/v1alpha1",
			"kind":       "HostFirmwareSettings",
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
			},
			"status": map[string]any{
				"settings": settingsAny,
			},
		},
	}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "metal3.io",
		Version: "v1alpha1",
		Kind:    "HostFirmwareSettings",
	})
	return obj
}

func newTestReferenceConfigMap(name, namespace, vendor, model, role, biosVersion, settings string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]any{
					"bios-reference/vendor": vendor,
					"bios-reference/model":  model,
					"bios-reference/role":   role,
				},
			},
			"data": map[string]any{
				"biosVersion": biosVersion,
				"settings":    settings,
			},
		},
	}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "",
		Version: "v1",
		Kind:    "ConfigMap",
	})
	return obj
}

var _ = Describe("BIOSDiffInputSchema", func() {
	It("generates valid schema", func() {
		schema := BIOSDiffInputSchema()
		Expect(schema).NotTo(BeNil())
		Expect(schema.Properties).To(HaveKey("namespace"))
		Expect(schema.Properties).To(HaveKey("host_name"))
		Expect(schema.Properties).To(HaveKey("reference_source"))
		Expect(schema.Properties).To(HaveKey("output_format"))
	})

	It("has enum constraint for output_format", func() {
		schema := BIOSDiffInputSchema()
		prop := schema.Properties["output_format"]
		Expect(prop.Enum).To(ContainElements("json", "yaml"))
	})
})

var _ = Describe("BIOSDiffOutputSchema", func() {
	It("generates valid schema", func() {
		schema := BIOSDiffOutputSchema()
		Expect(schema).NotTo(BeNil())
		Expect(schema.Properties).To(HaveKey("Namespace"))
		Expect(schema.Properties).To(HaveKey("Hosts"))
		Expect(schema.Properties).To(HaveKey("Summary"))
	})
})

var _ = Describe("HostBIOSResult", func() {
	It("can serialize to JSON with expected field names", func() {
		result := HostBIOSResult{
			Name:      "node-0",
			Namespace: "test-ns",
			Role:      "master",
			ServerModel: ServerModelInfo{
				Manufacturer: "Dell Inc.",
				ProductName:  "PowerEdge R750",
			},
			Reference:       "bios-ref-dell-master",
			ReferenceSource: ReferenceSourceMCPServer,
			BIOSVersion: BIOSVersionResult{
				Expected: "2.1.0",
				Actual:   "2.0.0",
				Match:    false,
			},
			SettingsDiff: []BIOSSettingDiff{
				{Setting: "Key", Expected: "A", Actual: "B"},
			},
			Compliant: false,
		}

		Expect(result.Name).To(Equal("node-0"))
		Expect(result.ReferenceSource).To(Equal(ReferenceSourceMCPServer))
		Expect(result.SettingsDiff).To(HaveLen(1))
	})
})

var _ = Describe("BIOSDiffSummary", func() {
	It("aggregates counts correctly", func() {
		summary := BIOSDiffSummary{
			TotalHosts:     10,
			CompliantHosts: 7,
			NumDiffHosts:   2,
			ErrorHosts:     1,
		}

		Expect(summary.TotalHosts).To(Equal(10))
		Expect(summary.CompliantHosts + summary.NumDiffHosts + summary.ErrorHosts).To(Equal(summary.TotalHosts))
	})
})

var _ = Describe("BIOSDiffResult output format", func() {
	var result *BIOSDiffResult

	BeforeEach(func() {
		result = &BIOSDiffResult{
			Namespace: "test-ns",
			Hosts: []HostBIOSResult{
				{
					Name:      "node-0",
					Namespace: "test-ns",
					Role:      "master",
					ServerModel: ServerModelInfo{
						Manufacturer: "Dell Inc.",
						ProductName:  "PowerEdge R750",
					},
					Reference:       "bios-ref-dell-r750-master",
					ReferenceSource: ReferenceSourceMCPServer,
					BIOSVersion: BIOSVersionResult{
						Expected: "2.1.0",
						Actual:   "2.0.0",
						Match:    false,
					},
					SettingsDiff: []BIOSSettingDiff{
						{Setting: "BootMode", Expected: "Uefi", Actual: "Legacy"},
					},
					Compliant: false,
				},
			},
			Summary: BIOSDiffSummary{
				TotalHosts:     1,
				CompliantHosts: 0,
				NumDiffHosts:   1,
				ErrorHosts:     0,
			},
		}
	})

	It("produces valid JSON output", func() {
		outputBytes, err := json.MarshalIndent(result, "", "  ")
		Expect(err).NotTo(HaveOccurred())

		var parsed BIOSDiffResult
		Expect(json.Unmarshal(outputBytes, &parsed)).To(Succeed())
		Expect(parsed.Namespace).To(Equal("test-ns"))
		Expect(parsed.Hosts).To(HaveLen(1))
		Expect(parsed.Hosts[0].Name).To(Equal("node-0"))
		Expect(parsed.Hosts[0].BIOSVersion.Expected).To(Equal("2.1.0"))
		Expect(parsed.Hosts[0].SettingsDiff).To(HaveLen(1))
		Expect(parsed.Summary.NumDiffHosts).To(Equal(1))
	})

	It("produces valid YAML output", func() {
		outputBytes, err := sigsyaml.Marshal(result)
		Expect(err).NotTo(HaveOccurred())

		var parsed BIOSDiffResult
		Expect(sigsyaml.Unmarshal(outputBytes, &parsed)).To(Succeed())
		Expect(parsed.Namespace).To(Equal("test-ns"))
		Expect(parsed.Hosts).To(HaveLen(1))
		Expect(parsed.Hosts[0].Name).To(Equal("node-0"))
		Expect(parsed.Hosts[0].BIOSVersion.Expected).To(Equal("2.1.0"))
		Expect(parsed.Hosts[0].SettingsDiff).To(HaveLen(1))
		Expect(parsed.Summary.NumDiffHosts).To(Equal(1))
	})

	It("JSON and YAML produce equivalent data", func() {
		jsonBytes, err := json.MarshalIndent(result, "", "  ")
		Expect(err).NotTo(HaveOccurred())

		yamlBytes, err := sigsyaml.Marshal(result)
		Expect(err).NotTo(HaveOccurred())

		var fromJSON, fromYAML BIOSDiffResult
		Expect(json.Unmarshal(jsonBytes, &fromJSON)).To(Succeed())
		Expect(sigsyaml.Unmarshal(yamlBytes, &fromYAML)).To(Succeed())
		Expect(fromJSON).To(Equal(fromYAML))
	})
})

var _ = Describe("HandleBIOSDiff input validation", func() {
	It("rejects context without kubeconfig", func() {
		input := BIOSDiffInput{
			Context:   "some-context",
			Namespace: "test-ns",
		}
		result, _, err := HandleBIOSDiff(context.Background(), nil, input)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.IsError).To(BeTrue())
		textContent, ok := result.Content[0].(*mcp.TextContent)
		Expect(ok).To(BeTrue())
		Expect(textContent.Text).To(ContainSubstring("kubeconfig"))
	})

	It("rejects empty namespace", func() {
		input := BIOSDiffInput{
			Namespace: "",
		}
		result, _, err := HandleBIOSDiff(context.Background(), nil, input)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.IsError).To(BeTrue())
		textContent, ok := result.Content[0].(*mcp.TextContent)
		Expect(ok).To(BeTrue())
		Expect(textContent.Text).To(ContainSubstring("namespace"))
	})
})

var _ = Describe("Context cancellation", func() {
	It("returns error when context is canceled", func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		input := BIOSDiffInput{
			Namespace: "test-ns",
		}
		result, _, err := HandleBIOSDiff(ctx, nil, input)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.IsError).To(BeTrue())
	})
})

var _ = Describe("Constants", func() {
	It("has expected default reference namespace", func() {
		Expect(DefaultReferenceConfigNamespace).To(Equal("reference-configs"))
	})

	It("has expected BMH role annotation key", func() {
		Expect(BMHRoleAnnotation).To(Equal("bmac.agent-install.openshift.io/role"))
	})

	It("has expected reference source constant", func() {
		Expect(ReferenceSourceMCPServer).To(Equal("mcp-server-cluster"))
	})
})
