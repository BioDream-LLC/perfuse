package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Versioned migrations, for changes the base schema cannot express.
//
// The statements in `schema` are all CREATE ... IF NOT EXISTS, which is why they
// can be re-run on every start. That works for adding a table and not for anything
// else: SQLite has no ALTER TABLE ... ADD COLUMN IF NOT EXISTS, and it cannot drop
// a UNIQUE constraint at all.
//
// So changes of that kind live here and run exactly once, gated on schema_version -
// a table that already existed and was never used.
//
// # Rules
//
//   - Append only. A released migration is never edited, because an installation
//     that already ran it will not run it again, and editing it means two
//     installations of the same version have different schemas.
//   - Each runs in a transaction. A migration that fails half way and leaves the
//     database in a shape no code expects is worse than one that fails cleanly.
//   - Each must be safe on an empty database as well as a populated one, because
//     a fresh install runs all of them in order.
type migration struct {
	// name appears in errors and logs. Worth having: "migration 3 failed" sends
	// somebody counting through a slice.
	name string
	// stmts run in order inside one transaction.
	stmts []string
}

var migrations = []migration{
	{
		// Multi-tenancy.
		//
		// users has to be rebuilt rather than altered, because username is UNIQUE
		// and SQLite cannot drop that constraint. Two tenants both wanting an
		// "admin" is not an edge case, it is the first thing that happens.
		//
		// Existing rows are assigned to a default tenant rather than deleted or
		// left null. A single-tenant installation upgrading in place must keep
		// working with its existing logins, and a null tenant would be a row that
		// every scoped query silently excludes - which presents as "all my users
		// disappeared".
		name: "tenancy",
		stmts: []string{
			`CREATE TABLE IF NOT EXISTS tenants (
				id         TEXT    PRIMARY KEY,
				name       TEXT    NOT NULL,
				disabled   INTEGER NOT NULL DEFAULT 0,
				created_at TEXT    NOT NULL,
				notes      TEXT
			)`,

			// The default tenant owns everything that existed before tenancy did.
			`INSERT OR IGNORE INTO tenants (id, name, disabled, created_at, notes)
			 VALUES ('main', 'Main', 0, datetime('now'), 'Created automatically when this installation gained multi-tenancy. Everything that existed beforehand belongs to it.')`,

			`CREATE TABLE users_new (
				id            INTEGER PRIMARY KEY AUTOINCREMENT,
				-- Defaulted, not just NOT NULL. A single-tenant installation genuinely
				-- has one tenant called main, so existing code that never mentions a
				-- tenant is correct rather than broken. Multi-tenant callers go through
				-- the scoped handle and always pass one explicitly.
				tenant_id     TEXT    NOT NULL DEFAULT 'main' REFERENCES tenants(id) ON DELETE CASCADE,
				username      TEXT    NOT NULL COLLATE NOCASE,
				password_hash TEXT    NOT NULL,
				role          TEXT    NOT NULL,
				disabled      INTEGER NOT NULL DEFAULT 0,
				created_at    TEXT    NOT NULL,
				last_login    TEXT,
				-- Unique per tenant rather than globally. This is the whole reason
				-- for the rebuild.
				UNIQUE (tenant_id, username)
			)`,

			`INSERT INTO users_new (id, tenant_id, username, password_hash, role, disabled, created_at, last_login)
			 SELECT id, 'main', username, password_hash, role, disabled, created_at, last_login FROM users`,

			`DROP TABLE users`,
			`ALTER TABLE users_new RENAME TO users`,

			// Everything else takes a column. Backfilled to the default tenant in
			// the same statement, so there is never a moment where a row exists
			// with no owner.
			// audit takes a column. Backfilled in the same statement, so there is
			// never a moment where a row exists with no owner.
			//
			// Only tables this package owns are touched here. messages, queue and
			// fhir_resources belong to msgstore, queue and fhirserver, and each
			// adds its own column - a migration reaching into another package's
			// table fails on any installation where that feature was never
			// enabled and the table does not exist.
			`ALTER TABLE audit ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'main'`,

			// The index leads with tenant_id, because every scoped query filters on
			// it first. One that did not would make a fifty-tenant instance scan
			// forty-nine tenants' rows to answer one tenant's question.
			`CREATE INDEX IF NOT EXISTS audit_tenant ON audit(tenant_id, at DESC)`,
		},
	},

	{
		// API tokens, for machines rather than people.
		//
		// Sessions cannot serve this. They expire, which is correct for a browser and wrong for a server polling
		// its neighbour every fifteen seconds, and they are revoked as a group when somebody signs out - so a
		// person logging out would silently break a fleet view.
		//
		// Only the hash is stored, exactly as for sessions, so a stolen database yields no usable token. The
		// consequence is that a token cannot be shown again after it is created, which the interface has to say
		// plainly rather than offering a reveal that cannot work.
		name: "api-tokens",
		stmts: []string{
			`CREATE TABLE IF NOT EXISTS api_tokens (
				token_hash TEXT    PRIMARY KEY,
				tenant_id  TEXT    NOT NULL DEFAULT 'main',
				label      TEXT    NOT NULL,
				role       TEXT    NOT NULL,
				created_at TEXT    NOT NULL,
				created_by TEXT    NOT NULL,
				-- Nullable rather than defaulted: a token that has never been used and one used a second ago
				-- must be distinguishable, because the first is probably a mistake somebody should clean up.
				last_used  TEXT,
				-- Revoked rather than deleted, so the audit log's reference to a token still resolves to a
				-- label. A revoked token that vanished would leave entries naming nothing.
				revoked_at TEXT
			)`,

			`CREATE INDEX IF NOT EXISTS api_tokens_tenant ON api_tokens(tenant_id, label)`,
		},
	},
	{
		// Where a user's identity comes from.
		//
		// Two columns rather than one, because an identity is only unique within the provider that issued it: two
		// identity providers can both mint the subject "1000" and they are not the same person.
		//
		// Deliberately no automatic linking by email or username. An identity provider that lets somebody set their own
		// email address would otherwise be a way to take over a local account by matching its name - so a federated
		// sign-in matches on issuer and subject only, and creates a new account when there is no match.
		name: "external-identity",
		stmts: []string{
			`ALTER TABLE users ADD COLUMN auth_source TEXT NOT NULL DEFAULT 'local'`,
			`ALTER TABLE users ADD COLUMN external_issuer TEXT`,
			`ALTER TABLE users ADD COLUMN external_subject TEXT`,
			`ALTER TABLE users ADD COLUMN email TEXT`,

			// Partial, so the local accounts - which have no external identity - do not all collide on a pair of nulls.
			`CREATE UNIQUE INDEX IF NOT EXISTS users_external
			 ON users(external_issuer, external_subject)
			 WHERE external_issuer IS NOT NULL AND external_subject IS NOT NULL`,
		},
	},
	{
		// What a DICOM query source has already seen.
		//
		// The first source that needs local state at all. The database source has the remote mark its own rows
		// processed and the SFTP source moves the file, so in both cases the source of truth remembers. An archive
		// will not let us mark a study read, so we have to.
		//
		// Two things are recorded per channel. A high water mark bounds the next query, and the identifiers seen
		// inside the overlap period deduplicate it - because the overlap deliberately re-asks for a period already
		// polled, to catch a study registered with an earlier date than the day it arrived.
		//
		// Rows are pruned by the same overlap: an identifier older than the window can never be returned again, so
		// keeping it would grow the table forever to prevent a duplicate that cannot happen.
		name: "dicom-query-state",
		stmts: []string{
			`CREATE TABLE IF NOT EXISTS dicom_query_seen (
				tenant_id  TEXT NOT NULL DEFAULT 'main',
				channel    TEXT NOT NULL,
				-- The study, series or instance UID, depending on the query level. Whichever one the level makes
				-- unique, which is why the column is not named after any of them.
				identifier TEXT NOT NULL,
				seen_at    TEXT NOT NULL,
				PRIMARY KEY (tenant_id, channel, identifier)
			)`,

			`CREATE INDEX IF NOT EXISTS dicom_query_seen_age ON dicom_query_seen(tenant_id, channel, seen_at)`,

			`CREATE TABLE IF NOT EXISTS dicom_query_marks (
				tenant_id TEXT NOT NULL DEFAULT 'main',
				channel   TEXT NOT NULL,
				-- The point the next query starts from, less the overlap. Stored rather than derived from the seen
				-- table, because an empty poll still moves it forward and a table of identifiers cannot express
				-- "nothing matched, and that is not the same as never having looked".
				high_water TEXT NOT NULL,
				-- Distinguishes a first poll from a poll that found nothing, which decides whether anything is
				-- emitted. Without it, pointing this at an archive holding a million studies would emit a message
				-- for each on the first run.
				polled_at  TEXT NOT NULL,
				PRIMARY KEY (tenant_id, channel)
			)`,
		},
	},
	{
		// The identity provider's own identifier for an account, for SCIM provisioning.
		//
		// This is the stable join between the two systems and the reason it matters is renaming. Somebody who
		// marries and changes their username is the same person; matching on username alone would have the
		// provider provision a second account, disable that one, and leave the original enabled. Which is a
		// former employee with working access, arrived at by way of a wedding.
		//
		// Nullable and unindexed-unique rather than UNIQUE, because a locally created account has none and
		// several nulls are not a conflict. Uniqueness is enforced by a partial index instead.
		name: "scim-external-id",
		stmts: []string{
			`ALTER TABLE users ADD COLUMN external_id TEXT`,

			// Partial, so rows without one do not collide. Two accounts claiming the same provider identity is
			// a genuine conflict worth refusing: it means one of them will be deprovisioned in place of the
			// other.
			`CREATE UNIQUE INDEX IF NOT EXISTS users_external_id
				ON users(tenant_id, external_id) WHERE external_id IS NOT NULL`,
		},
	},
	{
		// Passkeys.
		//
		// Nothing in this table is secret. It holds public keys, so it could be published without letting anybody
		// sign in - which is the property that makes passkeys worth the work. A stolen password database is a
		// breach; a stolen copy of this is a list of public keys.
		//
		// The credential identifier is the primary key and is globally unique rather than per-tenant, because an
		// authenticator chooses it and a sign-in arrives with nothing but that identifier - there is no tenant to
		// scope the lookup by until the credential has been found. Two tenants colliding on one is not a practical
		// concern given the identifiers are random and at least 16 bytes, and treating a collision as a conflict is
		// the safe reading anyway.
		name: "passkeys",
		stmts: []string{
			`CREATE TABLE IF NOT EXISTS passkeys (
				credential_id TEXT    PRIMARY KEY,
				user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				tenant_id     TEXT    NOT NULL DEFAULT 'main',
				-- The COSE public key exactly as it arrived. Stored encoded rather than parsed so the bytes
				-- verified at registration are the bytes used later: re-encoding risks a different encoding of
				-- the same key, and then a signature is checked against something never verified.
				public_key    BLOB    NOT NULL,
				-- The algorithm, stored rather than re-derived. This is the column that makes algorithm
				-- confusion impossible.
				algorithm     INTEGER NOT NULL,
				sign_count    INTEGER NOT NULL DEFAULT 0,
				aaguid        BLOB,
				-- Whether the authenticator verified who the person was at registration. Says what this
				-- credential is capable of, so a user-verification requirement can be checked before somebody
				-- tries and fails.
				user_verified INTEGER NOT NULL DEFAULT 0,
				label         TEXT    NOT NULL,
				created_at    TEXT    NOT NULL,
				last_used     TEXT
			)`,

			// ON DELETE CASCADE above is the important part: deleting an account takes its passkeys with it. A
			// credential outliving its account would be one that authenticates nobody, and a lookup finding it
			// would then have to decide what to do - which is a decision better not to have.
			`CREATE INDEX IF NOT EXISTS passkeys_user ON passkeys(user_id)`,
			`CREATE INDEX IF NOT EXISTS passkeys_tenant ON passkeys(tenant_id, user_id)`,
		},
	},

	{
		// Challenges, kept in the database rather than in memory.
		//
		// In memory would be simpler and is wrong for two reasons. A restart between issuing a challenge and
		// receiving the response would refuse a legitimate sign-in, which is a bad first impression of a new
		// authentication method. And single-use has to survive concurrency: two requests presenting the same
		// challenge must not both succeed, and a delete-if-present in the database is how that is enforced.
		name: "webauthn-challenges",
		stmts: []string{
			`CREATE TABLE IF NOT EXISTS webauthn_challenges (
				challenge  TEXT    PRIMARY KEY,
				-- Null for a sign-in, where the account is not known until the credential comes back.
				user_id    INTEGER REFERENCES users(id) ON DELETE CASCADE,
				tenant_id  TEXT    NOT NULL DEFAULT 'main',
				purpose    TEXT    NOT NULL,
				expires_at TEXT    NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS webauthn_challenges_expiry ON webauthn_challenges(expires_at)`,
		},
	},
	{
		// Timestamps were written with time.RFC3339Nano, which drops trailing zeros from the fraction, so
		// stored strings varied in length. Every store here orders and compares those strings, and text
		// comparison only agrees with chronology when the width is fixed: after a shared prefix, a shorter
		// string has 'Z' where a longer one has a digit, and 'Z' sorts above every digit - so the earlier
		// instant sorted later. It showed up as an audit log listing same-second entries out of order.
		//
		// New rows are written fixed-width by internal/dbtime. This normalises the rows already on disk,
		// because an audit trail that is wrong about the order of events is wrong about the thing it exists
		// to record, and leaving history unfixed would mean the defect persisted for exactly the data
		// somebody would go back to read.
		//
		// The expression pads the fraction to nine digits: characters 1-19 are the date and time to the
		// second, then a fraction taken from after the dot if there is one, right-padded from a run of
		// zeros and cut to nine, then Z. Guarded to rows that end in Z and are not already the target
		// width, so it is idempotent and leaves anything unexpected alone rather than corrupting it.
		name: "fixed-width-timestamps",
		stmts: func() []string {
			pad := func(table, column string) string {
				return `UPDATE ` + table + ` SET ` + column + ` =
					substr(` + column + `, 1, 19) || '.' ||
					substr(
						CASE WHEN instr(` + column + `, '.') > 0
							THEN substr(` + column + `, instr(` + column + `, '.') + 1,
								length(` + column + `) - instr(` + column + `, '.') - 1)
							ELSE '' END || '000000000', 1, 9) || 'Z'
					WHERE ` + column + ` IS NOT NULL
					  AND ` + column + ` LIKE '%Z'
					  AND length(` + column + `) <> 30
					  AND length(` + column + `) >= 20`
			}
			return []string{
				pad("audit", "at"),
				pad("users", "created_at"),
				pad("sessions", "created_at"),
				pad("sessions", "expires_at"),
				pad("api_tokens", "created_at"),
				pad("api_tokens", "revoked_at"),
				pad("passkeys", "created_at"),
				pad("webauthn_challenges", "expires_at"),
				// The two dicom_query tables are deliberately absent: they are created by a later
				// migration, so they do not exist yet at this point and cannot be rewritten here. Their
				// columns are a poll high-water mark and a dedup timestamp compared against a cutoff days
				// wide, so a sub-microsecond boundary in old rows changes nothing. New rows are written
				// fixed-width like everything else.
			}
		}(),
	},
	{
		// Friction: what the product refused to do.
		//
		// Recorded so that difficulty using Perfuse can be counted rather than reasoned about by the person who wrote it.
		// The alternative was the queue item asking for one real operator to be watched, which stood unachievable for three
		// days - a refusal is evidence that did not come from the author, which is the only property that item really wanted.
		//
		// No column holds anything typed by the operator. The route has identifiers replaced, and the message is the
		// server's own sentence: a validation message names a field and a rule, and the value that broke the rule is very
		// often patient data.
		name: "friction",
		stmts: []string{
			`CREATE TABLE IF NOT EXISTS friction (
				id       INTEGER PRIMARY KEY AUTOINCREMENT,
				at       TEXT    NOT NULL,
				-- Method and path with identifiers replaced by {id}, so twenty refusals on twenty channels group into one
				-- finding. Without that, nothing ever reaches a count above one and the report is accurate and useless.
				route    TEXT    NOT NULL,
				status   INTEGER NOT NULL,
				message  TEXT    NOT NULL,
				-- How many per-field problems came back with it. A refusal naming eleven at once is a different experience
				-- from one naming a single typo.
				problems INTEGER NOT NULL DEFAULT 0,
				username TEXT
			)`,
			// Grouping is the only read pattern, and it is the one the report does on every load.
			`CREATE INDEX IF NOT EXISTS friction_grouping ON friction (route, status, message)`,
		},
	},
}

// applyMigrations runs whatever has not run yet.
func (s *Store) applyMigrations(ctx context.Context) error {
	have, err := s.schemaVersion(ctx)
	if err != nil {
		return err
	}

	for i := have; i < len(migrations); i++ {
		m := migrations[i]
		if err := s.applyMigration(ctx, i, m); err != nil {
			return fmt.Errorf("store: migration %d (%s): %w", i, m.name, err)
		}
	}
	return nil
}

// applyMigration runs one migration and records it, both or neither.
func (s *Store) applyMigration(ctx context.Context, index int, m migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Rollback on any path that does not commit. A migration that half-applied
	// leaves a schema no version of the code expects, which is harder to recover
	// from than one that failed cleanly.
	defer func() { _ = tx.Rollback() }()

	for j, stmt := range m.stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("statement %d: %w", j, err)
		}
	}

	// The recorded version is the count of migrations applied, so it is also the
	// index of the next one to run.
	if err := setSchemaVersion(ctx, tx, index+1); err != nil {
		return err
	}
	return tx.Commit()
}

// schemaVersion reads how many migrations have been applied.
//
// An empty schema_version table means zero, which is correct both for a fresh
// database and for one created before this runner existed.
func (s *Store) schemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM schema_version LIMIT 1`).Scan(&v)
	switch {
	case err == sql.ErrNoRows:
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("store: reading schema version: %w", err)
	}
	return v, nil
}

func setSchemaVersion(ctx context.Context, tx *sql.Tx, v int) error {
	res, err := tx.ExecContext(ctx, `UPDATE schema_version SET version = ?`, v)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// First run: the row does not exist yet.
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, v); err != nil {
			return err
		}
	}
	return nil
}

// DefaultTenant is the tenant that owns everything created before tenancy existed,
// and the one a single-tenant installation uses without ever being told about it.
const DefaultTenant = "main"
