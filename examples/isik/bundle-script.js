// What an ISiK (Stufe 4) hospital adds to Perfuse's HL7 v2 to FHIR conversion: the facts its own systems hold that an HL7 v2
// ADT does not carry. Use it as a FHIR destination's bundle_script, or with `perfuse fhir convert -bundle-script`.
//
// Every value below is the hospital's to set. Nothing here is derived from the message: Perfuse does not guess an IK number or
// which contact level a visit is, and this script only writes what the hospital says is true of all its messages.

// The hospital's Institutionskennzeichen (ARGE IK) and the name its systems send in MSH-4 / PV1-3.4.
var IKNR = "260326822";
var FACILITY = "KLINIKUM-MUSTERSTADT";

// The patient-number system this hospital's PI numbers are written under (serve -authority / identifier_systems). Its PID-3
// "PI" numbers are its medical record numbers, which ISiK types MR.
var PATIENT_NUMBERS = "https://fhir.klinikum-musterstadt.example/sid/patientennummer";

var V2_0203 = "http://terminology.hl7.org/CodeSystem/v2-0203";

(bundle.entry || []).forEach(function (e) {
  var r = e.resource;
  switch (r.resourceType) {
    case "Organization":
      // The facility gets its IK number (ISiKOrganisation needs an identifier; IKNR is the slice for a hospital).
      if (r.name === FACILITY && !(r.identifier || []).some(function (i) { return i.system === "http://fhir.de/sid/arge-ik/iknr"; })) {
        r.identifier = (r.identifier || []).concat([{ system: "http://fhir.de/sid/arge-ik/iknr", value: IKNR }]);
      }
      break;

    case "Patient":
      // This hospital's patient numbers are its medical record numbers (ISiK's Patientennummer slice is typed MR).
      (r.identifier || []).forEach(function (i) {
        if (i.system === PATIENT_NUMBERS) {
          i.type = { coding: [{ system: V2_0203, code: "MR" }] };
        }
      });
      break;

    case "Encounter":
      // A PV1 visit here is a department contact (PV1-10, the hospital service, is the department), ISiK's Kontaktebene.
      var types = r.type || [];
      if (!types.some(function (t) { return (t.coding || []).some(function (c) { return c.system === "http://fhir.de/CodeSystem/Kontaktebene"; }); })) {
        types.push({ coding: [{ system: "http://fhir.de/CodeSystem/Kontaktebene", code: "abteilungskontakt", display: "Abteilungskontakt" }] });
      }
      r.type = types;
      // Every diagnosis on a stay here is relevant to its treatment (ISiK's Diagnosetyp); the admission or discharge role
      // Perfuse read from DG1-6 stays as the subtype.
      (r.diagnosis || []).forEach(function (d) {
        var uses = d.use ? (d.use.coding || []) : [];
        d.use = { coding: [{ system: "http://fhir.de/CodeSystem/KontaktDiagnoseProzedur", code: "treatment-diagnosis",
          display: "Behandlungsrelevante Diagnosen" }].concat(uses) };
      });
      break;
  }
});
