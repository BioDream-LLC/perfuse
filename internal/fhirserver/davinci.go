package fhirserver

import (
	"net/http"

	"github.com/biodream-llc/perfuse/internal/davinci"
)

// daVinciVersion is the Da Vinci release to answer a DTR or PAS request in: the version on a profile the input claims, then
// the calling client's registration, then this server's setting, then the newest (package davinci says why it matters).
func (s *Server) daVinciVersion(r *http.Request, input any) string {
	var client string
	if c := CallerFrom(r.Context()); c != nil && c.ClientID != "" && s.ClientDaVinci != nil {
		client = s.ClientDaVinci(c.ClientID)
	}
	return davinci.Pick(davinci.FromProfiles(davinci.ProfilesIn(input)...), client, s.DaVinciVersion)
}
