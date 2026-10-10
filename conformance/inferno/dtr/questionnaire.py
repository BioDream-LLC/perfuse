"""A payer questionnaire that uses every element DTR 2.2.0 marks must-support, so the Inferno suite can see that Perfuse
packages each of them unchanged. Loaded by run.py; also used for the $expand test (its answer ValueSet is stored)."""

SDC = "http://hl7.org/fhir/uv/sdc/StructureDefinition/"
EXT = "http://hl7.org/fhir/StructureDefinition/"
DTR = "http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/"
LIB = "https://madie.cms.gov/Library/DTRTest"
VS = "urn:inferno:dtr-test-kit:ValueSet/drinks"
ALT = {"url": DTR + "alternativeExpression",
       "valueExpression": {"language": "application/elm+json", "expression": "LibraryName.ExpressionName"}}


def cql(expr, with_alt=True):
    v = {"language": "text/cql-identifier", "expression": expr, "reference": LIB}
    if with_alt:
        v["extension"] = [ALT]
    return v


VALUESET = {
    "resourceType": "ValueSet", "id": "drinks", "url": VS, "version": "1.0.0", "status": "active", "name": "Drinks",
    "compose": {"include": [{"system": "urn:inferno:dtr-test-kit:CodeSystem/drinks",
                             "concept": [{"code": "water", "display": "Water"}, {"code": "juice", "display": "Juice"}]}]},
}

BIG_VS = "urn:inferno:dtr-test-kit:ValueSet/sides"
BIG_VALUESET = {
    "resourceType": "ValueSet", "id": "sides", "url": BIG_VS, "version": "1.0.0", "status": "active", "name": "Sides",
    "compose": {"include": [{"system": "urn:inferno:dtr-test-kit:CodeSystem/sides",
                             "concept": [{"code": f"side-{i:02d}", "display": f"Side dish {i}"} for i in range(1, 46)]}]},
}

QUESTIONNAIRE = {
    "resourceType": "Questionnaire", "id": "DinnerOrderFull", "url": "urn:inferno:dtr-test-kit:dinner-order-full",
    "version": "1.0.0", "name": "DinnerOrderFull", "title": "Dinner order (every must-support element)", "status": "active",
    "subjectType": ["Patient"], "date": "2026-10-05", "publisher": "Perfuse test data",
    "effectivePeriod": {"start": "2026-01-01"},
    "extension": [
        {"url": EXT + "cqf-library", "valueCanonical": LIB},
        {"url": EXT + "preferredTerminologyServer", "valueUrl": "https://tx.fhir.org/r4"},
        {"url": EXT + "artifact-versionAlgorithm", "valueCoding": {"system": "http://hl7.org/fhir/version-algorithm", "code": "semver"}},
        {"url": SDC + "sdc-questionnaire-performerType", "valueCode": "Practitioner"},
        {"url": DTR + "estimated-completion-time", "extension": [
            {"url": "totalTime", "valueCode": "under-3min"}, {"url": "clinicalTime", "valueCode": "under-1min"}]},
        {"url": DTR + "questionnaireAudience", "valueCode": "clinical"},
        {"url": DTR + "request-specific", "valueBoolean": False},
        {"url": EXT + "questionnaire-signatureRequired", "valueCodeableConcept": {"coding": [
            {"system": "urn:iso-astm:E1762-95:2013", "code": "1.2.840.10065.1.12.1.1"}]}},
        {"url": EXT + "variable", "valueExpression": {"name": "today", "language": "text/fhirpath", "expression": "today()"}},
        {"url": SDC + "sdc-questionnaire-launchContext", "extension": [
            {"url": "name", "valueCoding": {"system": "http://hl7.org/fhir/uv/sdc/CodeSystem/launchContext", "code": "patient"}},
            {"url": "type", "valueCode": "Patient"}]},
        {"url": SDC + "sdc-questionnaire-itemPopulationContext", "valueExpression": cql("PatientName")},
        {"url": SDC + "sdc-questionnaire-entryMode", "valueCode": "sequential"},
    ],
    "item": [
        {"linkId": "1", "prefix": "1.", "type": "group", "text": "Order", "item": [
            {"linkId": "1.1", "type": "string", "text": "Last name", "readOnly": True,
             "extension": [{"url": SDC + "sdc-questionnaire-initialExpression", "valueExpression": cql("LastName")}]},
            {"linkId": "1.2", "type": "choice", "text": "Drink", "answerValueSet": VS, "required": True,
             "extension": [{"url": SDC + "sdc-questionnaire-candidateExpression", "valueExpression": cql("Drinks")},
                           {"url": SDC + "sdc-questionnaire-contextExpression", "extension": [
                               {"url": "label", "valueString": "Drinks on the menu"},
                               {"url": "expression", "valueExpression": cql("DrinkContext")}]}]},
            {"linkId": "1.2b", "type": "choice", "text": "Side dish", "answerValueSet": BIG_VS},
            {"linkId": "1.3", "type": "choice", "text": "Main course",
             "_text": {"extension": [{"url": EXT + "rendering-xhtml", "valueString": "<b>Main course</b>"}]},
             "answerOption": [
                 {"valueCoding": {"system": "urn:inferno:dtr-test-kit:CodeSystem/meals", "code": "burger", "display": "Hamburger"},
                  "extension": [{"url": EXT + "questionnaire-optionExclusive", "valueBoolean": True}]},
                 {"valueReference": {"reference": "Substance/example", "display": "Allergy-free meal"}},
                 {"valueString": "Bean burrito",
                  "_valueString": {"extension": [{"url": EXT + "rendering-xhtml", "valueString": "<i>Bean burrito</i>"}]}}]},
            {"linkId": "1.4", "type": "integer", "text": "Servings", "repeats": False,
             "extension": [{"url": SDC + "sdc-questionnaire-calculatedExpression", "valueExpression": cql("Servings")}]},
            {"linkId": "1.5", "type": "quantity", "text": "Portion",
             "extension": [{"url": EXT + "questionnaire-unitOption", "valueCoding": {"system": "http://unitsofmeasure.org", "code": "g"}},
                           {"url": EXT + "questionnaire-unitValueSet", "valueCanonical": "http://hl7.org/fhir/ValueSet/ucum-units"}]},
            {"linkId": "1.6", "type": "reference", "text": "Dietitian",
             "extension": [{"url": EXT + "questionnaire-referenceResource", "valueCode": "Practitioner"},
                           {"url": EXT + "questionnaire-referenceProfile", "valueCanonical": "http://hl7.org/fhir/us/core/StructureDefinition/us-core-practitioner"},
                           {"url": SDC + "sdc-questionnaire-lookupQuestionnaire", "valueCanonical": "urn:inferno:dtr-test-kit:dinner-order-static"}]},
            {"linkId": "1.7", "type": "attachment", "text": "Menu photo",
             "extension": [{"url": EXT + "mimeType", "valueCode": "image/png"}]},
            {"linkId": "1.8", "type": "string", "text": "Special requests", "enableBehavior": "any",
             "enableWhen": [{"question": "1.3", "operator": "exists", "answerBoolean": True},
                            {"question": "1.4", "operator": ">", "answerInteger": 1}],
             "extension": [{"url": SDC + "sdc-questionnaire-enableWhenExpression", "valueExpression": cql("WantsExtras")}]},
            {"linkId": "1.9", "type": "display", "text": "Internal note",
             "extension": [{"url": EXT + "questionnaire-hidden", "valueBoolean": True},
                           {"url": EXT + "questionnaire-supportHyperlink", "extension": [
                               {"url": "label", "valueString": "DTR guide"},
                               {"url": "link", "valueUri": "https://hl7.org/fhir/us/davinci-dtr/"}]}]},
            {"linkId": "1.10", "type": "group", "text": "Each guest", "repeats": True,
             "extension": [{"url": SDC + "sdc-questionnaire-itemPopulationContext", "valueExpression": cql("Guests")}],
             "item": [{"linkId": "1.10.1", "type": "string", "text": "Guest name", "initial": [{"valueString": "Guest"}]}]},
        ]},
    ],
}
