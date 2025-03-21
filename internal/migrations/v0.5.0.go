package migrations

import (
	"github.com/jmoiron/sqlx"
	"github.com/knadh/koanf/v2"
	"github.com/knadh/stuffbin"
)

// V0_5_0 updates the database schema to v0.5.0.
func V0_5_0(db *sqlx.DB, fs stuffbin.FileSystem, ko *koanf.Koanf) error {
	_, err := db.Exec(`
		DO $$
		BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'applied_sla_status') THEN
				CREATE TYPE "applied_sla_status" AS ENUM ('pending', 'breached', 'met', 'partially_met');
			END IF;
		END$$;
	`)
	if err != nil {
		return err
	}

	_, err = db.Exec(`
		ALTER TABLE applied_slas ADD COLUMN IF NOT EXISTS status applied_sla_status DEFAULT 'pending' NOT NULL;
	`)
	if err != nil {
		return err
	}

	_, err = db.Exec(`
		CREATE INDEX IF NOT EXISTS index_applied_slas_on_status ON applied_slas(status);
	`)
	if err != nil {
		return err
	}

	_, err = db.Exec(`
		INSERT INTO settings (key, value)
		VALUES 
			('notification.email.tls_type', '"starttls"'::jsonb),
			('notification.email.tls_skip_verify', 'false'::jsonb),
			('notification.email.hello_hostname', '""'::jsonb)
		ON CONFLICT (key) DO NOTHING;
	`)
	if err != nil {
		return err
	}

	// Update tls_type for IMAP
	_, err = db.Exec(`
		UPDATE inboxes
		SET config = jsonb_set(config, '{imap,0,tls_type}', '"tls"', true)
		WHERE config->'imap' IS NOT NULL AND config#>'{imap,0,tls_type}' IS NULL;
	`)
	if err != nil {
		return err
	}

	// Update tls_skip_verify for IMAP
	_, err = db.Exec(`
		UPDATE inboxes
		SET config = jsonb_set(config, '{imap,0,tls_skip_verify}', 'false', true)
		WHERE config->'imap' IS NOT NULL AND config#>'{imap,0,tls_skip_verify}' IS NULL;
	`)
	if err != nil {
		return err
	}

	// Update scan_inbox_since for IMAP
	_, err = db.Exec(`
		UPDATE inboxes
		SET config = jsonb_set(config, '{imap,0,scan_inbox_since}', '"48h"', true)
		WHERE config->'imap' IS NOT NULL AND config#>'{imap,0,scan_inbox_since}' IS NULL;
	`)
	if err != nil {
		return err
	}

	// Update tls_type for SMTP
	_, err = db.Exec(`
		UPDATE inboxes
		SET config = jsonb_set(config, '{smtp,0,tls_type}', '"starttls"', true)
		WHERE config->'smtp' IS NOT NULL AND config#>'{smtp,0,tls_type}' IS NULL;
	`)
	if err != nil {
		return err
	}

	// Update tls_skip_verify for SMTP
	_, err = db.Exec(`
		UPDATE inboxes
		SET config = jsonb_set(config, '{smtp,0,tls_skip_verify}', 'false', true)
		WHERE config->'smtp' IS NOT NULL AND config#>'{smtp,0,tls_skip_verify}' IS NULL;
	`)
	if err != nil {
		return err
	}

	// Update hello_hostname for SMTP
	_, err = db.Exec(`
		UPDATE inboxes
		SET config = jsonb_set(config, '{smtp,0,hello_hostname}', '""', true)
		WHERE config->'smtp' IS NOT NULL AND config#>'{smtp,0,hello_hostname}' IS NULL;
	`)
	if err != nil {
		return err
	}
	return nil
}
