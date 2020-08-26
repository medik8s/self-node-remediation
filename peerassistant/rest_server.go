package peerassistant

import (
	"fmt"
	"github.com/gorilla/mux"
	"log"
	"net/http"
)

const (
//port = 30001
)

func healthCheck(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	w.WriteHeader(http.StatusOK)
	machineName := vars["machineName"]
	healthResult := isHealthy(machineName)
	_, _ = fmt.Fprintf(w, "%v", healthResult) //todo log error
}

func handleRequests() {
	r := mux.NewRouter()
	r.HandleFunc("/health/{machineName}", healthCheck)
	http.Handle("/", r)
	log.Fatal(http.ListenAndServe(":30001", nil))
}

func Start() {
	handleRequests()
}
