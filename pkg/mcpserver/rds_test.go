// SPDX-License-Identifier: Apache-2.0

package mcpserver_test

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"

	"github.com/sakhoury/kube-compare-mcp/pkg/mcpserver"
)

var _ = Describe("ReferenceHandler", func() {

	Describe("ExtractMajorMinorVersion", func() {
		DescribeTable("version extraction",
			func(version, expected string) {
				result := mcpserver.ExtractMajorMinorVersion(version)
				Expect(result).To(Equal(expected))
			},
			Entry("standard version", "4.20.0", "v4.20"),
			Entry("release candidate", "4.20.0-rc.3", "v4.20"),
			Entry("nightly build", "4.18.0-0.nightly-2024-01-15-000000", "v4.18"),
			Entry("EC build", "4.17.0-ec.1", "v4.17"),
			Entry("just major.minor", "4.16", "v4.16"),
			Entry("single digit minor", "4.9.0", "v4.9"),
			Entry("invalid version fallback", "invalid", "vinvalid"),
		)
	})

	Describe("BuildRDSReference", func() {
		DescribeTable("reference construction",
			func(rdsType, rhelVariant, ocpVersion, expectedContains string) {
				result := mcpserver.BuildRDSReference(rdsType, rhelVariant, ocpVersion)
				Expect(result).To(ContainSubstring(expectedContains))
				Expect(result).To(HavePrefix("container://"))
			},
			Entry("core RDS with RHEL9",
				mcpserver.RDSTypeCore, "rhel9", "v4.20",
				"openshift-telco-core-rds-rhel9:v4.20"),
			Entry("core RDS with RHEL8",
				mcpserver.RDSTypeCore, "rhel8", "v4.13",
				"openshift-telco-core-rds-rhel8:v4.13"),
			Entry("RAN RDS with RHEL8",
				mcpserver.RDSTypeRAN, "rhel8", "v4.17",
				"ztp-site-generate-rhel8:v4.17"),
		)
	})

	Describe("FilterVersionTags", func() {
		DescribeTable("tag filtering",
			func(tags []string, expected []string) {
				result := mcpserver.FilterVersionTags(tags)
				Expect(result).To(Equal(expected))
			},
			Entry("mixed tags with versions",
				[]string{"latest", "v4.18", "v4.19", "sha256-abc123", "v4.17", "main"},
				[]string{"v4.17", "v4.18", "v4.19"}),
			Entry("only version tags",
				[]string{"v4.18", "v4.19", "v4.20"},
				[]string{"v4.18", "v4.19", "v4.20"}),
			Entry("no version tags",
				[]string{"latest", "main", "sha256-abc"},
				[]string{}),
			Entry("empty input",
				[]string{},
				[]string{}),
			Entry("version with patch number excluded",
				[]string{"v4.18.1", "v4.18", "v4.19"},
				[]string{"v4.18", "v4.19"}),
			Entry("pre-release tags excluded",
				[]string{"v4.18-rc.1", "v4.18", "v4.19-beta"},
				[]string{"v4.18"}),
		)
	})

	Describe("ContainsTag", func() {
		DescribeTable("tag containment check",
			func(tags []string, target string, expected bool) {
				result := mcpserver.ContainsTag(tags, target)
				Expect(result).To(Equal(expected))
			},
			Entry("tag exists", []string{"v4.17", "v4.18", "v4.19"}, "v4.18", true),
			Entry("tag does not exist", []string{"v4.17", "v4.18", "v4.19"}, "v4.20", false),
			Entry("empty list", []string{}, "v4.18", false),
			Entry("case sensitive", []string{"V4.18"}, "v4.18", false),
		)
	})

	Describe("CompareVersionTags", func() {
		DescribeTable("version comparison",
			func(a, b string, expectedSign int) {
				result := mcpserver.CompareVersionTags(a, b)
				if expectedSign < 0 {
					Expect(result).To(BeNumerically("<", 0))
				} else if expectedSign > 0 {
					Expect(result).To(BeNumerically(">", 0))
				} else {
					Expect(result).To(Equal(0))
				}
			},
			Entry("a less than b (same major)", "v4.17", "v4.18", -1),
			Entry("a greater than b (same major)", "v4.19", "v4.18", 1),
			Entry("equal versions", "v4.18", "v4.18", 0),
			Entry("different major versions", "v3.11", "v4.18", -1),
		)
	})

	Describe("RDSConfigs", func() {
		It("has Core RDS config", func() {
			Expect(mcpserver.RDSTypeCore).To(Equal("core"))
		})

		It("has RAN RDS config", func() {
			Expect(mcpserver.RDSTypeRAN).To(Equal("ran"))
		})
	})

	Describe("ReferenceService.FindRDSReference", func() {
		var (
			ctrl         *gomock.Controller
			mockRegistry *MockRegistryClient
			mockCluster  *MockClusterClient
			mockFactory  *MockClusterClientFactory
			service      *mcpserver.ReferenceService
		)

		BeforeEach(func() {
			ctrl = gomock.NewController(GinkgoT())
			mockRegistry = NewMockRegistryClient(ctrl)
			mockCluster = NewMockClusterClient(ctrl)
			mockFactory = NewMockClusterClientFactory(ctrl)
			service = &mcpserver.ReferenceService{
				Registry:       mockRegistry,
				ClusterFactory: mockFactory,
			}
		})

		AfterEach(func() {
			ctrl.Finish()
		})

		Context("with explicit OCP version", func() {
			It("skips cluster version detection", func() {
				// Expect registry calls for finding RHEL variant
				mockRegistry.EXPECT().
					ListTags(gomock.Any(), gomock.Any()).
					Return([]string{"v4.17", "v4.18", "v4.19"}, nil).
					AnyTimes()
				mockRegistry.EXPECT().
					HeadImage(gomock.Any(), gomock.Any()).
					Return(nil).
					AnyTimes()

				args := &mcpserver.RDSReferenceArgs{
					RDSType:    mcpserver.RDSTypeCore,
					OCPVersion: "4.18.0",
				}

				result, err := service.FindRDSReference(context.Background(), args)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.ClusterVersion).To(Equal("4.18.0"))
				Expect(result.Reference).To(ContainSubstring("v4.18"))
				Expect(result.Validated).To(BeTrue())
			})
		})

		Context("with kubeconfig", func() {
			It("detects cluster version from API", func() {
				// Mock factory to return mock cluster client
				mockFactory.EXPECT().
					NewClient(gomock.Any()).
					Return(mockCluster, nil)
				mockCluster.EXPECT().
					GetClusterVersion(gomock.Any()).
					Return("4.20.0-rc.1", nil)
				mockRegistry.EXPECT().
					ListTags(gomock.Any(), gomock.Any()).
					Return([]string{"v4.18", "v4.19", "v4.20"}, nil).
					AnyTimes()
				mockRegistry.EXPECT().
					HeadImage(gomock.Any(), gomock.Any()).
					Return(nil).
					AnyTimes()

				args := &mcpserver.RDSReferenceArgs{
					RDSType:    mcpserver.RDSTypeCore,
					Kubeconfig: EncodeKubeconfig(ValidKubeconfig),
				}

				result, err := service.FindRDSReference(context.Background(), args)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.ClusterVersion).To(Equal("4.20.0-rc.1"))
				Expect(result.Reference).To(ContainSubstring("v4.20"))
			})
		})

		Context("when version not found in registry", func() {
			It("returns error with available versions", func() {
				mockRegistry.EXPECT().
					ListTags(gomock.Any(), gomock.Any()).
					Return([]string{"v4.16", "v4.17", "v4.18"}, nil).
					AnyTimes()

				args := &mcpserver.RDSReferenceArgs{
					RDSType:    mcpserver.RDSTypeCore,
					OCPVersion: "4.25.0",
				}

				_, err := service.FindRDSReference(context.Background(), args)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("not found"))
			})
		})

		Context("when registry fails", func() {
			It("returns registry error", func() {
				mockRegistry.EXPECT().
					ListTags(gomock.Any(), gomock.Any()).
					Return(nil, errors.New("UNAUTHORIZED")).
					AnyTimes()

				args := &mcpserver.RDSReferenceArgs{
					RDSType:    mcpserver.RDSTypeCore,
					OCPVersion: "4.18.0",
				}

				_, err := service.FindRDSReference(context.Background(), args)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("authentication"))
			})
		})

		Context("when image validation fails", func() {
			It("returns validation error", func() {
				mockRegistry.EXPECT().
					ListTags(gomock.Any(), gomock.Any()).
					Return([]string{"v4.17", "v4.18", "v4.19"}, nil).
					AnyTimes()
				mockRegistry.EXPECT().
					HeadImage(gomock.Any(), gomock.Any()).
					Return(errors.New("image not accessible")).
					AnyTimes()

				args := &mcpserver.RDSReferenceArgs{
					RDSType:    mcpserver.RDSTypeCore,
					OCPVersion: "4.18.0",
				}

				_, err := service.FindRDSReference(context.Background(), args)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("accessible"))
			})
		})
	})

	Describe("FindRDSReferenceTool", func() {
		var tool = mcpserver.FindRDSReferenceTool()

		It("has the correct name", func() {
			Expect(tool.Name).To(Equal("find_rds_reference"))
		})

		It("has a description", func() {
			Expect(tool.Description).NotTo(BeEmpty())
		})
	})

	Describe("GetRDSConfigs", func() {
		It("returns valid config for Core RDS", func() {
			ref := mcpserver.BuildRDSReference(mcpserver.RDSTypeCore, "rhel9", "v4.18")
			Expect(ref).To(ContainSubstring("telco-core-rds"))
		})

		It("returns valid config for RAN RDS", func() {
			ref := mcpserver.BuildRDSReference(mcpserver.RDSTypeRAN, "rhel8", "v4.18")
			Expect(ref).To(ContainSubstring("ztp-site-generate"))
		})
	})

	Describe("ParseRDSReferenceArgs", func() {
		DescribeTable("valid arguments",
			func(args map[string]interface{}, expectedType string) {
				result, err := mcpserver.ParseRDSReferenceArgs(args)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RDSType).To(Equal(expectedType))
			},
			Entry("core RDS type",
				map[string]interface{}{"rds_type": "core"},
				"core"),
			Entry("ran RDS type",
				map[string]interface{}{"rds_type": "ran"},
				"ran"),
			Entry("core RDS case insensitive",
				map[string]interface{}{"rds_type": "CORE"},
				"core"),
			Entry("ran RDS case insensitive",
				map[string]interface{}{"rds_type": "RAN"},
				"ran"),
			Entry("with kubeconfig",
				map[string]interface{}{
					"rds_type":   "core",
					"kubeconfig": EncodeKubeconfig(ValidKubeconfig),
				},
				"core"),
			Entry("with ocp_version",
				map[string]interface{}{
					"rds_type":    "core",
					"ocp_version": "4.18.0",
				},
				"core"),
		)

		DescribeTable("error cases",
			func(args map[string]interface{}, errContains string) {
				_, err := mcpserver.ParseRDSReferenceArgs(args)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring(errContains))
			},
			Entry("missing rds_type",
				map[string]interface{}{},
				"rds_type"),
			Entry("invalid rds_type",
				map[string]interface{}{"rds_type": "invalid"},
				"invalid"),
			Entry("rds_type wrong type",
				map[string]interface{}{"rds_type": 123},
				"rds_type"),
		)
	})

	Describe("RDSReferenceArgs struct", func() {
		It("can be created with all fields", func() {
			args := mcpserver.RDSReferenceArgs{
				Kubeconfig: "base64data",
				Context:    "my-context",
				RDSType:    "core",
				OCPVersion: "4.18.0",
			}
			Expect(args.Kubeconfig).To(Equal("base64data"))
			Expect(args.Context).To(Equal("my-context"))
			Expect(args.RDSType).To(Equal("core"))
			Expect(args.OCPVersion).To(Equal("4.18.0"))
		})
	})

	Describe("WrapRegistryError", func() {
		var ctrl *gomock.Controller

		BeforeEach(func() {
			ctrl = gomock.NewController(GinkgoT())
		})

		AfterEach(func() {
			ctrl.Finish()
		})

		Context("different error patterns", func() {
			It("handles unauthorized errors", func() {
				mockRegistry := NewMockRegistryClient(ctrl)
				mockFactory := NewMockClusterClientFactory(ctrl)
				service := &mcpserver.ReferenceService{
					Registry:       mockRegistry,
					ClusterFactory: mockFactory,
				}

				mockRegistry.EXPECT().
					ListTags(gomock.Any(), gomock.Any()).
					Return(nil, errors.New("UNAUTHORIZED: access denied")).
					AnyTimes()

				args := &mcpserver.RDSReferenceArgs{
					RDSType:    "core",
					OCPVersion: "4.18.0",
				}
				_, err := service.FindRDSReference(context.Background(), args)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("authentication"))
			})

			It("handles name unknown errors", func() {
				mockRegistry := NewMockRegistryClient(ctrl)
				mockFactory := NewMockClusterClientFactory(ctrl)
				service := &mcpserver.ReferenceService{
					Registry:       mockRegistry,
					ClusterFactory: mockFactory,
				}

				mockRegistry.EXPECT().
					ListTags(gomock.Any(), gomock.Any()).
					Return(nil, errors.New("NAME_UNKNOWN: repo not found")).
					AnyTimes()

				args := &mcpserver.RDSReferenceArgs{
					RDSType:    "core",
					OCPVersion: "4.18.0",
				}
				_, err := service.FindRDSReference(context.Background(), args)
				Expect(err).To(HaveOccurred())
			})

			It("handles generic network errors", func() {
				mockRegistry := NewMockRegistryClient(ctrl)
				mockFactory := NewMockClusterClientFactory(ctrl)
				service := &mcpserver.ReferenceService{
					Registry:       mockRegistry,
					ClusterFactory: mockFactory,
				}

				mockRegistry.EXPECT().
					ListTags(gomock.Any(), gomock.Any()).
					Return(nil, errors.New("connection timeout")).
					AnyTimes()

				args := &mcpserver.RDSReferenceArgs{
					RDSType:    "core",
					OCPVersion: "4.18.0",
				}
				_, err := service.FindRDSReference(context.Background(), args)
				Expect(err).To(HaveOccurred())
			})
		})
	})

	Describe("FindRDSReference with cluster version detection", func() {
		var ctrl *gomock.Controller

		BeforeEach(func() {
			ctrl = gomock.NewController(GinkgoT())
		})

		AfterEach(func() {
			ctrl.Finish()
		})

		It("detects version from kubeconfig", func() {
			mockRegistry := NewMockRegistryClient(ctrl)
			mockCluster := NewMockClusterClient(ctrl)
			mockFactory := NewMockClusterClientFactory(ctrl)
			service := &mcpserver.ReferenceService{
				Registry:       mockRegistry,
				ClusterFactory: mockFactory,
			}

			mockFactory.EXPECT().
				NewClient(gomock.Any()).
				Return(mockCluster, nil)
			mockCluster.EXPECT().
				GetClusterVersion(gomock.Any()).
				Return("4.19.0", nil)
			mockRegistry.EXPECT().
				ListTags(gomock.Any(), gomock.Any()).
				Return([]string{"v4.18", "v4.19", "v4.20"}, nil).
				AnyTimes()
			mockRegistry.EXPECT().
				HeadImage(gomock.Any(), gomock.Any()).
				Return(nil).
				AnyTimes()

			args := &mcpserver.RDSReferenceArgs{
				RDSType:    "core",
				Kubeconfig: EncodeKubeconfig(ValidKubeconfig),
			}
			result, err := service.FindRDSReference(context.Background(), args)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.ClusterVersion).To(Equal("4.19.0"))
			Expect(result.Reference).To(ContainSubstring("v4.19"))
		})
	})
})
