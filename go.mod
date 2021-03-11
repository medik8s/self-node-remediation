module github.com/n1r1/poison-pill

go 1.13

require (
	github.com/go-logr/logr v0.3.0
	github.com/gorilla/mux v1.7.4
	github.com/jessevdk/go-flags v1.4.0 // indirect
	github.com/onsi/ginkgo v1.14.1
	github.com/onsi/gomega v1.10.2
	github.com/openshift/machine-api-operator v0.2.1-0.20210104142355-8e6ae0acdfcf
	golang.org/x/sys v0.0.0-20201112073958-5cba982894dd
	k8s.io/api v0.20.0
	k8s.io/apimachinery v0.20.0
	k8s.io/client-go v0.20.0
	sigs.k8s.io/controller-runtime v0.7.0
)

replace (
	sigs.k8s.io/cluster-api-provider-aws => github.com/openshift/cluster-api-provider-aws v0.2.1-0.20201216171336-0b00fb8d96ac
	sigs.k8s.io/cluster-api-provider-azure => github.com/openshift/cluster-api-provider-azure v0.1.0-alpha.3.0.20201209184807-075372e2ed03
)
