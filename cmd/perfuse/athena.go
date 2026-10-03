package main

import (
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// perfuse athena prints the Amazon Athena table for an S3 archive written with format: ndjson.
//
// The archive is one JSON object per message under <destination>/dt=YYYY-MM-DD/, so the table uses partition projection: Athena works out
// the partitions from the date rather than from a catalogue, and nothing - no crawler, no MSCK REPAIR - has to run as days are added.
// Printed rather than executed, because creating a table needs Athena and Glue permissions this program should not hold.

var athenaIdent = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func cmdAthena(args []string, stdout, stderr io.Writer) error {
	fset := flag.NewFlagSet("athena", flag.ContinueOnError)
	fset.SetOutput(stderr)
	bucket := fset.String("bucket", "", "the archive bucket")
	dest := fset.String("destination", "", "the s3 destination's name, which is the first part of every key")
	table := fset.String("table", "hl7_archive", "the table to create")
	database := fset.String("database", "perfuse", "the Glue database to create it in")
	since := fset.String("since", "2026-01-01", "the first date the archive can hold, YYYY-MM-DD")
	fset.Usage = func() {
		fmt.Fprint(stderr, `Usage of athena:
  perfuse athena -bucket <bucket> -destination <name> [-table hl7_archive] [-database perfuse]

Prints the CREATE EXTERNAL TABLE statement for an S3 archive written with
s3.format: ndjson, using partition projection on the dt=YYYY-MM-DD key part.
Run it in the Athena console, then for example:

  SELECT control_id, received_at FROM perfuse.hl7_archive
  WHERE dt >= '2026-10-01' AND message_type = 'ADT' AND patient_id = '555';

`)
		fset.PrintDefaults()
	}
	if err := fset.Parse(args); err != nil {
		return err
	}
	if *bucket == "" || *dest == "" {
		fset.Usage()
		return fmt.Errorf("-bucket and -destination are required")
	}
	if !athenaIdent.MatchString(*table) || !athenaIdent.MatchString(*database) {
		return fmt.Errorf("-table and -database must be lower-case letters, digits and underscores")
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(*since) {
		return fmt.Errorf("-since must be YYYY-MM-DD")
	}
	if strings.ContainsAny(*bucket+*dest, "'\\ ") {
		return fmt.Errorf("the bucket and destination name cannot contain quotes, backslashes or spaces")
	}

	location := fmt.Sprintf("s3://%s/%s/", *bucket, *dest)
	fmt.Fprintf(stdout, `CREATE DATABASE IF NOT EXISTS %[1]s;

CREATE EXTERNAL TABLE IF NOT EXISTS %[1]s.%[2]s (
  received_at         string,
  channel             string,
  message_type        string,
  trigger_event       string,
  control_id          string,
  sending_application string,
  sending_facility    string,
  receiving_facility  string,
  patient_id          string,
  message_time        string,
  message             string
)
PARTITIONED BY (dt string)
ROW FORMAT SERDE 'org.openx.data.jsonserde.JsonSerDe'
LOCATION '%[3]s'
TBLPROPERTIES (
  'projection.enabled'        = 'true',
  'projection.dt.type'        = 'date',
  'projection.dt.format'      = 'yyyy-MM-dd',
  'projection.dt.range'       = '%[4]s,NOW',
  'projection.dt.interval'    = '1',
  'projection.dt.interval.unit' = 'DAYS',
  'storage.location.template' = '%[3]sdt=${dt}/'
);
`, *database, *table, location, *since)
	return nil
}
