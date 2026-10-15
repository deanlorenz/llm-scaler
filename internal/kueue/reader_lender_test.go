package kueue

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
)

// The lender signal, read end to end: team-a borrows past its nominal 9,
// team-b lends and reports pending Workloads in its status.
var _ = Describe("Reader reads a lender as waiting from its pending Workloads", func() {
	const v = "v1beta2"
	workload := func(ns, name string, priority int64, requests map[string]interface{}, admittedTo string) *unstructured.Unstructured {
		w := obj(v, KindWorkload, ns, name, map[string]interface{}{})
		spec := podSetsRequesting(requests)
		spec["queueName"] = "lq"
		spec["priority"] = priority
		w.Object["spec"] = spec
		if admittedTo != "" {
			w.Object["status"] = map[string]interface{}{
				"admission":  map[string]interface{}{"clusterQueue": admittedTo},
				"conditions": []interface{}{map[string]interface{}{"type": "QuotaReserved", "status": "True"}},
			}
		}
		return w
	}
	// snap is the last read's snapshot.
	var snap config.ExternalQuotas
	read := func(lenderPolicy string, extra ...client.Object) config.ExternalLender {
		objs := docsExample(v, "cohortName")
		a, b := objs[1].(*unstructured.Unstructured), objs[2].(*unstructured.Unstructured)
		// team-a holds 10 of nominal 9: it borrows 1.
		res := a.Object["status"].(map[string]interface{})["flavorsReservation"].([]interface{})[0].(map[string]interface{})["resources"].([]interface{})[0].(map[string]interface{})
		res["total"], res["borrowed"] = "10", "1"
		// team-b reports pending Workloads, as Kueue counts any.
		b.Object["status"].(map[string]interface{})["pendingWorkloads"] = int64(1)
		if lenderPolicy != "" {
			Expect(unstructured.SetNestedField(b.Object, lenderPolicy, "spec", "preemption", "reclaimWithinCohort")).To(Succeed())
		}
		m := cohortMapper(v).(*meta.DefaultRESTMapper)
		m.Add(schema.GroupVersionKind{Group: Group, Version: v, Kind: KindWorkload}, meta.RESTScopeNamespace)
		c := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).WithRESTMapper(m).
			WithObjects(append(objs, extra...)...).Build()
		var err error
		snap, err = NewReader(c, Options{}).Quotas(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(snap.SharedPools).To(HaveLen(1))
		for _, l := range snap.SharedPools[0].Lenders {
			if l.Queue == "team-b-cq" {
				return l
			}
		}
		Fail("no team-b lender")
		return config.ExternalLender{}
	}
	gpus := func(n string) map[string]interface{} { return map[string]interface{}{"nvidia.com/gpu": n} }
	cpu := map[string]interface{}{"cpu": "4"}

	It("does not wait for a pending CPU-only Workload, whatever Kueue's pending count says", func() {
		Expect(read("", workload("team-b", "batch", 0, cpu, "")).Waiting).To(BeFalse())
	})

	It("does not wait for a pending GPU Workload larger than its unused nominal", func() {
		Expect(read("", workload("team-b", "huge", 0, gpus("13"), "")).Waiting).To(BeFalse())
	})

	It("waits for a pending GPU Workload that fits, queued through its LocalQueue", func() {
		Expect(read("", workload("team-b", "train", 0, gpus("4"), "")).Waiting).To(BeTrue())
		Expect(read("", workload("elsewhere", "train", 0, gpus("4"), "")).Waiting).To(BeFalse(),
			"a namespace without a LocalQueue on the lender queues nothing there")
	})

	It("does not count a Workload Kueue already reserved quota for", func() {
		Expect(read("", workload("team-b", "running", 0, gpus("4"), "team-b-cq")).Waiting).To(BeFalse())
	})

	It("carries a LowerPriority lender's policy and its highest Workload priority", func() {
		borrower := workload("team-a", "serve", 100, gpus("10"), "team-a-cq")
		l := read("LowerPriority", workload("team-b", "train", 1000, gpus("4"), ""), borrower)
		Expect(l.Waiting).To(BeTrue())
		Expect(l.Reclaim).To(Equal("LowerPriority"))
		Expect(l.CanReclaim).To(BeFalse(), "only Any reclaims whatever the borrower runs")
		Expect(l.Priority).To(HaveValue(Equal(int32(1000))))
	})

	It("reads each namespace's lowest Workload priority, reserved or pending with a GPU request", func() {
		read("",
			workload("team-a", "serve", 100, gpus("8"), "team-a-cq"),
			workload("team-a", "canary", 40, gpus("2"), "team-a-cq"),
			workload("team-a", "cpu-job", 1, cpu, ""),
			workload("team-b", "train", 1000, gpus("4"), ""))
		Expect(snap.NamespaceLowestPriority).To(Equal(map[string]int32{"team-a": 40, "team-b": 1000}),
			"a pending CPU-only Workload is no GPU borrower")
	})
})
