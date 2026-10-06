package main

import (
	"strings"

	"github.com/biodream-llc/perfuse/internal/smartauth"
)

// builtinAuthServer is the authorization server for -smart-clients, at /auth beside the FHIR base it issues tokens for.
func builtinAuthServer(clientsFile, keyFile, fhirBase string) (*smartauth.Server, error) {
	clients, err := smartauth.LoadClients(clientsFile)
	if err != nil {
		return nil, err
	}
	key, err := smartauth.LoadOrCreateKey(keyFile)
	if err != nil {
		return nil, err
	}
	issuer := strings.TrimSuffix(strings.TrimRight(fhirBase, "/"), "/fhir") + "/auth"
	return &smartauth.Server{Issuer: issuer, Audience: fhirBase, Key: key, Clients: clients}, nil
}
