package slo

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSLO(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SLO Analyzer Suite")
}
