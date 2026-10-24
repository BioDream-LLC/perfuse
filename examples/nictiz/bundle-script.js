// What a Dutch hospital adds to Perfuse's HL7 v2 to FHIR conversion for Nictiz's nl-core (zib 2020) profiles: how its own
// systems fill a name. Use it as a FHIR destination's bundle_script, or with `perfuse fhir convert -bundle-script`.
//
// nl-core's NameInformation says of every given name whether it is a full first name (BR) or an initial (IN). An HL7 v2 name
// does not: XPN.2 is "given name" and XPN.3 "second and further given names or initials thereof". Perfuse writes XPN.2 as the
// first given and XPN.3 as the second and does not guess which they are. This hospital's systems send the first names in
// XPN.2 (and XCN.3) and the initials in XPN.3 (and XCN.4); a hospital that sends them differently changes the rule below.

var QUALIFIER = "http://hl7.org/fhir/StructureDefinition/iso21090-EN-qualifier";

function qualify(name) {
  if (!name.given) return;
  name._given = name.given.map(function (g, i) {
    return { extension: [{ url: QUALIFIER, valueCode: i === 0 ? "BR" : "IN" }] };
  });
}

(bundle.entry || []).forEach(function (e) {
  var r = e.resource;
  if (r.resourceType === "Patient" || r.resourceType === "Practitioner" || r.resourceType === "RelatedPerson") {
    (r.name || []).forEach(qualify);
  }
});
