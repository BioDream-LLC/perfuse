package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/fhir"
	"github.com/biodream-llc/perfuse/internal/api"
	"github.com/biodream-llc/perfuse/internal/epa"
	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/formulary"
	"github.com/biodream-llc/perfuse/internal/rtpb"

	_ "modernc.org/sqlite"
)

// drugSetup is what -formulary needs from the rest of serve.
type drugSetup struct {
	path, payerID, replyChannel string
	publicBase                  string // this server's public address, without a path
	db                          *sql.DB
	fhirSrv                     *fhirserver.Server // nil without -fhir
	fhirStore                   *fhirserver.Store
	runtime                     *api.Runtime
	log                         *slog.Logger
}

// setupDrugs loads the formulary and wires its three uses: the public Da Vinci US Drug Formulary API at /formulary/fhir,
// Real-Time Prescription Benefit at /ncpdp/rtpb, and NCPDP SCRIPT electronic prior authorization at /ncpdp/script, which
// decides through PAS when -pas is on. It returns a closer for the formulary's own store.
func setupDrugs(mux *http.ServeMux, d drugSetup) (*api.Drugs, func(), error) {
	f, err := os.Open(d.path)
	if err != nil {
		return nil, nil, fmt.Errorf("-formulary: %w", err)
	}
	file, err := formulary.Load(f)
	f.Close()
	if err != nil {
		return nil, nil, fmt.Errorf("-formulary %s: %w", d.path, err)
	}
	if strings.TrimSpace(d.payerID) != "" {
		file.Payer.ID = strings.TrimSpace(d.payerID)
	}

	// The Drug Formulary API is public, as CMS requires of a payer's formulary: anyone may read it with no credential. It is
	// served from a store of its own, in memory, holding the formulary and nothing else, so being open cannot reach a
	// patient's record however a query is written.
	mem, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, nil, err
	}
	mem.SetMaxOpenConns(1)
	closer := func() { mem.Close() }
	store, err := fhirserver.NewStore(mem, fhir.R4)
	if err != nil {
		closer()
		return nil, nil, err
	}
	updated := time.Now().UTC().Format(time.RFC3339)
	for _, r := range file.Resources(updated) {
		raw, _ := json.Marshal(r)
		res, err := fhir.UnmarshalResource(raw)
		if err != nil {
			closer()
			return nil, nil, fmt.Errorf("-formulary: publishing %s/%s: %w", r["resourceType"], r["id"], err)
		}
		if _, err := store.Put(context.Background(), res); err != nil {
			closer()
			return nil, nil, fmt.Errorf("-formulary: publishing %s/%s: %w", r["resourceType"], r["id"], err)
		}
	}
	base := strings.TrimRight(d.publicBase, "/") + "/formulary/fhir"
	pub := fhirserver.NewServer(store, base, d.log)
	pub.Auth = fhirserver.OpenAuth{}
	pub.ReadOnly = true
	inner := http.StripPrefix("/formulary/fhir", pub.Handler())
	mux.Handle("/formulary/fhir/", inner)
	mux.Handle("/formulary/fhir", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path, r2.URL.RawPath = "/formulary/fhir/", ""
		inner.ServeHTTP(w, r2)
	}))

	drugs := &api.Drugs{Formulary: file, FormularyBase: base, ReplyChannel: d.replyChannel}

	// A member's plan, from the FHIR store's Coverage: subscriberId is the member ID, and the class of type plan names it.
	if d.fhirStore != nil {
		drugs.RTPB.Member = coveragePlan(d.fhirStore)
	}

	if d.fhirSrv != nil {
		svc := &epa.Service{Formulary: file, DB: d.db, PayerID: file.Payer.ID, Member: drugs.RTPB.Member, Log: d.log,
			Questionnaire: func(ctx context.Context, url string) map[string]any {
				return d.fhirSrv.ByCanonical(ctx, "Questionnaire", url)
			}}
		if d.fhirSrv.PAS != nil {
			svc.Submit = d.fhirSrv.SubmitPAS
			svc.Review = func(ctx context.Context, id, decision, reason string) error {
				_, err := d.fhirSrv.Review(ctx, id, fhirserver.PASReview{Decision: decision, Reason: reason})
				return err
			}
			previous := d.fhirSrv.PAS.Decided
			d.fhirSrv.PAS.Decided = func(ctx context.Context, id string, response map[string]any) {
				if previous != nil {
					previous(ctx, id, response)
				}
				svc.Decided(ctx, id, response)
			}
		} else {
			d.log.Warn("electronic prior authorization answers initiations but cannot decide requests without PAS",
				"fix", "pass -pas as well")
		}
		if d.replyChannel != "" {
			if d.runtime == nil {
				closer()
				return nil, nil, fmt.Errorf("-epa-reply-channel routes ePA decisions to a channel, and this server runs none")
			}
			rt, channel := d.runtime, d.replyChannel
			svc.Send = func(ctx context.Context, b []byte) error { return rt.Route(ctx, channel, b) }
		}
		drugs.EPA = svc
		drugs.RTPB.PAOnFile = svc.PAOnFile
	} else if d.replyChannel != "" {
		closer()
		return nil, nil, fmt.Errorf("-epa-reply-channel needs electronic prior authorization, which needs -fhir")
	}
	_ = rtpb.Version
	return drugs, closer, nil
}

// coveragePlan finds a member's plan id in the FHIR store: a Coverage whose subscriberId is the member ID, and its class of
// type plan.
func coveragePlan(store *fhirserver.Store) func(string) string {
	return func(member string) string {
		q, err := fhirserver.ParseSearch("Coverage", map[string][]string{"subscriber-id": {member}})
		if err != nil {
			return ""
		}
		res, err := store.Search(context.Background(), q)
		if err != nil {
			return ""
		}
		for _, r := range res.Resources {
			c, ok := r.(*fhir.Coverage)
			if !ok || (c.Status != "" && c.Status != "active") {
				continue
			}
			for _, cl := range c.Class {
				if cl.Type == nil {
					continue
				}
				for _, coding := range cl.Type.Coding {
					if coding.Code == "plan" {
						return cl.Value
					}
				}
			}
		}
		return ""
	}
}
