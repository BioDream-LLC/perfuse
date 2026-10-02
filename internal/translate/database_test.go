package translate

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/mirth"
)

func TestConvertVelocityStatementBindsRatherThanPastes(t *testing.T) {
	// The whole point of translating this connector. The original pasted values into
	// the SQL; every one of those statements is one apostrophe away from failing on a
	// real patient, and O'Brien is a common name.
	stmt, params, notes := convertVelocityStatement(
		`INSERT INTO admissions (mrn, surname) VALUES ('${msg['PID']['PID.3']['PID.3.1'].toString()}', '${msg['PID']['PID.5']['PID.5.1'].toString()}')`,
		"postgres")

	if len(notes) != 0 {
		t.Errorf("unexpected notes: %v", notes)
	}
	want := "INSERT INTO admissions (mrn, surname) VALUES ($1, $2)"
	if stmt != want {
		t.Errorf("statement =\n  %q\nwant\n  %q", stmt, want)
	}
	if len(params) != 2 || params[0] != "PID-3.1" || params[1] != "PID-5.1" {
		t.Errorf("params = %v, want [PID-3.1 PID-5.1]", params)
	}
}

func TestConvertVelocityStatementStripsTheQuotesTheAuthorAdded(t *testing.T) {
	// Mirth statements are routinely written with the reference inside quotes, because
	// the value was being pasted in. Leaving them makes the placeholder a literal
	// string, so every row gets the text "$1" written into it - a statement that runs,
	// reports success, and fills a column with rubbish.
	stmt, _, _ := convertVelocityStatement(
		`UPDATE t SET a = '${PID.5.1}' WHERE b = "${PID.3.1}"`, "postgres")

	if strings.Contains(stmt, "'$1'") || strings.Contains(stmt, `"$2"`) {
		t.Errorf("the quotes were not stripped: %q", stmt)
	}
	if !strings.Contains(stmt, "a = $1") {
		t.Errorf("statement = %q", stmt)
	}
	if !strings.Contains(stmt, "b = $2") {
		t.Errorf("statement = %q", stmt)
	}
}

func TestConvertVelocityStatementUsesEachDialectsPlaceholder(t *testing.T) {
	cases := []struct {
		driver, want string
	}{
		{"postgres", "$1"},
		{"sqlserver", "@p1"},
		{"mysql", "?"},
		{"sqlite", "?"},
	}
	for _, tc := range cases {
		stmt, _, _ := convertVelocityStatement(
			`INSERT INTO t (a) VALUES ('${PID.3.1}')`, tc.driver)
		if !strings.Contains(stmt, tc.want) {
			t.Errorf("%s: statement = %q, want a %q placeholder", tc.driver, stmt, tc.want)
		}
	}
}

func TestConvertVelocityStatementStripsQuotesForTheQuestionMarkDialect(t *testing.T) {
	// The ? dialect has no numbering, so the quote stripping has to handle it
	// separately. Missing this would leave '?' as a literal question mark string in
	// every mysql and sqlite migration.
	stmt, _, _ := convertVelocityStatement(
		`INSERT INTO t (a, b) VALUES ('${PID.3.1}', '${PID.5.1}')`, "mysql")

	if strings.Contains(stmt, "'?'") {
		t.Errorf("the quotes were not stripped for the ? dialect: %q", stmt)
	}
	if stmt != "INSERT INTO t (a, b) VALUES (?, ?)" {
		t.Errorf("statement = %q", stmt)
	}
}

func TestConvertVelocityStatementMarksWhatItCannotResolve(t *testing.T) {
	// Guessing is the one thing this must not do. A wrong path binds a value to the
	// wrong column and writes data that looks entirely valid, which is far worse than
	// a statement that refuses to load.
	stmt, params, notes := convertVelocityStatement(
		`INSERT INTO t (a) VALUES ('${$co('someChannelMapVariable')}')`, "postgres")

	if len(notes) == 0 {
		t.Error("an unresolvable reference should produce a note")
	}
	if len(params) != 1 {
		t.Fatalf("params = %v", params)
	}
	if !strings.HasPrefix(params[0], "REVIEW-") {
		t.Errorf("params[0] = %q, want a REVIEW marker rather than a guess", params[0])
	}
	// The statement still has to be shaped correctly, so the file loads far enough for
	// somebody to fix one field.
	if !strings.Contains(stmt, "$1") {
		t.Errorf("statement = %q", stmt)
	}
}

func TestConvertVelocityStatementHandlesAPlainFieldReference(t *testing.T) {
	_, params, notes := convertVelocityStatement(
		`INSERT INTO t (a, b, c) VALUES (${PID.3.1}, ${PID.5}, ${MSH.10.1})`, "postgres")

	if len(notes) != 0 {
		t.Errorf("unexpected notes: %v", notes)
	}
	want := []string{"PID-3.1", "PID-5", "MSH-10.1"}
	for i, w := range want {
		if params[i] != w {
			t.Errorf("params[%d] = %q, want %q", i, params[i], w)
		}
	}
}

func TestConvertVelocityStatementReportsAnEmptyStatement(t *testing.T) {
	stmt, _, notes := convertVelocityStatement("   ", "postgres")
	if stmt != "" {
		t.Errorf("statement = %q", stmt)
	}
	if len(notes) == 0 {
		t.Error("an empty statement should be reported rather than passed through")
	}
}

func TestDottedToPathRefusesSomethingThatIsNotAField(t *testing.T) {
	cases := []string{"PID", "notasegment.5.1", "P.5", "", "PIDX.5.1"}
	for _, in := range cases {
		if got, ok := dottedToPath(in); ok {
			t.Errorf("dottedToPath(%q) = %q, but it is not a field reference", in, got)
		}
	}
}

func TestMirthDriverToPerfuseMapsTheDriversWeHave(t *testing.T) {
	cases := []struct{ jdbc, want string }{
		{"org.postgresql.Driver", "postgres"},
		{"com.mysql.cj.jdbc.Driver", "mysql"},
		{"org.mariadb.jdbc.Driver", "mysql"},
		{"com.microsoft.sqlserver.jdbc.SQLServerDriver", "sqlserver"},
		{"net.sourceforge.jtds.jdbc.Driver", "sqlserver"},
		{"org.sqlite.JDBC", "sqlite"},
		// Oracle and DB2 have no pure-Go driver worth staking a clinical feed on, so
		// they must map to nothing and become a blocker rather than a wrong guess.
		{"oracle.jdbc.OracleDriver", ""},
		{"com.ibm.db2.jcc.DB2Driver", ""},
	}
	for _, tc := range cases {
		if got := mirthDriverToPerfuse(tc.jdbc); got != tc.want {
			t.Errorf("mirthDriverToPerfuse(%q) = %q, want %q", tc.jdbc, got, tc.want)
		}
	}
}

func TestJDBCToDSNProducesSomethingTheDriverAccepts(t *testing.T) {
	cases := []struct {
		driver, url  string
		wantContains []string
	}{
		{
			"postgres", "jdbc:postgresql://db.internal:5432/records",
			[]string{"postgres://", "db.internal:5432", "/records", "${TEST_DB_PASSWORD}"},
		},
		{
			"mysql", "jdbc:mysql://db.internal:3306/records",
			[]string{"tcp(db.internal:3306)", "/records", "${TEST_DB_PASSWORD}"},
		},
		{
			"sqlserver", "jdbc:sqlserver://db.internal:1433;databaseName=lab",
			[]string{"sqlserver://", "db.internal:1433", "database=lab"},
		},
	}

	for _, tc := range cases {
		got := jdbcToDSN(tc.driver, tc.url, "interface", "TEST_DB")
		for _, want := range tc.wantContains {
			if !strings.Contains(got, want) {
				t.Errorf("jdbcToDSN(%s, %q) = %q, missing %q",
					tc.driver, tc.url, got, want)
			}
		}
		// The password is never carried across, so the DSN must reference the
		// environment instead. A literal here would be a credential in git.
		if strings.Contains(got, "hunter2") {
			t.Errorf("jdbcToDSN produced a literal password: %q", got)
		}
	}
}

func TestParseJDBCURLHandlesBothShapes(t *testing.T) {
	cases := []struct {
		in, host, database string
	}{
		{"jdbc:postgresql://db:5432/records", "db:5432", "records"},
		{"jdbc:mysql://db:3306/records?useSSL=true", "db:3306", "records"},
		{"jdbc:sqlserver://db:1433;databaseName=lab", "db:1433", "lab"},
		{"jdbc:sqlserver://db:1433;databaseName=lab;encrypt=true", "db:1433", "lab"},
	}

	for _, tc := range cases {
		host, database := parseJDBCURL(tc.in)
		if host != tc.host || database != tc.database {
			t.Errorf("parseJDBCURL(%q) = (%q, %q), want (%q, %q)",
				tc.in, host, database, tc.host, tc.database)
		}
	}
}

func TestJDBCToDSNMarksWhatItCouldNotWorkOut(t *testing.T) {
	// A DSN with a real-looking but wrong host would connect somewhere unintended, so
	// an unparseable URL has to produce something that obviously needs editing.
	got := jdbcToDSN("postgres", "", "", "TEST_DB")
	if !strings.Contains(got, "CHANGEME") {
		t.Errorf("jdbcToDSN with no URL = %q, which does not obviously need editing", got)
	}
}

func TestConvertVelocityColumnsResolvesColumnNames(t *testing.T) {
	// A Database Reader's on-update statement runs against the row that was just
	// read, so ${row_id} means the row_id column, not a message field. Resolving it
	// as a field finds nothing and leaves key_column as CHANGEME, which a real
	// export is what showed.
	stmt, params, notes := convertVelocityColumns(
		"UPDATE lab_outbound SET processed = now() WHERE row_id = ${row_id}", "postgres")

	if len(notes) != 0 {
		t.Errorf("unexpected notes: %v", notes)
	}
	if !strings.Contains(stmt, "row_id = $1") {
		t.Errorf("statement = %q", stmt)
	}
	if len(params) != 1 || params[0] != "row_id" {
		t.Errorf("params = %v, want [row_id]", params)
	}
}

func TestConvertVelocityStatementDoesNotTreatFieldsAsColumns(t *testing.T) {
	// The inverse has to stay true: a destination statement's references are message
	// fields, and reading them as column names would bind a literal string.
	_, params, _ := convertVelocityStatement(
		"INSERT INTO t (a) VALUES ('${PID.3.1}')", "postgres")
	if len(params) != 1 || params[0] != "PID-3.1" {
		t.Errorf("params = %v, want [PID-3.1]", params)
	}
}

func TestEnvVarNamesAreDistinctPerConnector(t *testing.T) {
	// A channel reading one database and writing another would otherwise reference
	// the same ${DB_PASSWORD} twice, and whichever value was set would be wrong for
	// one of them - a failure that looks like a bad password on a connection whose
	// password is fine.
	src := jdbcToDSN("postgres", "jdbc:postgresql://a:5432/x", "u", envVarName("source"))
	dst := jdbcToDSN("sqlserver", "jdbc:sqlserver://b:1433;databaseName=y", "u",
		envVarName("write-to-registry-db"))

	if src == dst {
		t.Fatal("two connectors produced the same DSN")
	}
	if !strings.Contains(src, "${SOURCE_DB_PASSWORD}") {
		t.Errorf("source DSN = %q", src)
	}
	if !strings.Contains(dst, "${WRITE_TO_REGISTRY_DB_PASSWORD}") {
		t.Errorf("destination DSN = %q", dst)
	}
}

func TestEnvVarNameSurvivesAwkwardConnectorNames(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Write To Registry DB", "WRITE_TO_REGISTRY_DB"},
		{"lab-results", "LAB_RESULTS_DB"},
		{"a.b/c", "A_B_C_DB"},
		// Must never produce a bare "_PASSWORD", which would be an unsettable name.
		{"", "DB"},
		{"---", "DB"},
	}
	for _, tc := range cases {
		if got := envVarName(tc.in); got != tc.want {
			t.Errorf("envVarName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSFTPDestinationTranslationRequiresAHostKey(t *testing.T) {
	// Mirth does not verify a host key by default, so no export contains one. That
	// makes this a blocker rather than a warning: the alternative is a channel that
	// connects to anything answering on that address and hands over the credentials.
	b := &builder{}
	out := b.buildSFTPDestination(mirth.Connector{
		Name: "send-to-lab",
		Properties: map[string]string{
			"scheme": "sftp", "host": "sftp.lab.internal/incoming",
			"username": "interface", "password": "hunter2",
		},
	}, "send-to-lab", "destination 1")

	if !strings.Contains(out, "known_hosts_file") {
		t.Error("the output should include known_hosts_file")
	}
	if !strings.Contains(out, "ssh-keyscan") {
		t.Error("the output should say how to get the key")
	}
	// The password is never carried across. Mirth stores it in the export, and writing
	// it into a file destined for git would turn a migration into a credential leak.
	if strings.Contains(out, "hunter2") {
		t.Error("the password was carried across")
	}
	if !strings.Contains(out, "${SEND_TO_LAB_SFTP_PASSWORD}") {
		t.Errorf("the password should be an environment reference:\n%s", out)
	}

	// host and dir have to be pulled apart; Mirth keeps them in one field.
	if !strings.Contains(out, "host: sftp.lab.internal") {
		t.Errorf("the host was not separated from the path:\n%s", out)
	}
	if !strings.Contains(out, "dir: /incoming") {
		t.Errorf("the directory was not separated from the host:\n%s", out)
	}

	blockers := 0
	for _, n := range b.notes {
		if n.Severity == "blocker" {
			blockers++
		}
	}
	if blockers == 0 {
		t.Error("a missing host key should be a blocker, not a warning")
	}
}

func TestSFTPTranslationAddsFramingWhenAppending(t *testing.T) {
	// Several messages in one file cannot be separated again reliably without it,
	// because MSH can appear inside a free-text field.
	b := &builder{}
	out := b.buildSFTPDestination(mirth.Connector{
		Properties: map[string]string{
			"scheme": "sftp", "host": "h/dir", "username": "u",
			"outputAppend": "true",
		},
	}, "batch", "destination 1")

	if !strings.Contains(out, "append: true") {
		t.Error("append was not carried across")
	}
	if !strings.Contains(out, "framed: true") {
		t.Errorf("appending without framing produces a file nobody can split:\n%s", out)
	}
}
