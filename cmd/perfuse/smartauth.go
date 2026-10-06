package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/biodream-llc/perfuse/internal/fhir"
	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/smartauth"
	"github.com/biodream-llc/perfuse/internal/store"
)

// builtinAuthServer is the authorization server for -smart-clients, at /auth beside the FHIR base it issues tokens for. With
// -smart-users, people sign in there to authorize apps, and clinicians pick patients from this FHIR store.
func builtinAuthServer(clientsFile, usersFile, keyFile, fhirBase string, fhirStore *fhirserver.Store) (*smartauth.Server, error) {
	clients, err := smartauth.LoadClients(clientsFile)
	if err != nil {
		return nil, err
	}
	key, err := smartauth.LoadOrCreateKey(keyFile)
	if err != nil {
		return nil, err
	}
	issuer := strings.TrimSuffix(strings.TrimRight(fhirBase, "/"), "/fhir") + "/auth"
	as := &smartauth.Server{Issuer: issuer, Audience: fhirBase, Key: key, Clients: clients}
	if usersFile != "" {
		if as.Users, err = smartauth.LoadUsers(usersFile); err != nil {
			return nil, err
		}
		as.Patients = func(ctx context.Context, search string) ([]smartauth.PatientChoice, error) {
			params := map[string][]string{"_count": {"50"}}
			if s := strings.TrimSpace(search); s != "" {
				params["name"] = []string{s}
			}
			q, err := fhirserver.ParseSearch("Patient", params)
			if err != nil {
				return nil, err
			}
			res, err := fhirStore.Search(ctx, q)
			if err != nil {
				return nil, err
			}
			var out []smartauth.PatientChoice
			for _, r := range res.Resources {
				out = append(out, patientChoice(r))
			}
			return out, nil
		}
		as.PatientExists = func(ctx context.Context, id string) bool {
			_, err := fhirStore.Get(ctx, "Patient", id)
			return err == nil
		}
	}
	return as, nil
}

func patientChoice(r fhir.Resource) smartauth.PatientChoice {
	raw, _ := json.Marshal(r)
	var p struct {
		Name []struct {
			Text   string   `json:"text"`
			Family string   `json:"family"`
			Given  []string `json:"given"`
		} `json:"name"`
		BirthDate string `json:"birthDate"`
	}
	_ = json.Unmarshal(raw, &p)
	c := smartauth.PatientChoice{ID: r.ResourceID(), BirthDate: p.BirthDate}
	if len(p.Name) > 0 {
		n := p.Name[0]
		c.Name = n.Text
		if c.Name == "" {
			c.Name = strings.TrimSpace(strings.Join(n.Given, " ") + " " + n.Family)
		}
	}
	if c.Name == "" {
		c.Name = "(no name)"
	}
	return c
}

// cmdSmart is perfuse smart hash: it reads a password or client secret on standard input and prints the hash a SMART users or
// clients file holds, so the secret itself is never written down.
func cmdSmart(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) != 1 || args[0] != "hash" {
		return errors.New("usage: perfuse smart hash < secret   (prints the password_hash or secret_hash for a SMART users or clients file)")
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	secret := strings.TrimRight(line, "\r\n")
	if len(secret) < 8 {
		return errors.New("give a secret of at least 8 characters on standard input")
	}
	h, err := store.HashPassword(secret)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, h)
	return nil
}
