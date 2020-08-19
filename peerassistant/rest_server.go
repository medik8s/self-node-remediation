package peerassistant

import (
	"github.com/gorilla/mux"
	"log"
	"net/http"
)

const (
	port = 30001
)

func healthCheck(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	w.WriteHeader(http.StatusOK)
	machineName := vars["machineName"]
	isHealthy(machineName)
}

func handleRequests() {
	r := mux.NewRouter()
	r.HandleFunc("/health/{machineName}", healthCheck)
	log.Fatal(http.ListenAndServe(":"+string(port), nil))
}

func Start() {
	handleRequests()
}
