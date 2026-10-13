// Package published holds the guards for Perfuse's importable packages, in one place.
//
// hl7 was the first package published and carries its own copy of these checks. fhir and pas were published so that a
// decision service such as Vaglio reads FHIR and PAS requests with exactly the code Perfuse uses, rather than a copy that
// drifts: four readers of the X12 action codes in this repository once disagreed, and one read a denial as an approval.
// The guards are kept here rather than copied into each package, for the same reason.
package published
