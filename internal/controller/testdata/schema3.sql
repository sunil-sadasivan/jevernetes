-- Pristine schema 3 DDL from commit a3d42e0.
BEGIN IMMEDIATE;
CREATE TABLE verdicts (key TEXT PRIMARY KEY, judgment TEXT NOT NULL, created INTEGER NOT NULL, expires INTEGER NOT NULL);
 CREATE INDEX verdict_expiry ON verdicts(expires);
 CREATE TABLE seen (key TEXT PRIMARY KEY, at INTEGER NOT NULL, decision TEXT NOT NULL);
 CREATE INDEX seen_time ON seen(at);
 CREATE TABLE incidents (key TEXT PRIMARY KEY, window INTEGER NOT NULL, count INTEGER NOT NULL, level INTEGER NOT NULL, last INTEGER NOT NULL, sequence INTEGER NOT NULL, touched INTEGER NOT NULL, last_decision TEXT CHECK(last_decision IN ('review','notify')));
 CREATE INDEX incident_time ON incidents(touched);
 CREATE TABLE outbox (id TEXT PRIMARY KEY, incident TEXT NOT NULL, payload TEXT NOT NULL, status TEXT NOT NULL, attempts INTEGER NOT NULL, due INTEGER NOT NULL, updated INTEGER NOT NULL);
 CREATE INDEX outbox_due ON outbox(status,due);
 CREATE INDEX outbox_time ON outbox(status,updated);
 CREATE TABLE delivery_clock (id INTEGER PRIMARY KEY CHECK(id=1), next INTEGER NOT NULL);
 INSERT INTO delivery_clock VALUES(1,0);
PRAGMA user_version=3;
COMMIT;
