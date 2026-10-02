package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// Tests for the shapes real identity providers actually send.
//
// **This is not the two-provider verification the queue asks for and does not replace it.** Every response here is one this codebase
// built, so it can only prove that the attribute handling copes with documented differences between vendors - not that any vendor
// produces what its documentation says. That still needs Keycloak locally and a hosted commercial tenant, and no container runtime is
// installed on this machine.
//
// What it does cover is the part written today with nothing behind it: reading groups and identity out of an assertion. Three vendors
// name the same attribute three ways, and the code that reconciles them was untested.

func TestGroupsArriveUnderAClaimURI(t *testing.T) {
	// Entra and ADFS send attribute names as claim URIs rather than short names. Somebody configuring Perfuse types "groups", because
	// that is what the field asks for and what their own documentation calls it, so the shorthand has to find the long form.
	// Each case needs its own response id, and the reason is worth writing down: the assertion-id replay cache is package-level state
	// in the parser and outlives a subtest. Reusing one id made the second and third cases fail as replays, and the assertion message
	// I had written blamed group matching - confidently, and wrongly. An assertion that names one cause when several are possible
	// sends the next person to the wrong place.
	for i, attrName := range []string{
		"groups",
		"http://schemas.microsoft.com/ws/2008/06/identity/claims/groups",
		"http://schemas.xmlsoap.org/claims/groups",
	} {
		t.Run(attrName, func(t *testing.T) {
			sh := newSAMLHarness(t, nil)
			requestID := sh.start(t, "")

			response := sh.idp.signResponse(t, "_r_claim"+strconv.Itoa(i), requestID, "grace@hospital.test", sh.acsURL,
				"https://perfuse.test", map[string][]string{attrName: {"perfuse-editors"}})

			res := sh.post(t, response)

			var session string

			for _, c := range res.Result().Cookies() {
				if c.Name == sessionCookie {
					session = c.Value
				}
			}

			if session == "" {
				t.Fatalf("groups sent as %q granted no role, so this provider's users would all be refused", attrName)
			}

			if role := roleOf(t, sh, session); role != "editor" {
				t.Errorf("role = %q, want editor", role)
			}
		})
	}
}

func TestAnAttributeWhoseNameMerelyEndsWithTheConfiguredWordIsNotMatched(t *testing.T) {
	// The limit of that leniency, which is worth pinning. Matching any name containing "groups" would find "excluded_groups" or
	// "nested_groups_flattened" and grant a role from the wrong list. Only an exact name or a final path segment counts.
	sh := newSAMLHarness(t, nil)
	requestID := sh.start(t, "")

	response := sh.idp.signResponse(t, "_r_nearmiss", requestID, "grace@hospital.test", sh.acsURL, "https://perfuse.test",
		map[string][]string{"excluded_groups": {"perfuse-admins"}})

	res := sh.post(t, response)

	for _, c := range res.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			t.Fatal("a role was granted from an attribute that merely ends with the configured word")
		}
	}
}

func TestSeveralGroupsInSeparateValuesAllCount(t *testing.T) {
	// The normal shape: one Attribute, several AttributeValue children. Somebody in an editor group and an admin group gets admin,
	// because resolving upward is the only safe direction - the alternative is losing access you are entitled to for being in a
	// lesser group as well.
	sh := newSAMLHarness(t, nil)
	requestID := sh.start(t, "")

	response := sh.idp.signResponse(t, "_r_multi", requestID, "grace@hospital.test", sh.acsURL, "https://perfuse.test",
		map[string][]string{"groups": {"perfuse-viewers", "perfuse-admins", "unrelated-group"}})

	res := sh.post(t, response)

	var session string

	for _, c := range res.Result().Cookies() {
		if c.Name == sessionCookie {
			session = c.Value
		}
	}

	if session == "" {
		t.Fatal("no session")
	}

	if role := roleOf(t, sh, session); role != "admin" {
		t.Errorf("role = %q, want admin: the most privileged matching group has to win", role)
	}
}

func TestGroupNamesAreMatchedWithoutRegardToCase(t *testing.T) {
	// Active Directory group names are routinely written with different capitalisation in the directory and in a configuration file,
	// and a mapping that silently matches nothing is very hard to tell apart from an attribute that is not being sent.
	sh := newSAMLHarness(t, nil)
	requestID := sh.start(t, "")

	response := sh.idp.signResponse(t, "_r_case", requestID, "grace@hospital.test", sh.acsURL, "https://perfuse.test",
		map[string][]string{"groups": {"Perfuse-Editors"}})

	res := sh.post(t, response)

	var session string

	for _, c := range res.Result().Cookies() {
		if c.Name == sessionCookie {
			session = c.Value
		}
	}

	if session == "" {
		t.Fatal("a group differing only in capitalisation matched nothing")
	}
}

func TestTheUsernameFallsBackWhenNoEmailAttributeIsSent(t *testing.T) {
	// Plenty of providers send no email attribute at all, and a NameID is often an opaque identifier. Whatever is chosen ends up in
	// the audit log beside things somebody changed, so it has to be a name a person can recognise rather than a random string - and
	// it must exist, since an empty username is not an account.
	sh := newSAMLHarness(t, nil)
	requestID := sh.start(t, "")

	response := sh.idp.signResponse(t, "_r_noemail", requestID, "HOSPITAL\\\\gsmith", sh.acsURL, "https://perfuse.test",
		map[string][]string{"groups": {"perfuse-editors"}})

	res := sh.post(t, response)

	var session string

	for _, c := range res.Result().Cookies() {
		if c.Name == sessionCookie {
			session = c.Value
		}
	}

	if session == "" {
		t.Fatalf("a sign-in with no email attribute was refused: %s", res.Header().Get("Location"))
	}

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})

	who := httptest.NewRecorder()
	sh.server.Handler().ServeHTTP(who, req)

	var me struct {
		Username string `json:"username"`
	}
	_ = json.Unmarshal(who.Body.Bytes(), &me)

	// The domain prefix is dropped, because HOSPITAL\gsmith is not a name anybody wants to read in an audit log.
	if me.Username != "gsmith" {
		t.Errorf("username = %q, want gsmith", me.Username)
	}
}

func TestGroupsSentAsOneCommaSeparatedValueAreNotSplit(t *testing.T) {
	// A recorded limitation rather than a claim about correctness.
	//
	// A few providers can be configured to emit group membership as a single delimited string. SAML has no notion of that - an
	// attribute with several values has several AttributeValue elements - so the string is treated as one group name and matches
	// nothing. That is the right reading of the document, and splitting on commas would mean a group legitimately containing a comma
	// silently became two.
	//
	// Written down because the symptom is "nobody gets a role" with a log line showing the groups attribute present and populated,
	// which is a confusing place to be. The fix is at the provider, not here.
	sh := newSAMLHarness(t, nil)
	requestID := sh.start(t, "")

	response := sh.idp.signResponse(t, "_r_joined", requestID, "grace@hospital.test", sh.acsURL, "https://perfuse.test",
		map[string][]string{"groups": {"perfuse-editors,perfuse-viewers"}})

	res := sh.post(t, response)

	for _, c := range res.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			t.Fatal("a comma-joined group string was split; if that changed deliberately, update this test and say why")
		}
	}
}

// roleOf reads the role a session carries.
func roleOf(t *testing.T, sh samlHarness, session string) string {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})

	res := httptest.NewRecorder()
	sh.server.Handler().ServeHTTP(res, req)

	var me struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}

	return me.Role
}
