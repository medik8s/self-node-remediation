module github.com/n1r1/poison-pill

go 1.13

require (
	github.com/go-logr/logr v0.1.0
	github.com/gorilla/mux v1.7.4
	github.com/onsi/ginkgo v1.12.1
	github.com/onsi/gomega v1.10.1
	github.com/openshift/machine-api-operator v0.2.1-0.20200721125631-d234cceb5de1
	github.com/prometheus/common v0.7.0
	k8s.io/api v0.18.6

	k8s.io/apimachinery v0.18.8
	k8s.io/client-go v0.18.6
	sigs.k8s.io/controller-runtime v0.6.2
)

replace (
	sigs.k8s.io/cluster-api-provider-aws => github.com/openshift/cluster-api-provider-aws v0.2.1-0.20200520125206-5e266b553d8e
	sigs.k8s.io/cluster-api-provider-azure => github.com/openshift/cluster-api-provider-azure v0.1.0-alpha.3.0.20200529030741-17d4edc5142f
)
