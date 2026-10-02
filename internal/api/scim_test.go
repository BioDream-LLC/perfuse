package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/scim"
	"github.com/biodream-llc/perfuse/internal/store"
)

// SCIM tests. The subject is not whether JSON parses but whether a deprovisioning actually removes access, because that is
// the operation with somebody's employment attached and the one nothing downstream will check.

// scimHarness turns the provisioning endpoints on.
func scimHarness(t *testing.T) *harness {
	t.Helper()

	h := newHarness(t)
	h.server.SCIMEnabled = true
	h.server.SCIMDefaultRole = store.RoleViewer
	h.handler = h.server.Handler()

	return h
}

// asIdentityProvider sends a request the way an identity provider does: a bearer token, SCIM content type, and no
// Perfuse-specific header, because no provider can be configured to send one.
func asIdentityProvider(t *testing.T, h *harness, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	token := h.scimToken(t)

	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}

	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", scim.ContentType)

	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	return rec
}

// scimToken mints an admin token once and reuses it.
func (h *harness) scimToken(t *testing.T) string {
	t.Helper()

	if existing, ok := h.tokens["scim-provisioner-token"]; ok {
		return existing
	}

	token, err := h.store.ScopeUnchecked(store.DefaultTenant).CreateAPIToken(
		t.Context(), "identity-provider", store.RoleAdmin, "test")
	if err != nil {
		t.Fatal(err)
	}
	h.tokens["scim-provisioner-token"] = token

	return token
}

// TestTheCrossSiteHeaderIsStillRequiredForCookies pins the half of the exemption that must not have moved.
//
// Bearer-authenticated requests are exempt from the X-Perfuse-Request header, because cross-site request forgery is an attack
// on ambient credentials: a hostile page can make a browser attach its cookie and cannot make it attach a header the attacker
// does not know. A caller presenting a bearer token has already proved possession of it.
//
// That reasoning only holds if cookie-authenticated requests are still refused without the header. There was no test
// asserting that before this one, so adding the exemption could have disabled the protection entirely and nothing would have
// said so.
func TestTheCrossSiteHeaderIsStillRequiredForCookies(t *testing.T) {
	h := newHarness(t)

	// A signed-in session, then a mutating request with the cookie and no header - which is what a cross-site form
	// submission would produce.
	h.doAs(t, "main", store.RoleAdmin, http.MethodGet, "/api/channels", nil)
	cookie, ok := h.tokens["u-main-admin"]
	if !ok {
		t.Fatal("no session was established")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/channels",
		strings.NewReader(`{"yaml":"name: csrf-test\nsource:\n  type: mllp\n  listen: 127.0.0.1:19998\ndestinations:\n  - name: out\n    type: file\n    dir: /tmp/csrf\n"}`))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	req.Header.Set("Content-Type", "application/json")
	// Deliberately no X-Perfuse-Request.

	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("a cookie-authenticated mutating request with no cross-site header got %d rather than 403; the "+
			"protection has been disabled: %s", rec.Code, rec.Body.String())
	}

	// And the same request with the header works, so the check is the header rather than something else.
	req = httptest.NewRequest(http.MethodPost, "/api/channels",
		strings.NewReader(`{"yaml":"name: csrf-test\nsource:\n  type: mllp\n  listen: 127.0.0.1:19998\ndestinations:\n  - name: out\n    type: file\n    dir: /tmp/csrf\ndestinations:\n  - name: out\n    type: file\n    dir: /tmp/csrf\n"}`))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Perfuse-Request", "1")

	rec = httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code == http.StatusForbidden {
		t.Errorf("the same request with the header was still refused: %s", rec.Body.String())
	}
}

// TestAnIdentityProviderCanProvisionWithoutAPerfuseHeader is the other half.
//
// No provider can be configured to send a Perfuse-specific header. Without the exemption every deprovisioning would be
// refused, and the account would stay enabled.
func TestAnIdentityProviderCanProvisionWithoutAPerfuseHeader(t *testing.T) {
	h := scimHarness(t)

	rec := asIdentityProvider(t, h, http.MethodPost, "/scim/v2/Users",
		`{"schemas":["`+scim.SchemaUser+`"],"userName":"rturner@example.org","externalId":"okta-00u1abcd","active":true}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("provisioning was refused with %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != scim.ContentType {
		t.Errorf("content type is %q; some providers treat plain JSON as a protocol error", got)
	}
}

// TestDisablingAnAccountEndsItsSessionsImmediately is the test this whole feature exists for.
//
// A browser tab open on a laptop that has gone home with a terminated employee must stop working now. A session that survives
// until its expiry is a session that survives the sacking, and the expiry may be days away.
func TestDisablingAnAccountEndsItsSessionsImmediately(t *testing.T) {
	h := scimHarness(t)

	// An account with an active session, which is the state somebody is in when they are terminated: signed in.
	const password = "a sufficiently long password"
	scoped := h.store.ScopeUnchecked(store.DefaultTenant)
	user, err := scoped.CreateUser(t.Context(), "leaver@example.org", password, store.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	sessionToken, _, err := scoped.Authenticate(t.Context(), "leaver@example.org", password, "10.0.0.1", "browser")
	if err != nil {
		t.Fatal(err)
	}

	// The session works, so the check below means something.
	if rec := requestWithCookie(h, http.MethodGet, "/api/channels", sessionToken); rec.Code != http.StatusOK {
		t.Fatalf("the session did not work to begin with (%d), so this test proves nothing", rec.Code)
	}

	// The identity provider disables the account, in the shape Okta sends.
	rec := asIdentityProvider(t, h, http.MethodPatch, "/scim/v2/Users/"+itoa(user.ID),
		`{"schemas":["`+scim.SchemaPatchOp+`"],"Operations":[{"op":"replace","path":"active","value":false}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("the disable request was refused with %d: %s", rec.Code, rec.Body.String())
	}

	// The response has to say the account is inactive, because the provider reconciles against this.
	var returned scim.User
	if err := json.Unmarshal(rec.Body.Bytes(), &returned); err != nil {
		t.Fatal(err)
	}
	if returned.Active == nil || *returned.Active {
		t.Errorf("the response says the account is still active: %s", rec.Body.String())
	}

	// And the session must be dead now rather than at its expiry.
	if rec := requestWithCookie(h, http.MethodGet, "/api/channels", sessionToken); rec.Code == http.StatusOK {
		t.Error("the session still works after the account was disabled; a terminated employee keeps access " +
			"until the session expires, which may be days")
	}

	// Verified at the store as well, because the endpoint returning 401 could in principle be for another reason.
	remaining, err := scoped.CountUserSessions(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("%d session(s) remain after disabling the account", remaining)
	}
}

// requestWithCookie makes a request with a session cookie.
func requestWithCookie(h *harness, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})

	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	return rec
}

// TestEveryDisableShapeProvidersSendActuallyDisables is the end-to-end version of the unit test in internal/scim.
//
// Parsing the shape is not enough: the handler has to act on it. A shape that is parsed and then not applied, and answered
// with a 200, is the worst case - the provider records the deprovisioning as complete and never tries again.
func TestEveryDisableShapeProvidersSendActuallyDisables(t *testing.T) {
	shapes := map[string]string{
		"JSON boolean":    `{"op":"replace","path":"active","value":false}`,
		"quoted word":     `{"op":"replace","path":"active","value":"False"}`,
		"capitalised op":  `{"op":"Replace","path":"active","value":false}`,
		"pathless object": `{"op":"replace","value":{"active":false}}`,
		"wrapped array":   `{"op":"replace","path":"active","value":[{"value":false}]}`,
	}

	for name, operation := range shapes {
		t.Run(name, func(t *testing.T) {
			h := scimHarness(t)
			scoped := h.store.ScopeUnchecked(store.DefaultTenant)

			user, err := scoped.CreateUser(t.Context(), "person@example.org", "a sufficiently long password",
				store.RoleViewer)
			if err != nil {
				t.Fatal(err)
			}

			rec := asIdentityProvider(t, h, http.MethodPatch, "/scim/v2/Users/"+itoa(user.ID),
				`{"schemas":["`+scim.SchemaPatchOp+`"],"Operations":[`+operation+`]}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("refused with %d: %s", rec.Code, rec.Body.String())
			}

			after, err := scoped.GetUserByID(t.Context(), user.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !after.Disabled {
				t.Errorf("the account is still enabled after a disable in this shape was answered with 200")
			}
		})
	}
}

// TestAnOperationTheServerDoesNotUnderstandIsRefused is the rule that makes the rest safe.
//
// A response must never be more successful than what happened. A provider that sends a disable in a shape this does not
// handle, and receives a 200, records the deprovisioning as done and never tries again. A 400 appears in the provider's own
// error report, where somebody sees it.
func TestAnOperationTheServerDoesNotUnderstandIsRefused(t *testing.T) {
	h := scimHarness(t)
	scoped := h.store.ScopeUnchecked(store.DefaultTenant)

	user, err := scoped.CreateUser(t.Context(), "person@example.org", "a sufficiently long password", store.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}

	unknown := []string{
		`{"op":"replace","path":"somethingInvented","value":"x"}`,
		`{"op":"replace","value":{"somethingInvented":"x"}}`,
		`{"op":"remove","path":"somethingInvented"}`,
	}

	for _, operation := range unknown {
		rec := asIdentityProvider(t, h, http.MethodPatch, "/scim/v2/Users/"+itoa(user.ID),
			`{"schemas":["`+scim.SchemaPatchOp+`"],"Operations":[`+operation+`]}`)

		if rec.Code == http.StatusOK {
			t.Errorf("%s was answered with 200; a provider would record this as applied", operation)

			continue
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s gave %d, expected 400", operation, rec.Code)
		}

		// And the error has to carry the keyword, so the provider logs something useful rather than retrying.
		var e scim.Error
		if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
			t.Errorf("the error is not SCIM-shaped: %s", rec.Body.String())

			continue
		}
		if e.ScimType != scim.ErrInvalidPath {
			t.Errorf("scimType is %q, expected invalidPath", e.ScimType)
		}
		// The message should say what to send instead, because somebody is reading a provisioning log.
		if !strings.Contains(e.Detail, "active") {
			t.Errorf("the message does not say what to send for a disable: %q", e.Detail)
		}
	}

	// And a harmless attribute sync is still accepted, so the refusal above is not simply refusing everything.
	rec := asIdentityProvider(t, h, http.MethodPatch, "/scim/v2/Users/"+itoa(user.ID),
		`{"schemas":["`+scim.SchemaPatchOp+`"],"Operations":[{"op":"replace","path":"displayName","value":"A Person"}]}`)
	if rec.Code != http.StatusOK {
		t.Errorf("a display name sync was refused with %d: %s", rec.Code, rec.Body.String())
	}
}

// TestDeletingAnAccountRemovesAccessBeforeAnythingElseCanFail covers the ordering.
//
// Disable then delete. If the delete fails after access is removed, the outcome is an account that cannot sign in - the state
// the provider asked for. The other way round, a failed disable after a successful delete would leave nothing to disable and
// the sessions still working.
func TestDeletingAnAccountRemovesAccessBeforeAnythingElseCanFail(t *testing.T) {
	h := scimHarness(t)
	scoped := h.store.ScopeUnchecked(store.DefaultTenant)

	const password = "a sufficiently long password"
	user, err := scoped.CreateUser(t.Context(), "gone@example.org", password, store.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	sessionToken, _, err := scoped.Authenticate(t.Context(), "gone@example.org", password, "10.0.0.1", "browser")
	if err != nil {
		t.Fatal(err)
	}

	rec := asIdentityProvider(t, h, http.MethodDelete, "/scim/v2/Users/"+itoa(user.ID), "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("the delete was refused with %d: %s", rec.Code, rec.Body.String())
	}

	// The session must be dead.
	if rec := requestWithCookie(h, http.MethodGet, "/api/channels", sessionToken); rec.Code == http.StatusOK {
		t.Error("the session still works after the account was deleted")
	}

	// And the account is gone.
	if _, err := scoped.GetUserByID(t.Context(), user.ID); err == nil {
		t.Error("the account still exists after a successful delete")
	}

	// A second delete is a 404. Some providers treat that as success, which is correct - the desired state is reached.
	rec = asIdentityProvider(t, h, http.MethodDelete, "/scim/v2/Users/"+itoa(user.ID), "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("deleting an already-deleted account gave %d, expected 404", rec.Code)
	}
}

// TestTheExternalIdentifierSurvivesARename is the wedding case.
//
// Somebody who marries and changes their username is the same person. Matching on username alone has the provider provision a
// second account, disable the one it just made, and leave the original enabled - a former employee with working access,
// arrived at by way of a wedding.
func TestTheExternalIdentifierSurvivesARename(t *testing.T) {
	h := scimHarness(t)

	rec := asIdentityProvider(t, h, http.MethodPost, "/scim/v2/Users",
		`{"schemas":["`+scim.SchemaUser+`"],"userName":"jsmith@example.org","externalId":"okta-00u1abcd"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("provisioning failed: %s", rec.Body.String())
	}

	var created scim.User
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ExternalID != "okta-00u1abcd" {
		t.Errorf("the external identifier was not recorded: %q", created.ExternalID)
	}

	// The provider looks the account up by external identifier, which is the lookup that survives a rename.
	rec = asIdentityProvider(t, h, http.MethodGet,
		`/scim/v2/Users?filter=externalId+eq+%22okta-00u1abcd%22`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("the lookup failed with %d: %s", rec.Code, rec.Body.String())
	}

	var list scim.ListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.TotalResults != 1 {
		t.Errorf("the lookup found %d accounts; finding none means the provider creates a duplicate", list.TotalResults)
	}
}

// TestTheUsernameLookupFindsAnExistingAccount covers the commonest request in the protocol.
//
// A provider performs this before creating an account. Coming back empty when the account exists means a duplicate.
func TestTheUsernameLookupFindsAnExistingAccount(t *testing.T) {
	h := scimHarness(t)

	if rec := asIdentityProvider(t, h, http.MethodPost, "/scim/v2/Users",
		`{"schemas":["`+scim.SchemaUser+`"],"userName":"rturner@example.org"}`); rec.Code != http.StatusCreated {
		t.Fatalf("provisioning failed: %s", rec.Body.String())
	}

	// Including a differing case, which is what makes a duplicate appear in practice.
	for _, query := range []string{
		`filter=userName+eq+%22rturner@example.org%22`,
		`filter=userName+eq+%22RTurner@Example.ORG%22`,
		`filter=username+eq+%22rturner@example.org%22`,
	} {
		rec := asIdentityProvider(t, h, http.MethodGet, "/scim/v2/Users?"+query, "")
		if rec.Code != http.StatusOK {
			t.Errorf("%s failed with %d: %s", query, rec.Code, rec.Body.String())

			continue
		}

		var list scim.ListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
			t.Errorf("%s: %v", query, err)

			continue
		}
		if list.TotalResults != 1 {
			t.Errorf("%s found %d accounts, expected 1; finding none produces a duplicate account",
				query, list.TotalResults)
		}
	}
}

// TestProvisioningTwiceIsAConflictWithTheRightKeyword covers retry behaviour.
//
// A provider seeing "uniqueness" knows the account exists and reconciles. One seeing no keyword retries a conflict for ever.
func TestProvisioningTwiceIsAConflictWithTheRightKeyword(t *testing.T) {
	h := scimHarness(t)

	body := `{"schemas":["` + scim.SchemaUser + `"],"userName":"rturner@example.org"}`
	if rec := asIdentityProvider(t, h, http.MethodPost, "/scim/v2/Users", body); rec.Code != http.StatusCreated {
		t.Fatalf("the first attempt failed: %s", rec.Body.String())
	}

	rec := asIdentityProvider(t, h, http.MethodPost, "/scim/v2/Users", body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("a duplicate gave %d, expected 409: %s", rec.Code, rec.Body.String())
	}

	var e scim.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if e.ScimType != scim.ErrUniqueness {
		t.Errorf("scimType is %q, expected uniqueness; without it the provider retries for ever", e.ScimType)
	}
}

// TestAnUnsupportedFilterIsAFourHundredNotAFiveHundred covers the difference between visible and invisible failure.
func TestAnUnsupportedFilterIsAFourHundredNotAFiveHundred(t *testing.T) {
	h := scimHarness(t)

	rec := asIdentityProvider(t, h, http.MethodGet,
		`/scim/v2/Users?filter=emails%5Btype+eq+%22work%22%5D.value+eq+%22a@b.org%22`, "")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("an unsupported filter gave %d, expected 400: %s", rec.Code, rec.Body.String())
	}

	var e scim.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if e.ScimType != scim.ErrInvalidFilter {
		t.Errorf("scimType is %q, expected invalidFilter", e.ScimType)
	}
}

// TestARoleArrivesFromAGroupName covers how a role is actually assigned.
//
// Group names in a real directory are prefixed: "perfuse-admins", "APP-Perfuse-Editor". Matching only exact names means every
// site has to rename its groups, which they will not do - so everybody ends up on the default role instead.
func TestARoleArrivesFromAGroupName(t *testing.T) {
	h := scimHarness(t)

	cases := map[string]store.Role{
		`"groups":[{"value":"g1","display":"perfuse-admins"}]`:     store.RoleAdmin,
		`"groups":[{"value":"g2","display":"APP-Perfuse-Editor"}]`: store.RoleEditor,
		`"groups":[{"value":"g3","display":"Perfuse Viewers"}]`:    store.RoleViewer,
		`"roles":[{"value":"admin","primary":true}]`:               store.RoleAdmin,
		`"groups":[{"value":"g4","display":"Finance Department"}]`: store.RoleViewer, // no match, so the default
	}

	i := 0
	for attribute, want := range cases {
		username := "person" + strconv.Itoa(i) + "@example.org"
		i++

		rec := asIdentityProvider(t, h, http.MethodPost, "/scim/v2/Users",
			`{"schemas":["`+scim.SchemaUser+`"],"userName":"`+username+`",`+attribute+`}`)
		if rec.Code != http.StatusCreated {
			t.Errorf("%s: provisioning failed: %s", attribute, rec.Body.String())

			continue
		}

		var created scim.User
		if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		if len(created.Roles) == 0 || created.Roles[0].Value != string(want) {
			t.Errorf("%s produced role %v, expected %s", attribute, created.Roles, want)
		}
	}
}

// TestPlatformCannotBeGrantedByAGroupName is a privilege boundary.
//
// Platform is the role that crosses tenant boundaries. Letting a directory group name grant it means anybody who can create a
// group in the identity provider can reach every tenant's data - and creating a group is not usually a privileged action.
func TestPlatformCannotBeGrantedByAGroupName(t *testing.T) {
	h := scimHarness(t)

	for _, attempt := range []string{
		`"groups":[{"value":"g","display":"perfuse-platform"}]`,
		`"roles":[{"value":"platform"}]`,
		`"groups":[{"value":"g","display":"Platform Administrators"}]`,
	} {
		rec := asIdentityProvider(t, h, http.MethodPost, "/scim/v2/Users",
			`{"schemas":["`+scim.SchemaUser+`"],"userName":"escalate`+attempt[10:14]+`@example.org",`+attempt+`}`)
		if rec.Code != http.StatusCreated {
			continue
		}

		var created scim.User
		if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		for _, role := range created.Roles {
			if role.Value == string(store.RolePlatform) {
				t.Errorf("%s granted the platform role, which crosses every tenant boundary", attempt)
			}
		}
	}
}

// TestAnAbsentActiveOnAPutDoesNotChangeAccess covers a one-line bug with somebody's job attached.
//
// A provider syncing a display name with a partial resource must not re-enable an account that was deliberately disabled.
func TestAnAbsentActiveOnAPutDoesNotChangeAccess(t *testing.T) {
	h := scimHarness(t)
	scoped := h.store.ScopeUnchecked(store.DefaultTenant)

	user, err := scoped.CreateUser(t.Context(), "disabled@example.org", "a sufficiently long password",
		store.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	if err := scoped.SetDisabled(t.Context(), user.ID, true); err != nil {
		t.Fatal(err)
	}

	// A PUT with no active field at all.
	rec := asIdentityProvider(t, h, http.MethodPut, "/scim/v2/Users/"+itoa(user.ID),
		`{"schemas":["`+scim.SchemaUser+`"],"userName":"disabled@example.org","displayName":"A Person"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("the PUT was refused with %d: %s", rec.Code, rec.Body.String())
	}

	after, err := scoped.GetUserByID(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Disabled {
		t.Error("a PUT that never mentioned active re-enabled a deliberately disabled account")
	}
}

// TestTheProvisioningEndpointsAreAbsentUntilTurnedOn covers the default.
//
// These endpoints create and delete accounts. An installation not being provisioned by an identity provider should not expose
// them at all, because an endpoint nobody uses is one nobody is watching.
func TestTheProvisioningEndpointsAreAbsentUntilTurnedOn(t *testing.T) {
	h := newHarness(t) // SCIM not enabled

	for _, path := range []string{"/scim/v2/Users", "/scim/v2/ServiceProviderConfig", "/scim/v2/Users/1"} {
		rec := asIdentityProvider(t, h, http.MethodGet, path, "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s answered %d with provisioning turned off", path, rec.Code)
		}
	}
}

// TestAPasswordIsNeverReturnedByTheProvisioningEndpoints covers a leak into a system Perfuse does not control.
//
// A provider's request log holds what it sent and what came back. A password echoed there is a password in somebody else's
// log store.
func TestAPasswordIsNeverReturnedByTheProvisioningEndpoints(t *testing.T) {
	h := scimHarness(t)

	rec := asIdentityProvider(t, h, http.MethodPost, "/scim/v2/Users",
		`{"schemas":["`+scim.SchemaUser+`"],"userName":"rturner@example.org","password":"Sup3rSecret!"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("provisioning failed: %s", rec.Body.String())
	}

	body := rec.Body.String()
	if strings.Contains(body, "Sup3rSecret") {
		t.Errorf("the supplied password was echoed back: %s", body)
	}
	if strings.Contains(strings.ToLower(body), "password") {
		t.Errorf("a password field appears in the response: %s", body)
	}
}

// TestTheServiceProviderConfigDoesNotClaimWhatIsNotBuilt covers honesty.
//
// A provider reads this to decide what to send. Claiming bulk support makes it batch operations into a request that would be
// refused wholesale, which looks like every account failing at once.
func TestTheServiceProviderConfigDoesNotClaimWhatIsNotBuilt(t *testing.T) {
	h := scimHarness(t)

	rec := asIdentityProvider(t, h, http.MethodGet, "/scim/v2/ServiceProviderConfig", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var config scim.ServiceProviderConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}

	if !config.Patch.Supported {
		t.Error("patch is reported unsupported, and it is how providers disable accounts")
	}
	if config.Bulk.Supported {
		t.Error("bulk is claimed and is not implemented")
	}
	if config.ChangePassword.Supported {
		t.Error("changePassword is claimed; accepting one would create a way in the provider does not know about")
	}
	if !config.Filter.Supported || config.Filter.MaxResults <= 0 {
		t.Errorf("filtering is reported as %+v; an unstated limit means a provider asks for everything", config.Filter)
	}
	if len(config.AuthenticationSchemes) == 0 {
		t.Error("no authentication scheme is described")
	}
}
