package natid

import (
	"errors"
	"fmt"
	"strings"
)

// The schemes, country by country. Sources for the identifier systems are given where a national FHIR guide or naming
// system names them; SystemVerified false means none was found and the value is a default for the site to confirm.

func init() {
	// ---- United Kingdom -----------------------------------------------------------------------------------------------
	nhsMod11 := func(s string) error {
		if err := needDigits(10)(s); err != nil {
			return err
		}
		c, ok := mod11Weights(s, []int{10, 9, 8, 7, 6, 5, 4, 3, 2})
		if !ok || c != d(s, 9) {
			return errCheck
		}
		return nil
	}
	register(&Scheme{ID: "uk-nhs", Country: "GB", ISO3: "GBR", Region: "England, Wales and the Isle of Man", Primary: true,
		Name: "NHS number", System: "https://fhir.nhs.uk/Id/nhs-number", SystemVerified: true,
		SystemSource: "NHS England FHIR R4 guides (UK Core)", Aliases: []string{"urn:oid:2.16.840.1.113883.2.1.4.1"},
		Format: "10 digits, printed 3 3 4", Check: "modulus 11, weights 10 to 2", Example: "999 000 0018",
		validate: nhsMod11, display: groups(3, 3, 4)})
	register(&Scheme{ID: "uk-chi", Country: "GB", ISO3: "GBR", Region: "Scotland",
		Name: "CHI number", Local: "Community Health Index", System: "https://phf.nhs.scot/fhir/Id/chi-number",
		SystemSource: "used by NHS Scotland's guides; confirm in the guide you exchange with",
		Format:       "10 digits: date of birth DDMMYY, then four", Check: "modulus 11, weights 10 to 2, as the NHS number",
		Example: "010170 0016", validate: func(s string) error {
			if err := nhsMod11(s); err != nil {
				return err
			}
			if !dateOK(atoi(s[0:2]), atoi(s[2:4]), atoi(s[4:6])) {
				return errors.New("the first six digits are not a date of birth (DDMMYY)")
			}
			return nil
		}, display: groups(6, 4)})
	register(&Scheme{ID: "uk-hcn", Country: "GB", ISO3: "GBR", Region: "Northern Ireland",
		Name: "Health and Care number", System: "https://fhir.hscni.net/Id/hc-number",
		SystemSource: "HSCNI; confirm in the guide you exchange with",
		Format:       "10 digits, 320 000 0001 to 399 999 9999", Check: "modulus 11, weights 10 to 2, as the NHS number",
		Example: "320 000 0015", validate: func(s string) error {
			if err := nhsMod11(s); err != nil {
				return err
			}
			if n := atoi(s[:3]); n < 320 || n > 399 {
				return errors.New("Northern Ireland numbers run from 320 000 0001 to 399 999 9999")
			}
			return nil
		}, display: groups(3, 3, 4)})

	// ---- Ireland --------------------------------------------------------------------------------------------------------
	register(&Scheme{ID: "ie-ppsn", Country: "IE", ISO3: "IRL", Primary: true,
		Name: "PPS number", Local: "Personal Public Service Number", System: "urn:perfuse:id:ie:ppsn",
		SystemSource: "no national FHIR system published; set the HSE's when it does",
		Format:       "7 digits, a check letter, and an optional second letter", Check: "modulus 23, weights 8 to 2 (the second letter weighs 9)",
		Restricted: "Only bodies the Social Welfare Consolidation Act 2005 lists may use the PPSN.",
		Example:    "1234567FA",
		validate: func(s string) error {
			if len(s) < 8 || len(s) > 9 || !allDigits(s[:7]) || s[7] < 'A' || s[7] > 'W' {
				return errors.New("should be 7 digits and a letter, with an optional second letter")
			}
			sum := weighted(s, []int{8, 7, 6, 5, 4, 3, 2})
			if len(s) == 9 {
				if s[8] < 'A' || s[8] > 'Z' {
					return errors.New("the second letter should be a letter")
				}
				if s[8] != 'W' {
					sum += int(s[8]-'A'+1) * 9
				}
			}
			want := byte('W')
			if r := sum % 23; r != 0 {
				want = byte('A' + r - 1)
			}
			if s[7] != want {
				return errCheck
			}
			return nil
		}})
	register(&Scheme{ID: "ie-ihi", Country: "IE", ISO3: "IRL",
		Name: "Individual Health Identifier", System: "https://fhir.hse.ie/Id/individual-health-identifier",
		SystemSource: "HSE; confirm in the guide you exchange with", Format: "digits, as the HSE issues them",
		Check: "none checked here (the HSE's algorithm is not published)", Example: "1234567",
		validate: func(s string) error {
			if !allDigits(s) || len(s) < 7 || len(s) > 18 {
				return errors.New("should be 7 to 18 digits")
			}
			return nil
		}})

	// ---- Germany, Austria, Switzerland ----------------------------------------------------------------------------------
	kvnr := func(s string) error {
		if len(s) != 10 || s[0] < 'A' || s[0] > 'Z' || !allDigits(s[1:]) {
			return errors.New("should be a capital letter and 9 digits")
		}
		num := fmt.Sprintf("%02d", int(s[0]-'A'+1)) + s[1:9]
		sum := 0
		for i := 0; i < len(num); i++ {
			x := d(num, i) * (1 + i%2)
			sum += x/10 + x%10
		}
		if sum%10 != d(s, 9) {
			return errCheck
		}
		return nil
	}
	register(&Scheme{ID: "de-kvnr", Country: "DE", ISO3: "DEU", Primary: true,
		Name: "Health insurance number (statutory)", Local: "Krankenversichertennummer (GKV)", System: "http://fhir.de/sid/gkv/kvid-10",
		SystemVerified: true, SystemSource: "fhir.de naming system, used by the KBV",
		// The OID HL7 v2 and CDA senders name it by (gematik's for the KVNR), and the identifier type fhir.de's base
		// profiles and ISiK slice the patient's identifiers on.
		Aliases:    []string{"urn:oid:1.2.276.0.76.4.8"},
		TypeSystem: "http://fhir.de/CodeSystem/identifier-type-de-basis", TypeCode: "KVZ10",
		Format:     "a capital letter and 9 digits",
		Check:      "letter as 01 to 26, then weights 1 and 2 alternately, cross sums, total modulus 10",
		Restricted: "§290 SGB V: the unchanging part is for health insurance and must not be used for other purposes.",
		Example:    "A123456780", validate: kvnr})
	register(&Scheme{ID: "de-kvnr-pkv", Country: "DE", ISO3: "DEU",
		Name: "Health insurance number (private)", Local: "Krankenversichertennummer (PKV)", System: "http://fhir.de/sid/pkv/kvid-10",
		SystemVerified: true, SystemSource: "fhir.de naming system", Format: "a capital letter and 9 digits",
		TypeSystem: "http://fhir.de/CodeSystem/identifier-type-de-basis", TypeCode: "PKV",
		Check: "as the statutory number", Example: "A123456780", validate: kvnr})
	register(&Scheme{ID: "at-svnr", Country: "AT", ISO3: "AUT", Primary: true,
		Name: "Social insurance number", Local: "Sozialversicherungsnummer", System: "urn:oid:1.2.40.0.10.1.4.3.1",
		SystemSource: "the SVNR's OID; ELGA's FHIR guides identify patients by the sector-specific bPK-GH instead",
		Format:       "10 digits: 3 serial, a check digit, then date of birth DDMMYY",
		Check:        "weights 3 7 9 (check) 5 8 4 2 1 6, modulus 11 equals the 4th digit", Example: "1237 010180",
		validate: func(s string) error {
			if err := needDigits(10)(s); err != nil {
				return err
			}
			if s[0] == '0' {
				return errors.New("does not begin with 0")
			}
			sum := d(s, 0)*3 + d(s, 1)*7 + d(s, 2)*9 + d(s, 4)*5 + d(s, 5)*8 + d(s, 6)*4 + d(s, 7)*2 + d(s, 8)*1 + d(s, 9)*6
			if r := sum % 11; r == 10 || r != d(s, 3) {
				return errCheck
			}
			return nil
		}, display: groups(4, 6)})
	register(&Scheme{ID: "ch-ahv", Country: "CH", ISO3: "CHE", Primary: true,
		Name: "AHV number", Local: "AHV-Nr. / numéro AVS / numero AVS", System: "urn:oid:2.16.756.5.32",
		SystemVerified: true, SystemSource: "CH Core and eCH-0213 (AHVN13)", Format: "13 digits beginning 756, printed 756.1234.5678.97",
		Check:      "EAN-13 (weights 1 and 3)",
		Restricted: "Systematic use is regulated by the AHVG (2022 revision): logging and security duties apply.",
		Example:    "756.1234.5678.97",
		validate: func(s string) error {
			if err := needDigits(13)(s); err != nil {
				return err
			}
			if !strings.HasPrefix(s, "756") {
				return errors.New("Swiss AHV numbers begin with 756")
			}
			sum := 0
			for i := 0; i < 12; i++ {
				sum += d(s, i) * (1 + 2*(i%2))
			}
			if (10-sum%10)%10 != d(s, 12) {
				return errCheck
			}
			return nil
		}, display: func(s string) string {
			if len(s) != 13 {
				return s
			}
			return s[:3] + "." + s[3:7] + "." + s[7:11] + "." + s[11:]
		}})

	// ---- Benelux --------------------------------------------------------------------------------------------------------
	register(&Scheme{ID: "nl-bsn", Country: "NL", ISO3: "NLD", Primary: true,
		Name: "Citizen service number", Local: "Burgerservicenummer (BSN)", System: "http://fhir.nl/fhir/NamingSystem/bsn",
		SystemVerified: true, SystemSource: "Nictiz (OID 2.16.840.1.113883.2.4.6.3)", Aliases: []string{"urn:oid:2.16.840.1.113883.2.4.6.3"},
		Format: "9 digits (8 are padded with a leading 0)", Check: "the eleven test: weights 9 to 2 and -1, total divisible by 11",
		Restricted: "The Wabb limits use of the BSN in care to identifying the patient.", Example: "111222333",
		normalise: func(v string) string {
			s := stripSep(v)
			if len(s) == 8 && allDigits(s) {
				s = "0" + s
			}
			return s
		},
		validate: func(s string) error {
			if err := needDigits(9)(s); err != nil {
				return err
			}
			if (weighted(s, []int{9, 8, 7, 6, 5, 4, 3, 2})-d(s, 8))%11 != 0 || s == "000000000" {
				return errCheck
			}
			return nil
		}})
	register(&Scheme{ID: "be-niss", Country: "BE", ISO3: "BEL", Primary: true,
		Name: "National register number", Local: "INSZ / NISS (rijksregisternummer)",
		System:       "https://www.ehealth.fgov.be/standards/fhir/core/NamingSystem/ssin",
		SystemSource: "eHealth Belgium core guide; confirm in the guide you exchange with", Aliases: []string{"urn:oid:2.16.56.1.1.1"},
		Format: "11 digits: YYMMDD, 3 serial, 2 check", Check: "97 minus (first nine modulo 97); a 2 is put in front for births from 2000",
		Restricted: "Each controller needs a legal basis to use the national register number.", Example: "93051822361",
		validate: func(s string) error {
			if err := needDigits(11)(s); err != nil {
				return err
			}
			first, check := int64(atoi(s[:9])), atoi(s[9:])
			if int(97-first%97) == check || int(97-(2000000000+first)%97) == check {
				return nil
			}
			return errCheck
		}, display: func(s string) string {
			if len(s) != 11 {
				return s
			}
			return s[:2] + "." + s[2:4] + "." + s[4:6] + "-" + s[6:9] + "." + s[9:]
		}})
	register(&Scheme{ID: "lu-matricule", Country: "LU", ISO3: "LUX", Primary: true,
		Name: "National identification number", Local: "Matricule", System: "urn:perfuse:id:lu:matricule",
		SystemSource: "no national FHIR system found; set the Agence eSanté's", Format: "13 digits: YYYYMMDD, 3 serial, 2 check",
		Check: "12th digit Luhn over the first 11, 13th Verhoeff over the first 11", Example: "1983060212318",
		validate: func(s string) error {
			if err := needDigits(13)(s); err != nil {
				return err
			}
			if !luhnValid(s[:12]) || !verhoeffValid(s[:11]+s[12:]) {
				return errCheck
			}
			return nil
		}})

	// ---- France, Italy, Spain, Portugal -------------------------------------------------------------------------------------
	register(&Scheme{ID: "fr-nir", Country: "FR", ISO3: "FRA", Primary: true,
		Name: "INS registration number (NIR)", Local: "matricule INS (NIR)", System: "urn:oid:1.2.250.1.213.1.4.8",
		SystemVerified: true, SystemSource: "ANS, Référentiel INS", Format: "15 characters: 13 (with 2A or 2B for Corsica) and a 2-digit key",
		Check:      "key = 97 minus (the 13 as a number modulo 97), 2A read as 19 and 2B as 18",
		Restricted: "The Référentiel INS (ANS, CNIL) allows its use only once the identity is qualified; the NIR is sensitive data.",
		Example:    "255081430500126",
		validate:   nirValid})
	register(&Scheme{ID: "fr-nia", Country: "FR", ISO3: "FRA",
		Name: "INS registration number (NIA, provisional)", Local: "matricule INS (NIA)", System: "urn:oid:1.2.250.1.213.1.4.9",
		SystemSource: "ANS documents give .9 and .10; confirm in the MOS", Format: "as the NIR", Check: "as the NIR",
		Example: "255081430500126", validate: nirValid})
	register(&Scheme{ID: "it-cf", Country: "IT", ISO3: "ITA", Primary: true,
		Name: "Tax code", Local: "Codice fiscale", System: "urn:oid:2.16.840.1.113883.2.9.4.3.2",
		SystemVerified: true, SystemSource: "HL7 Italia", Aliases: []string{"http://hl7.it/sid/codiceFiscale"},
		Format: "16 letters and digits", Check: "the last letter from the odd- and even-position tables, modulo 26",
		Example: "RSSMRA85T10A562S", validate: codiceFiscaleValid})
	register(&Scheme{ID: "es-dni", Country: "ES", ISO3: "ESP", Primary: true,
		Name: "National identity document", Local: "DNI / NIE", System: "urn:oid:1.3.6.1.4.1.19126.3",
		SystemSource: "used by Spanish guides; confirm in the guide you exchange with",
		Format:       "DNI 8 digits and a letter; NIE X, Y or Z, 7 digits and a letter", Check: "the letter is TRWAGMYFPDXBNJZSQVHLCKE[number modulo 23]",
		Example: "12345678Z", validate: func(s string) error {
			if len(s) != 9 {
				return errors.New("should be 8 digits and a letter (or X, Y or Z, 7 digits and a letter)")
			}
			body := s[:8]
			switch body[0] {
			case 'X':
				body = "0" + body[1:]
			case 'Y':
				body = "1" + body[1:]
			case 'Z':
				body = "2" + body[1:]
			}
			if !allDigits(body) {
				return errors.New("should be 8 digits and a letter (or X, Y or Z, 7 digits and a letter)")
			}
			if "TRWAGMYFPDXBNJZSQVHLCKE"[atoi(body)%23] != s[8] {
				return errCheck
			}
			return nil
		}})
	register(&Scheme{ID: "es-cip", Country: "ES", ISO3: "ESP",
		Name: "National Health System personal code", Local: "CIP-SNS", System: "urn:perfuse:id:es:cip-sns",
		SystemSource: "no national FHIR system found; each autonomous community has its own CIP too",
		Format:       "letters and digits as issued", Check: "none checked here", Example: "BBBB000000000000",
		validate: func(s string) error {
			if len(s) < 8 || len(s) > 20 {
				return errors.New("should be 8 to 20 letters and digits")
			}
			return nil
		}})
	register(&Scheme{ID: "pt-sns", Country: "PT", ISO3: "PRT", Primary: true,
		Name: "SNS user number", Local: "Número de utente do SNS", System: "urn:perfuse:id:pt:sns-utente",
		SystemSource: "no national FHIR system found; set SPMS's", Format: "9 digits",
		Check: "none checked here (SPMS's check digit is not published)", Example: "123456789", validate: needDigits(9)})

	// ---- Nordic countries ----------------------------------------------------------------------------------------------------
	register(&Scheme{ID: "se-pnr", Country: "SE", ISO3: "SWE", Primary: true,
		Name: "Personal identity number", Local: "Personnummer", System: "urn:oid:1.2.752.129.2.1.3.1",
		SystemVerified: true, SystemSource: "Swedish OID register (Inera)", Format: "YYMMDD-NNNC or YYYYMMDDNNNC",
		Check: "Luhn over YYMMDDNNN", Example: "811218-9876", normalise: swedishNormalise, validate: swedishValid(false),
		display: swedishDisplay})
	register(&Scheme{ID: "se-samordning", Country: "SE", ISO3: "SWE",
		Name: "Coordination number", Local: "Samordningsnummer", System: "urn:oid:1.2.752.129.2.1.3.3",
		SystemVerified: true, SystemSource: "Swedish OID register (Inera)", Format: "as the personnummer, with 60 added to the day",
		Check: "Luhn over YYMMDDNNN", Example: "811278-9873", normalise: swedishNormalise, validate: swedishValid(true),
		display: swedishDisplay})
	register(&Scheme{ID: "dk-cpr", Country: "DK", ISO3: "DNK", Primary: true,
		Name: "CPR number", Local: "CPR-nummer", System: "urn:oid:1.2.208.176.1.2", SystemVerified: true,
		SystemSource: "MedCom", Format: "DDMMYY-SSSS", Check: "none: since 2007 valid numbers need not pass the old modulus 11",
		Example: "070761-4285", validate: func(s string) error {
			if err := needDigits(10)(s); err != nil {
				return err
			}
			if !dateOK(atoi(s[0:2]), atoi(s[2:4]), atoi(s[4:6])) {
				return errors.New("the first six digits are not a date of birth (DDMMYY)")
			}
			return nil
		}, display: func(s string) string { return s[:6] + "-" + s[6:] }})
	register(&Scheme{ID: "no-fnr", Country: "NO", ISO3: "NOR", Primary: true,
		Name: "National identity number", Local: "Fødselsnummer", System: "urn:oid:2.16.578.1.12.4.1.4.1",
		SystemVerified: true, SystemSource: "Norwegian OID register (Helsedirektoratet)", Format: "11 digits: DDMMYY, 3 individual, 2 check",
		Check: "two modulus 11 digits (weights 3 7 6 1 8 9 4 5 2, then 5 4 3 2 7 6 5 4 3 2)", Example: "01010150074",
		validate: norwegianValid(false)})
	register(&Scheme{ID: "no-dnr", Country: "NO", ISO3: "NOR",
		Name: "D number", Local: "D-nummer", System: "urn:oid:2.16.578.1.12.4.1.4.2", SystemVerified: true,
		SystemSource: "Norwegian OID register (Helsedirektoratet)", Format: "as the fødselsnummer, with 4 added to the first digit",
		Check: "as the fødselsnummer", Example: "41010150068", validate: norwegianValid(true)})
	register(&Scheme{ID: "fi-hetu", Country: "FI", ISO3: "FIN", Primary: true,
		Name: "Personal identity code", Local: "Henkilötunnus", System: "urn:oid:1.2.246.21", SystemVerified: true,
		SystemSource: "Kanta (Finnish OID)", Format: "DDMMYY, a century sign (+, -, U to Y, A to F), 3 digits and a check character",
		Check: "DDMMYYNNN modulo 31 into 0123456789ABCDEFHJKLMNPRSTUVWXY", Example: "131052-308T",
		normalise: func(v string) string { return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(v), " ", "")) },
		validate: func(s string) error {
			if len(s) != 11 || !allDigits(s[:6]) || !allDigits(s[7:10]) || !strings.ContainsRune("+-UVWXYABCDEF", rune(s[6])) {
				return errors.New("should be DDMMYY, a century sign, 3 digits and a check character")
			}
			if "0123456789ABCDEFHJKLMNPRSTUVWXY"[atoi(s[:6]+s[7:10])%31] != s[10] {
				return errCheck
			}
			return nil
		}})
	register(&Scheme{ID: "is-kt", Country: "IS", ISO3: "ISL", Primary: true,
		Name: "Identity number", Local: "Kennitala", System: "urn:perfuse:id:is:kennitala",
		SystemSource: "no national FHIR system found; set the Directorate of Health's", Format: "10 digits: DDMMYY, 2, a check digit, a century digit",
		Check: "weights 3 2 7 6 5 4 3 2, modulus 11", Example: "120174-3399", validate: func(s string) error {
			if err := needDigits(10)(s); err != nil {
				return err
			}
			c, ok := mod11Weights(s, []int{3, 2, 7, 6, 5, 4, 3, 2})
			if !ok || c != d(s, 8) {
				return errCheck
			}
			return nil
		}, display: func(s string) string { return s[:6] + "-" + s[6:] }})

	// ---- Baltic states ---------------------------------------------------------------------------------------------------------
	balticCheck := func(s string) error {
		if err := needDigits(11)(s); err != nil {
			return err
		}
		c := weighted(s, []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 1}) % 11
		if c == 10 {
			c = weighted(s, []int{3, 4, 5, 6, 7, 8, 9, 1, 2, 3}) % 11
			if c == 10 {
				c = 0
			}
		}
		if c != d(s, 10) {
			return errCheck
		}
		return nil
	}
	register(&Scheme{ID: "ee-ik", Country: "EE", ISO3: "EST", Primary: true,
		Name: "Personal identification code", Local: "Isikukood", System: "urn:perfuse:id:ee:isikukood",
		SystemSource: "no national FHIR system found; set TEHIK's", Format: "11 digits: century and sex, YYMMDD, 3 serial, check",
		Check: "weights 1 to 9 and 1 modulo 11; when 10, weights 3 to 9, 1 to 3; when 10 again, 0", Example: "37605030299",
		validate: balticCheck})
	register(&Scheme{ID: "lt-ak", Country: "LT", ISO3: "LTU", Primary: true,
		Name: "Personal code", Local: "Asmens kodas", System: "urn:perfuse:id:lt:asmens-kodas",
		SystemSource: "no national FHIR system found", Format: "11 digits, as Estonia's", Check: "as Estonia's isikukood",
		Example: "37605030299", validate: balticCheck})
	register(&Scheme{ID: "lv-pk", Country: "LV", ISO3: "LVA", Primary: true,
		Name: "Personal code", Local: "Personas kods", System: "urn:perfuse:id:lv:personas-kods",
		SystemSource: "no national FHIR system found", Format: "11 digits; codes from 2017 begin 32 and carry no date or check",
		Check: "(1101 minus weights 1 6 3 7 9 10 5 8 4 2) modulo 11; none for codes beginning 32", Example: "16117219839",
		validate: func(s string) error {
			if err := needDigits(11)(s); err != nil {
				return err
			}
			if strings.HasPrefix(s, "32") {
				return nil
			}
			if (1101-weighted(s, []int{1, 6, 3, 7, 9, 10, 5, 8, 4, 2}))%11 != d(s, 10) {
				return errCheck
			}
			return nil
		}})

	// ---- Central and eastern Europe ------------------------------------------------------------------------------------------
	register(&Scheme{ID: "pl-pesel", Country: "PL", ISO3: "POL", Primary: true,
		Name: "PESEL number", Local: "Numer PESEL", System: "urn:oid:2.16.840.1.113883.3.4424.1.1.616",
		SystemSource: "the OID Polish P1 documents use; confirm in the guide you exchange with",
		Format:       "11 digits: YYMMDD (month plus 20 for the 2000s), 4 serial, check", Check: "weights 1 3 7 9 1 3 7 9 1 3, (10 - sum mod 10) mod 10",
		Example: "44051401359", validate: func(s string) error {
			if err := needDigits(11)(s); err != nil {
				return err
			}
			if (10-weighted(s, []int{1, 3, 7, 9, 1, 3, 7, 9, 1, 3})%10)%10 != d(s, 10) {
				return errCheck
			}
			return nil
		}})
	czsk := func(s string) error {
		if !allDigits(s) || (len(s) != 9 && len(s) != 10) {
			return errors.New("should be 9 or 10 digits (YYMMDD/XXXX)")
		}
		if len(s) == 9 { // numbers issued before 1954 have no check digit
			return nil
		}
		n := atoi(s[:9])
		r := n % 11
		if r == 10 {
			r = 0
		}
		if r != d(s, 9) {
			return errCheck
		}
		return nil
	}
	register(&Scheme{ID: "cz-rc", Country: "CZ", ISO3: "CZE", Primary: true,
		Name: "Birth number", Local: "Rodné číslo", System: "urn:perfuse:id:cz:rodne-cislo",
		SystemSource: "no national FHIR system found; set ÚZIS's", Format: "YYMMDD/XXXX (9 digits before 1954)",
		Check: "the 10-digit number divisible by 11 (remainder 10 written as 0)", Example: "7103192745", validate: czsk,
		display: func(s string) string { return s[:6] + "/" + s[6:] }})
	register(&Scheme{ID: "sk-rc", Country: "SK", ISO3: "SVK", Primary: true,
		Name: "Birth number", Local: "Rodné číslo", System: "urn:perfuse:id:sk:rodne-cislo",
		SystemSource: "no national FHIR system found; set NCZI's", Format: "as the Czech", Check: "as the Czech",
		Example: "7103192745", validate: czsk, display: func(s string) string { return s[:6] + "/" + s[6:] }})
	register(&Scheme{ID: "hu-taj", Country: "HU", ISO3: "HUN", Primary: true,
		Name: "Social security number", Local: "TAJ szám", System: "urn:perfuse:id:hu:taj",
		SystemSource: "no national FHIR system found; set EESZT's", Format: "9 digits", Check: "weights 3 and 7 alternately, modulus 10",
		Example: "123456788", validate: func(s string) error {
			if err := needDigits(9)(s); err != nil {
				return err
			}
			if weighted(s, []int{3, 7, 3, 7, 3, 7, 3, 7})%10 != d(s, 8) {
				return errCheck
			}
			return nil
		}, display: groups(3, 3, 3)})
	register(&Scheme{ID: "ro-cnp", Country: "RO", ISO3: "ROU", Primary: true,
		Name: "Personal numeric code", Local: "Cod numeric personal (CNP)", System: "urn:perfuse:id:ro:cnp",
		SystemSource: "no national FHIR system found", Format: "13 digits", Check: "weights 2 7 9 1 4 6 3 5 8 2 7 9 modulo 11; 10 becomes 1",
		Example: "1960229112226", validate: func(s string) error {
			if err := needDigits(13)(s); err != nil {
				return err
			}
			c := weighted(s, []int{2, 7, 9, 1, 4, 6, 3, 5, 8, 2, 7, 9}) % 11
			if c == 10 {
				c = 1
			}
			if c != d(s, 12) {
				return errCheck
			}
			return nil
		}})
	register(&Scheme{ID: "bg-egn", Country: "BG", ISO3: "BGR", Primary: true,
		Name: "Uniform civil number", Local: "ЕГН (EGN)", System: "urn:perfuse:id:bg:egn",
		SystemSource: "no national FHIR system found", Format: "10 digits", Check: "weights 2 4 8 5 10 9 7 3 6 modulo 11; 10 becomes 0",
		Example: "7523169263", validate: func(s string) error {
			if err := needDigits(10)(s); err != nil {
				return err
			}
			c := weighted(s, []int{2, 4, 8, 5, 10, 9, 7, 3, 6}) % 11
			if c == 10 {
				c = 0
			}
			if c != d(s, 9) {
				return errCheck
			}
			return nil
		}})
	register(&Scheme{ID: "hr-oib", Country: "HR", ISO3: "HRV", Primary: true,
		Name: "Personal identification number", Local: "OIB", System: "urn:perfuse:id:hr:oib",
		SystemSource: "no national FHIR system found; set CEZIH's", Format: "11 digits", Check: "ISO 7064 MOD 11,10",
		Example: "12345678903", validate: func(s string) error {
			if err := needDigits(11)(s); err != nil {
				return err
			}
			a := 10
			for i := 0; i < 10; i++ {
				a = (a + d(s, i)) % 10
				if a == 0 {
					a = 10
				}
				a = (a * 2) % 11
			}
			c := 11 - a
			if c == 10 {
				c = 0
			}
			if c != d(s, 10) {
				return errCheck
			}
			return nil
		}})
	register(&Scheme{ID: "si-emso", Country: "SI", ISO3: "SVN", Primary: true,
		Name: "Unique master citizen number", Local: "EMŠO", System: "urn:perfuse:id:si:emso",
		SystemSource: "no national FHIR system found; set NIJZ's", Format: "13 digits: DDMMYYY, region, serial, check",
		Check: "weights 7 6 5 4 3 2 7 6 5 4 3 2, 11 minus the remainder (0 for none)", Example: "0101006500006",
		validate: func(s string) error {
			if err := needDigits(13)(s); err != nil {
				return err
			}
			m := weighted(s, []int{7, 6, 5, 4, 3, 2, 7, 6, 5, 4, 3, 2}) % 11
			c := 0
			if m != 0 {
				c = 11 - m
			}
			if c == 10 || c != d(s, 12) {
				return errCheck
			}
			return nil
		}})
	register(&Scheme{ID: "gr-amka", Country: "GR", ISO3: "GRC", Primary: true,
		Name: "Social security registration number", Local: "ΑΜΚΑ (AMKA)", System: "urn:perfuse:id:gr:amka",
		SystemSource: "no national FHIR system found; set IDIKA's", Format: "11 digits: DDMMYY, 4 serial, check",
		Check: "Luhn", Example: "01017000009", validate: func(s string) error {
			if err := needDigits(11)(s); err != nil {
				return err
			}
			if !luhnValid(s) {
				return errCheck
			}
			if !dateOK(atoi(s[0:2]), atoi(s[2:4]), atoi(s[4:6])) {
				return errors.New("the first six digits are not a date of birth (DDMMYY)")
			}
			return nil
		}})
}

// ---- algorithms used by more than one scheme -----------------------------------------------------------------------------

func nirValid(s string) error {
	if len(s) != 15 {
		return errors.New("should be 13 characters and a 2-digit key")
	}
	body := s[:13]
	switch {
	case strings.Contains(body[5:7], "A") && body[5:7] == "2A":
		body = body[:5] + "19" + body[7:]
	case strings.Contains(body[5:7], "B") && body[5:7] == "2B":
		body = body[:5] + "18" + body[7:]
	}
	if !allDigits(body) || !allDigits(s[13:]) {
		return errors.New("should be digits (2A or 2B for Corsica) and a 2-digit key")
	}
	if s[0] < '1' || s[0] > '8' {
		return errors.New("the first digit (sex and kind of number) should be 1 to 8")
	}
	var n int64
	for _, c := range body {
		n = n*10 + int64(c-'0')
	}
	if int(97-n%97) != atoi(s[13:]) {
		return errCheck
	}
	return nil
}

func codiceFiscaleValid(s string) error {
	if len(s) != 16 {
		return errors.New("should be 16 letters and digits")
	}
	odd := map[byte]int{'0': 1, '1': 0, '2': 5, '3': 7, '4': 9, '5': 13, '6': 15, '7': 17, '8': 19, '9': 21,
		'A': 1, 'B': 0, 'C': 5, 'D': 7, 'E': 9, 'F': 13, 'G': 15, 'H': 17, 'I': 19, 'J': 21, 'K': 2, 'L': 4, 'M': 18, 'N': 20,
		'O': 11, 'P': 3, 'Q': 6, 'R': 8, 'S': 12, 'T': 14, 'U': 16, 'V': 10, 'W': 22, 'X': 25, 'Y': 24, 'Z': 23}
	sum := 0
	for i := 0; i < 15; i++ {
		c := s[i]
		isDigit, isLetter := c >= '0' && c <= '9', c >= 'A' && c <= 'Z'
		if !isDigit && !isLetter {
			return errors.New("should be 16 letters and digits")
		}
		if i%2 == 0 { // positions 1, 3, 5 ... counted from 1
			sum += odd[c]
		} else if isDigit {
			sum += int(c - '0')
		} else {
			sum += int(c - 'A')
		}
	}
	if byte('A'+sum%26) != s[15] {
		return errCheck
	}
	return nil
}

func swedishNormalise(v string) string {
	s := strings.TrimSpace(v)
	s = strings.NewReplacer(" ", "", "-", "", "+", "").Replace(s)
	if len(s) == 10 && allDigits(s) {
		// The century is not in a 10-digit number; the index stores 12 digits, so a number sent each way still matches.
		// "+" (aged 100 or more) is not distinguishable once removed, so such numbers keep their 10 digits.
		if strings.Contains(v, "+") {
			return s
		}
		yy := atoi(s[:2])
		century := "19"
		if yy <= currentYY() {
			century = "20"
		}
		s = century + s
	}
	return s
}

func swedishValid(coordination bool) func(string) error {
	return func(s string) error {
		if len(s) == 12 {
			s = s[2:]
		}
		if err := needDigits(10)(s); err != nil {
			return errors.New("should be YYMMDD-NNNC or YYYYMMDDNNNC")
		}
		day := atoi(s[4:6])
		if coordination {
			if day < 61 || day > 91 {
				return errors.New("a coordination number's day is the day plus 60 (61 to 91)")
			}
		} else if day < 1 || day > 31 {
			return errors.New("the day should be 01 to 31 (a coordination number adds 60)")
		}
		if !luhnValid(s) {
			return errCheck
		}
		return nil
	}
}

func norwegianValid(dNumber bool) func(string) error {
	return func(s string) error {
		if err := needDigits(11)(s); err != nil {
			return err
		}
		if dNumber != (s[0] >= '4' && s[0] <= '7') {
			if dNumber {
				return errors.New("a D number's first digit is 4 to 7")
			}
			return errors.New("a first digit of 4 to 7 is a D number, not a fødselsnummer")
		}
		k1, ok := mod11Weights(s, []int{3, 7, 6, 1, 8, 9, 4, 5, 2})
		if !ok || k1 != d(s, 9) {
			return errCheck
		}
		k2, ok := mod11Weights(s, []int{5, 4, 3, 2, 7, 6, 5, 4, 3, 2})
		if !ok || k2 != d(s, 10) {
			return errCheck
		}
		return nil
	}
}

var verhoeffD = [10][10]int{
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, {1, 2, 3, 4, 0, 6, 7, 8, 9, 5}, {2, 3, 4, 0, 1, 7, 8, 9, 5, 6},
	{3, 4, 0, 1, 2, 8, 9, 5, 6, 7}, {4, 0, 1, 2, 3, 9, 5, 6, 7, 8}, {5, 9, 8, 7, 6, 0, 4, 3, 2, 1},
	{6, 5, 9, 8, 7, 1, 0, 4, 3, 2}, {7, 6, 5, 9, 8, 2, 1, 0, 4, 3}, {8, 7, 6, 5, 9, 3, 2, 1, 0, 4},
	{9, 8, 7, 6, 5, 4, 3, 2, 1, 0}}
var verhoeffP = [8][10]int{
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, {1, 5, 7, 6, 2, 8, 3, 0, 9, 4}, {5, 8, 0, 3, 7, 9, 6, 1, 4, 2},
	{8, 9, 1, 6, 0, 4, 3, 5, 2, 7}, {9, 4, 5, 3, 1, 2, 6, 8, 7, 0}, {4, 2, 8, 6, 5, 7, 3, 9, 0, 1},
	{2, 7, 9, 3, 8, 0, 6, 4, 1, 5}, {7, 0, 4, 6, 9, 1, 3, 2, 5, 8}}

// verhoeffValid tests a number whose last digit is a Verhoeff check digit.
func verhoeffValid(s string) bool {
	c := 0
	for i := 0; i < len(s); i++ {
		c = verhoeffD[c][verhoeffP[i%8][d(s, len(s)-1-i)]]
	}
	return c == 0
}

func swedishDisplay(s string) string {
	if len(s) == 12 {
		return s[:8] + "-" + s[8:]
	}
	if len(s) == 10 {
		return s[:6] + "+" + s[6:]
	}
	return s
}
