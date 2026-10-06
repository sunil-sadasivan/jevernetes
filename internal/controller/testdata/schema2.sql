-- Pristine schema 2 creation and column upgrade from commit b9eeb6c.
BEGIN IMMEDIATE;
            CREATE TABLE IF NOT EXISTS verdicts (key TEXT PRIMARY KEY, judgment TEXT NOT NULL, created INTEGER NOT NULL, expires INTEGER NOT NULL);
            CREATE INDEX IF NOT EXISTS verdict_expiry ON verdicts(expires);
            CREATE TABLE IF NOT EXISTS seen (key TEXT PRIMARY KEY, at INTEGER NOT NULL, decision TEXT NOT NULL);
            CREATE INDEX IF NOT EXISTS seen_time ON seen(at);
            CREATE TABLE IF NOT EXISTS incidents (key TEXT PRIMARY KEY, window INTEGER NOT NULL, count INTEGER NOT NULL, level INTEGER NOT NULL, last INTEGER NOT NULL, sequence INTEGER NOT NULL, touched INTEGER NOT NULL);
            CREATE INDEX IF NOT EXISTS incident_time ON incidents(touched);
            CREATE TABLE IF NOT EXISTS outbox (id TEXT PRIMARY KEY, incident TEXT NOT NULL, payload TEXT NOT NULL, status TEXT NOT NULL, attempts INTEGER NOT NULL, due INTEGER NOT NULL, updated INTEGER NOT NULL);
            CREATE INDEX IF NOT EXISTS outbox_due ON outbox(status,due);
            CREATE INDEX IF NOT EXISTS outbox_time ON outbox(status,updated);
            CREATE TABLE IF NOT EXISTS delivery_clock (id INTEGER PRIMARY KEY CHECK(id=1), next INTEGER NOT NULL);
            INSERT OR IGNORE INTO delivery_clock VALUES (1,0);
ALTER TABLE incidents ADD COLUMN last_decision TEXT CHECK(last_decision IN ('review','notify'));
PRAGMA user_version=2;
COMMIT;
