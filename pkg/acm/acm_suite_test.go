// SPDX-License-Identifier: Apache-2.0

package acm_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestACM(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ACM Suite")
}
